package notification

import (
	"context"
	"testing"
	"time"
)

func TestDispatcher_Enqueue(t *testing.T) {
	d := NewDispatcher("", 0, "", "", "test@local", nil)

	// Fill the buffer to the brim
	for i := 0; i < 500; i++ {
		d.Enqueue(AlertPayload{JobID: "test"})
	}

	// The 501st should not block the main thread. It must be dropped.
	done := make(chan bool)
	go func() {
		d.Enqueue(AlertPayload{JobID: "test-dropped"})
		done <- true
	}()

	select {
	case <-done:
		// Success! The enqueue function returned immediately despite full channel.
	case <-time.After(1 * time.Second):
		t.Fatal("Enqueue blocked the goroutine! Non-blocking logic failed.")
	}

	d.Stop()
}

func TestDispatcher_WorkerGracefulShutdown(t *testing.T) {
	d := NewDispatcher("", 0, "", "", "test@local", nil)

	// We only start the dispatcher, no emails
	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx, 2)

	// Stopping before context cancels should drain and exit cleanly
	done := make(chan bool)
	go func() {
		d.Stop()
		done <- true
	}()

	select {
	case <-done:
		// Workers shut down properly when queue closed
	case <-time.After(2 * time.Second):
		t.Fatal("Dispatcher Stop() hangs indefinitely!")
	}
	cancel()
}
