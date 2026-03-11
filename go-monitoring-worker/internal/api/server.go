package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/monitoring-system/go-worker/internal/models"
	"github.com/monitoring-system/go-worker/internal/scheduler"
)

// Server expone un puerto local (ej: :8081) que el Servidor Principal (FastAPI) consume
type Server struct {
	sched scheduler.Manager
}

func NewServer(m scheduler.Manager) *Server {
	return &Server{sched: m}
}

// Router returns the configured ServeMux
func (s *Server) Router() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/targets/sync", s.HandleSyncTargets())
	mux.HandleFunc("/health", s.HandleHealthCheck())

	// Inicializamos e inyectamos el Audito de Performance Pprof (Aislado)
	profiler := NewProfiler()
	// Rate 1: Bloqueos y Contención reportados al 100% (Ajustar en producción masiva si requiere)
	profiler.EnableAdvancedProfiling(1, 1)
	profiler.RegisterRoutes(mux)

	return mux
}

// POST /api/targets/sync -> FastAPI avisa que las configuraciones de BD cambiaron
func (s *Server) HandleSyncTargets() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		var targets []models.TargetConfig
		if err := json.NewDecoder(r.Body).Decode(&targets); err != nil {
			http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
			return
		}

		s.sched.SyncConfiguration(targets)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status": "synchronized", "count": ` + fmt.Sprint(len(targets)) + `}`))
	}
}

// GET /health -> Saber si este worker está vivo
func (s *Server) HandleHealthCheck() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}
}
