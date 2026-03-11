package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/monitoring-system/go-worker/internal/alerts"
	"github.com/monitoring-system/go-worker/internal/api"
	"github.com/monitoring-system/go-worker/internal/config"
	"github.com/monitoring-system/go-worker/internal/notification"
	"github.com/monitoring-system/go-worker/internal/ping"
	"github.com/monitoring-system/go-worker/internal/scheduler"
	"github.com/monitoring-system/go-worker/internal/storage/influx"
)

func main() {
	log.Println("Bootstrapping IpMonitor Data Plane.")

	// 1. Cargar Configuración usando Viper o Fallbacks locales para debug
	cfg, err := config.Load()
	if err != nil {
		log.Printf("WARN: Env configuration errors (%v). Using local fallback...", err)
		cfg = &config.Config{
			WorkerID:        "local-mac-worker",
			ControlPlaneURL: "http://localhost:8000",
			HMACSecret:      "development_secret_key_that_is_long_enough",
			InfluxURL:       "http://localhost:8086",
			InfluxToken:     "test-token",
			InfluxOrg:       "monitoring",
			InfluxBucket:    "monitorings",
		}
	}

	influxCfg := influx.Config{
		URL:    cfg.InfluxURL,
		Token:  cfg.InfluxToken,
		Org:    cfg.InfluxOrg,
		Bucket: cfg.InfluxBucket,
	}

	// 2. Instanciar Clientes Base
	influxWriter := influx.NewInfluxWriter(influxCfg)

	// Iniciar SMTP Dispatcher en Goroutines Background
	smtpLogger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	smtpDispatcher := notification.NewDispatcher(
		cfg.SMTPHost,
		cfg.SMTPPort,
		cfg.SMTPUsername,
		cfg.SMTPPassword,
		cfg.SMTPFrom,
		smtpLogger,
	)

	// Start 2 concurrent background workers
	smtpDispatcher.Start(context.Background(), 2)

	mailer := alerts.NewBatchNotifier(10*time.Second, smtpDispatcher)

	// 3. Ensamblar Lógica Core
	alertEngine := alerts.NewEngine(mailer, influxWriter)
	networkExec := ping.NewNetworkExecutor()
	// Cap Workers at 50 to strictly bound Memory
	pingService := ping.NewWorkerPool(50, networkExec, alertEngine, influxWriter)

	// 4. Iniciar Scheduler Central
	manager := scheduler.NewManager()

	// 5. Instanciar Cliente M2M para arquitecturas PULL Auto-Scalables
	m2mClient := api.NewM2MClient(cfg)

	// 6. Iniciar goroutines maestras con Contexto Cancelable
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pingService.StartWorkers(ctx)
	go manager.StartTicks(ctx, pingService)

	// Arquitectura PULL: Ticker Periódico pidiendo trabajos al Control Plane
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		// Initial immediate pull
		if targets, err := m2mClient.FetchTargets(ctx); err == nil {
			manager.SyncConfiguration(targets)
		} else {
			log.Printf("Boot M2M Pull Failed: %v", err)
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if targets, err := m2mClient.FetchTargets(ctx); err == nil {
					manager.SyncConfiguration(targets)
				} else {
					log.Printf("Periodic M2M Pull Failed: %v", err)
				}
			}
		}
	}()

	// 7. Levantar API Interna (Pprof)
	apiServer := api.NewServer(manager)
	server := &http.Server{
		Addr:    ":8081",
		Handler: apiServer.Router(),
	}

	go func() {
		log.Println("Internal Pprof/Metric API available at :8081")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Listen error: %s\n", err)
		}
	}()

	// 8. Manejo OS Signals para Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutdown OS signal received. Graceful termination in progress...")
	cancel() // Aborta PULL loops y Pings en curso

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	server.Shutdown(shutdownCtx)

	if impl, ok := influxWriter.(interface{ Close() }); ok {
		impl.Close()
	}
	log.Println("Monitor process terminated safely.")
}
