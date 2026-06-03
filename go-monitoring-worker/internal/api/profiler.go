package api

import (
	"net/http"
	"net/http/pprof"
	"runtime"
)

// Profiler maneja la exposición limpia y explícita de las herramientas de auditoría pprof
// de Golang sin utilizar variables globales ni el DefaultServeMux (Clean Architecture).
type Profiler struct{}

func NewProfiler() *Profiler {
	return &Profiler{}
}

// EnableAdvancedProfiling activa la recolección profunda para Mutexes y Bloqueos (Deadlocks)
func (p *Profiler) EnableAdvancedProfiling(blockRate int, mutexFraction int) {
	// runtime.SetBlockProfileRate controla la fracción de bloqueos de goroutines
	// por contención de recursos que se reportan.
	runtime.SetBlockProfileRate(blockRate)

	// runtime.SetMutexProfileFraction controla la fracción de eventos de contención
	// de mutex que se reportan en el perfil de mutex.
	runtime.SetMutexProfileFraction(mutexFraction)
}

// RegisterRoutes acopla los handlers de auditoría al enrutador (Mux) provisto.
func (p *Profiler) RegisterRoutes(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
	// Puntos de entrada para auditorías de uso de CPU, Goroutines Colgadas y Memory Leaks.
	mux.Handle("/debug/pprof/", auth(http.HandlerFunc(pprof.Index)))
	mux.Handle("/debug/pprof/cmdline", auth(http.HandlerFunc(pprof.Cmdline)))
	mux.Handle("/debug/pprof/profile", auth(http.HandlerFunc(pprof.Profile))) // Uso de CPU (Ej. 30s)
	mux.Handle("/debug/pprof/symbol", auth(http.HandlerFunc(pprof.Symbol)))
	mux.Handle("/debug/pprof/trace", auth(http.HandlerFunc(pprof.Trace)))

	// Sub-handlers que administra el pprof.Index pero que explicitamos por seguridad/claridad.
	mux.Handle("/debug/pprof/goroutine", auth(pprof.Handler("goroutine"))) // Goroutines colgadas
	mux.Handle("/debug/pprof/heap", auth(pprof.Handler("heap")))           // Memory leaks
	mux.Handle("/debug/pprof/threadcreate", auth(pprof.Handler("threadcreate")))
	mux.Handle("/debug/pprof/block", auth(pprof.Handler("block"))) // Bloqueos (Starvation)
	mux.Handle("/debug/pprof/mutex", auth(pprof.Handler("mutex"))) // Contención de Locks
}
