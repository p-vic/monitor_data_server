package api

import (
	"encoding/json"
	"net/http"
	"syscall"

	"github.com/monitoring-system/go-worker/internal/scheduler"
)

// Server expone un puerto local (ej: :8081) que el Servidor Principal (FastAPI) consume
type Server struct {
	sched          scheduler.Manager
	reloadCh       chan struct{}
	internalSecret string
}

func NewServer(m scheduler.Manager, reloadCh chan struct{}, internalSecret string) *Server {
	return &Server{
		sched:          m,
		reloadCh:       reloadCh,
		internalSecret: internalSecret,
	}
}

// Router returns the configured ServeMux
func (s *Server) Router() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/reload", s.HandleReload())
	mux.HandleFunc("/internal/storage", s.HandleStorageInfo())
	mux.HandleFunc("/health", s.HandleHealthCheck())

	// Inicializamos e inyectamos el Audito de Performance Pprof (Aislado)
	profiler := NewProfiler()
	// Rate 1: Bloqueos y Contención reportados al 100% (Ajustar en producción masiva si requiere)
	profiler.EnableAdvancedProfiling(1, 1)
	profiler.RegisterRoutes(mux)

	return mux
}

// POST /internal/reload -> FastAPI notifica que hubo un cambio en la configuración de targets.
// El handler responde 202 inmediatamente y delega el fetch al PULL loop via canal (fire-and-forget).
// Si ya hay una señal pendiente en el canal (buffer=1), la nueva se descarta silenciosamente:
// el reload en vuelo ya va a capturar el estado más reciente.
func (s *Server) HandleReload() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		if r.Header.Get("X-Internal-Secret") != s.internalSecret {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Non-blocking send: descarta si ya hay un reload encolado
		select {
		case s.reloadCh <- struct{}{}:
		default:
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"status": "reload_queued"}`))
	}
}

// GET /health -> Saber si este worker está vivo
func (s *Server) HandleHealthCheck() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}
}

// GET /internal/storage -> Disk usage stats of this worker node's filesystem
func (s *Server) HandleStorageInfo() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Internal-Secret") != s.internalSecret {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		var stat syscall.Statfs_t
		if err := syscall.Statfs("/", &stat); err != nil {
			http.Error(w, `{"error":"failed to get disk stats"}`, http.StatusInternalServerError)
			return
		}

		blockSize := uint64(stat.Bsize)
		total := stat.Blocks * blockSize
		free := stat.Bfree * blockSize
		available := stat.Bavail * blockSize
		used := total - free

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]uint64{
			"disk_total_bytes":     total,
			"disk_used_bytes":      used,
			"disk_available_bytes": available,
		})
	}
}
