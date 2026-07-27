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

	telegramSender := notification.NewTelegramSender(cfg.TelegramBotToken)

	mailer := alerts.NewBatchNotifier(10*time.Second, smtpDispatcher, telegramSender)

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

	// Canal de señalización push: buffer=1 para descartar señales duplicadas
	// mientras un reload ya está en vuelo.
	reloadCh := make(chan struct{}, 1)

	// Arquitectura PULL con Push-Signal: el ticker de 5min es el fallback de seguridad.
	// Los cambios en tiempo real se propagan vía señal push desde FastAPI → /internal/reload.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		doSync := func(reason string) {
			if targets, err := m2mClient.FetchTargets(ctx); err == nil {
				alertEngine.UpdateTargets(targets)
				manager.SyncConfiguration(targets)
				log.Printf("Sync OK [%s]: %d targets loaded", reason, len(targets))
			} else {
				log.Printf("Sync FAILED [%s]: %v", reason, err)
			}
		}

		// Pull inicial en el arranque
		doSync("boot")

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				doSync("periodic-fallback")
			case <-reloadCh:
				doSync("push-signal")
			}
		}
	}()

	// 7. Levantar API Interna (Pprof + Reload endpoint)
	apiServer := api.NewServer(manager, reloadCh, cfg.HMACSecret, cfg.InfluxURL, cfg.InfluxToken)
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

	// Stop SMTP Dispatcher gracefully to send pending emails
	log.Println("Stopping SMTP dispatcher queue...")
	smtpDispatcher.Stop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	server.Shutdown(shutdownCtx)

	if impl, ok := influxWriter.(interface{ Close() }); ok {
		impl.Close()
	}
	log.Println("Monitor process terminated safely.")
}
