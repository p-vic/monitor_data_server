package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/monitoring-system/go-worker/internal/config"
)

func TestM2MClient_FetchJobs_Success(t *testing.T) {
	expectedSecret := "super-secure-test-secret"
	mockWorkerID := "worker-123"

	// Mock FastAPI Control Plane Server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Verify Headers exist
		workerID := r.Header.Get("X-Worker-ID")
		timestamp := r.Header.Get("X-Timestamp")
		signature := r.Header.Get("X-Signature")

		if workerID != mockWorkerID {
			t.Errorf("Expected Worker-ID %s, got %s", mockWorkerID, workerID)
		}

		if timestamp == "" || signature == "" {
			t.Errorf("Missing cryptographic headers")
		}

		// 2. Read Request Body for integrity verification
		body, _ := io.ReadAll(r.Body)

		// 3. Re-calculate HMAC locally to ensure Go Client signed it correctly
		message := append([]byte(timestamp), []byte(".")...)
		message = append(message, body...)

		mac := hmac.New(sha256.New, []byte(expectedSecret))
		mac.Write(message)
		expectedSignature := hex.EncodeToString(mac.Sum(nil))

		if signature != expectedSignature {
			t.Errorf("Client generated invalid HMAC signature: expected %s, got %s", expectedSignature, signature)
		}

		// 4. Return dummy SyncResponse
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		response := `{
			"worker_id": "worker-123",
			"tasks_count": 1,
			"tasks": [
				{"id": "job-1", "ip_address": "1.1.1.1", "is_active": true, "check_interval": 30, "timeout": 5}
			]
		}`
		w.Write([]byte(response))
	}))
	defer server.Close()

	// Inject dynamic URL from Test Server into Config
	cfg := &config.Config{
		ControlPlaneURL: server.URL,
		WorkerID:        mockWorkerID,
		HMACSecret:      expectedSecret,
	}

	client := NewM2MClient(cfg)

	// Execute Fetch
	jobs, err := client.FetchTargets(context.Background())
	if err != nil {
		t.Fatalf("FetchTargets failed unexpectedly: %v", err)
	}

	if len(jobs) != 1 {
		t.Fatalf("Expected 1 job returned, got %d", len(jobs))
	}

	if jobs[0].IPAddress != "1.1.1.1" {
		t.Errorf("Job mapping failed, got wrong ip_address: %v", jobs[0])
	}
}

func TestM2MClient_FetchTargets_TimeoutProtection(t *testing.T) {
	// Simulate Slowloris / Slow Server (takes 2 seconds)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		ControlPlaneURL: server.URL,
	}

	client := NewM2MClient(cfg)
	// Force a 500ms timeout on the context intentionally
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := client.FetchTargets(ctx)

	if err == nil {
		t.Fatal("Expected error due to context timeout, got success")
	}

	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("Expected context deadline error, got: %v", err)
	}
}

func TestM2MClient_FetchTargets_MemoryExhaustionProtection(t *testing.T) {
	// Simulate a malicious server sending a 10MB response to crash the worker
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		// Send 10MB of garbage
		garbage := make([]byte, 10*1024*1024)
		w.Write(garbage)
	}))
	defer server.Close()

	cfg := &config.Config{
		ControlPlaneURL: server.URL,
	}

	client := NewM2MClient(cfg)
	_, err := client.FetchTargets(context.Background())

	// Our limitReader is set to 5MB, so io.ReadAll won't crash
	// but JSON unmarshal should fail spectacularly on the truncated or garbage bytes.
	if err == nil {
		t.Fatal("Expected JSON Unmarshal error on truncated/giant response, got success")
	}
}
