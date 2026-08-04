// IPMonitor Agent — installable binary for customer servers.
//
// Usage:
//
//	ipmonitor-agent register --token <TOKEN> --url <CP_URL>
//	ipmonitor-agent run
//
// Credentials written by 'register' are read by 'run'.
// Default credentials path: /etc/ipmonitor-agent/credentials.json (Linux)
//
//	%ProgramData%\IPMonitor\credentials.json (Windows)
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/monitoring-system/go-worker/internal/alerts"
	"github.com/monitoring-system/go-worker/internal/engines"
	"github.com/monitoring-system/go-worker/internal/models"
	"github.com/monitoring-system/go-worker/internal/notification"
	"github.com/monitoring-system/go-worker/internal/ping"
	"github.com/monitoring-system/go-worker/internal/reporter"
	"github.com/monitoring-system/go-worker/internal/scheduler"
)

const agentVersion = "1.0.0"

// ─── Credentials ────────────────────────────────────────────────────────────

// Credentials is persisted to disk after a successful 'register' call.
type Credentials struct {
	AgentID      string `json:"agent_id"`
	HMACSecret   string `json:"hmac_secret"`
	CPURL        string `json:"cp_url"`
	PollInterval int    `json:"poll_interval"` // seconds
}

// credentialsPath returns the platform-appropriate default path.
// Override with IPMONITOR_CREDENTIALS_PATH env var.
func credentialsPath() string {
	if path := os.Getenv("IPMONITOR_CREDENTIALS_PATH"); path != "" {
		return path
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("PROGRAMDATA"), "IPMonitor", "credentials.json")
	}
	return "/etc/ipmonitor-agent/credentials.json"
}

func loadCredentials() (*Credentials, error) {
	path := credentialsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("credentials not found at %s — run 'ipmonitor-agent register' first: %w", path, err)
	}
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("corrupt credentials file at %s: %w", path, err)
	}
	if c.AgentID == "" || c.HMACSecret == "" || c.CPURL == "" {
		return nil, fmt.Errorf("credentials file at %s is incomplete", path)
	}
	return &c, nil
}

func saveCredentials(c *Credentials) error {
	path := credentialsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// Write atomically via temp file + rename to avoid partial writes.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ─── Entry Point ─────────────────────────────────────────────────────────────

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "register":
		cmdRegister(os.Args[2:])
	case "run":
		cmdRun()
	case "service":
		cmdService(os.Args[2:])
	case "version":
		fmt.Printf("ipmonitor-agent %s %s/%s\n", agentVersion, runtime.GOOS, runtime.GOARCH)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `ipmonitor-agent <subcommand> [flags]

Subcommands:
  register  --token <TOKEN> --url <CP_URL>   Register with the Control Plane
  run                                         Start monitoring (uses saved credentials)
  service   install|uninstall|start|stop      Manage the system service (requires root)
  version                                     Print version and exit`)
}

// ─── register ────────────────────────────────────────────────────────────────

func cmdRegister(args []string) {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	token := fs.String("token", "", "Installation token (required)")
	cpURL := fs.String("url", "", "Control Plane URL, e.g. https://ipmonitor.yaurima.com (required)")
	_ = fs.Parse(args)

	if *token == "" || *cpURL == "" {
		fmt.Fprintln(os.Stderr, "register: --token and --url are required")
		fs.Usage()
		os.Exit(1)
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}

	body, _ := json.Marshal(map[string]any{
		"token":        *token,
		"hostname":     hostname,
		"platform":     runtime.GOOS,
		"arch":         runtime.GOARCH,
		"version":      agentVersion,
		"capabilities": []string{"icmp"},
	})

	endpoint := *cpURL + "/api/v1/agents/register"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(body))
	if err != nil {
		log.Fatalf("register: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("register: could not reach Control Plane at %s: %v", *cpURL, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	if resp.StatusCode != http.StatusCreated {
		log.Fatalf("register: Control Plane returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		AgentID      string `json:"agent_id"`
		HMACSecret   string `json:"hmac_secret"`
		PollInterval int    `json:"poll_interval"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		log.Fatalf("register: failed to parse response: %v", err)
	}

	creds := &Credentials{
		AgentID:      result.AgentID,
		HMACSecret:   result.HMACSecret,
		CPURL:        *cpURL,
		PollInterval: result.PollInterval,
	}
	if creds.PollInterval <= 0 {
		creds.PollInterval = 30
	}

	if err := saveCredentials(creds); err != nil {
		log.Fatalf("register: failed to save credentials to %s: %v", credentialsPath(), err)
	}

	fmt.Printf("Agent registered successfully\n")
	fmt.Printf("  Agent ID: %s\n", creds.AgentID)
	fmt.Printf("  Saved to: %s\n", credentialsPath())
	fmt.Printf("  Start with: ipmonitor-agent run\n")
}

// ─── run ─────────────────────────────────────────────────────────────────────

// agentSyncResponse mirrors GET /api/v1/agents/{id}/config response.
type agentSyncResponse struct {
	AgentID    string            `json:"agent_id"`
	TasksCount int               `json:"tasks_count"`
	Tasks      []agentTaskConfig `json:"tasks"`
}

type agentTaskConfig struct {
	ID                  string      `json:"id"`
	TenantID            string      `json:"tenant_id"`
	Name                string      `json:"name"`
	IPAddress           string      `json:"ip_address"`
	Group               string      `json:"group"`
	ParentID            *string     `json:"parent_id"`
	CheckInterval       int         `json:"check_interval"`
	Timeout             int         `json:"timeout"`
	WarningLatency      float64     `json:"warning_latency"`
	MaxLatency          float64     `json:"max_latency"`
	AlertEmails         []string    `json:"alert_emails"`
	AlertConfig         interface{} `json:"alert_config"`
	IsActive            bool        `json:"is_active"`
	NotificationChannel string      `json:"notification_channel"`
	TelegramChatID      string      `json:"telegram_chat_id"`
}

func cmdRun() {
	creds, err := loadCredentials()
	if err != nil {
		log.Fatalf("run: %v", err)
	}

	log.Printf("Starting IPMonitor Agent %s  agent_id=%s", agentVersion, creds.AgentID)

	smtpLogger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	smtpDispatcher := notification.NewDispatcher(
		os.Getenv("SMTP_HOST"),
		envInt("SMTP_PORT", 587),
		os.Getenv("SMTP_USERNAME"),
		os.Getenv("SMTP_PASSWORD"),
		envStr("SMTP_FROM", "alerts@ipmonitor.local"),
		smtpLogger,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	smtpDispatcher.Start(ctx, 2)

	telegramSender := notification.NewTelegramSender(os.Getenv("TELEGRAM_BOT_TOKEN"))
	mailer := alerts.NewBatchNotifier(10*time.Second, smtpDispatcher, telegramSender)

	// Engine registry: ICMPEngine registers itself on construction.
	networkExec := ping.NewNetworkExecutor()
	_ = engines.NewICMPEngine(networkExec) // registers "icmp"

	icmpEngine := engines.Default()

	// Reporter ships batched results to the Control Plane instead of writing to InfluxDB.
	rep := reporter.New(creds.AgentID, creds.HMACSecret, creds.CPURL, 5*time.Second)

	// nil influx.Writer is safe: engine_impl already guards with != nil check.
	alertEngine := alerts.NewEngine(mailer, nil)

	pool := newAgentPool(50, icmpEngine, alertEngine, rep)
	mgr := scheduler.NewManager()

	go pool.StartWorkers(ctx)
	go mgr.StartTicks(ctx, pool)
	go rep.Run(ctx)

	// Heartbeat every 60s
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := rep.Heartbeat(ctx); err != nil {
					log.Printf("WARN heartbeat: %v", err)
				}
			}
		}
	}()

	// Config sync loop
	go func() {
		interval := time.Duration(creds.PollInterval) * time.Second
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		doSync := func(reason string) {
			targets, err := fetchConfig(ctx, creds)
			if err != nil {
				log.Printf("sync FAILED [%s]: %v", reason, err)
				return
			}
			alertEngine.UpdateTargets(targets)
			mgr.SyncConfiguration(targets)
			log.Printf("sync OK [%s]: %d targets", reason, len(targets))
		}

		doSync("boot")

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				doSync("periodic")
			}
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutdown signal received. Draining...")
	cancel()
	smtpDispatcher.Stop()
	log.Println("Agent stopped.")
}

// fetchConfig pulls the target list from the Control Plane with HMAC auth.
func fetchConfig(ctx context.Context, creds *Credentials) ([]models.TargetConfig, error) {
	// GET with an empty body — we still sign the empty payload for consistency.
	payload := []byte(`{}`)
	endpoint := fmt.Sprintf("%s/api/v1/agents/%s/config", creds.CPURL, creds.AgentID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, bytes.NewBuffer(payload))
	if err != nil {
		return nil, err
	}

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(creds.HMACSecret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	sig := hex.EncodeToString(mac.Sum(nil))

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-ID", creds.AgentID)
	req.Header.Set("X-Timestamp", timestamp)
	req.Header.Set("X-Signature", sig)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("CP returned %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return nil, err
	}

	var syncResp agentSyncResponse
	if err := json.Unmarshal(body, &syncResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	var targets []models.TargetConfig
	for _, t := range syncResp.Tasks {
		alertConfigJSON := ""
		if t.AlertConfig != nil {
			b, _ := json.Marshal(t.AlertConfig)
			alertConfigJSON = string(b)
		}
		parentID := ""
		if t.ParentID != nil {
			parentID = *t.ParentID
		}
		targets = append(targets, models.TargetConfig{
			ID:                  t.ID,
			TenantID:            t.TenantID,
			Name:                t.Name,
			IPAddress:           t.IPAddress,
			Group:               t.Group,
			ParentID:            parentID,
			CheckInterval:       time.Duration(t.CheckInterval) * time.Second,
			Timeout:             time.Duration(t.Timeout) * time.Second,
			WarningLatency:      t.WarningLatency,
			MaxLatency:          t.MaxLatency,
			AlertEmails:         t.AlertEmails,
			AlertConfig:         alertConfigJSON,
			NotificationChannel: t.NotificationChannel,
			TelegramChatID:      t.TelegramChatID,
			IsActive:            t.IsActive,
		})
	}
	return targets, nil
}

// ─── Agent Worker Pool ───────────────────────────────────────────────────────
// Mirrors ping.workerPool but sends results to reporter instead of InfluxDB.
// Implements ping.Service so it can be passed to scheduler.Manager.

type agentPool struct {
	maxWorkers  int
	engine      engines.Engine
	alertEngine alerts.Engine
	rep         *reporter.Reporter
	jobQueue    chan models.TargetConfig
	wg          sync.WaitGroup
}

// Compile-time check that agentPool satisfies the scheduler's pool interface.
var _ ping.Service = (*agentPool)(nil)

func newAgentPool(maxWorkers int, eng engines.Engine, ae alerts.Engine, rep *reporter.Reporter) *agentPool {
	return &agentPool{
		maxWorkers:  maxWorkers,
		engine:      eng,
		alertEngine: ae,
		rep:         rep,
		jobQueue:    make(chan models.TargetConfig, maxWorkers*3),
	}
}

func (p *agentPool) ResetTarget(targetID string) {
	if p.alertEngine != nil {
		p.alertEngine.ResetTarget(targetID)
	}
}

func (p *agentPool) EnqueueJob(target models.TargetConfig) {
	select {
	case p.jobQueue <- target:
	default:
		log.Printf("WARN agentPool: queue full, dropping check for %s", target.IPAddress)
	}
}

func (p *agentPool) StartWorkers(ctx context.Context) {
	for i := 0; i < p.maxWorkers; i++ {
		p.wg.Add(1)
		go p.worker(ctx)
	}
	<-ctx.Done()
	p.wg.Wait()
}

func (p *agentPool) worker(ctx context.Context) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case target := <-p.jobQueue:
			if !target.IsActive {
				continue
			}

			result := p.engine.Check(ctx, target)

			pingRes := models.PingResult{
				TargetID:  result.TargetID,
				LatencyMs: result.LatencyMs,
				IsDown:    result.Status == "down",
				Timestamp: result.Timestamp,
			}

			if p.alertEngine != nil {
				p.alertEngine.ProcessResult(target, pingRes)
			}

			p.rep.Add(result)
		}
	}
}

// ─── service ─────────────────────────────────────────────────────────────────

const systemdUnitPath = "/etc/systemd/system/ipmonitor-agent.service"

func cmdService(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "service: expected install|uninstall|start|stop")
		os.Exit(1)
	}
	switch args[0] {
	case "install":
		cmdServiceInstall()
	case "uninstall":
		cmdServiceUninstall()
	case "start":
		runSystemctl("start", "ipmonitor-agent")
	case "stop":
		runSystemctl("stop", "ipmonitor-agent")
	default:
		fmt.Fprintf(os.Stderr, "service: unknown action %q — expected install|uninstall|start|stop\n", args[0])
		os.Exit(1)
	}
}

func cmdServiceInstall() {
	if runtime.GOOS == "windows" {
		log.Fatal("service install: Windows Service support is not yet available — use NSSM or Task Scheduler manually")
	}

	binaryPath, err := os.Executable()
	if err != nil {
		log.Fatalf("service install: cannot determine binary path: %v", err)
	}
	binaryPath, err = filepath.EvalSymlinks(binaryPath)
	if err != nil {
		log.Fatalf("service install: cannot resolve binary path: %v", err)
	}

	unit := fmt.Sprintf(`[Unit]
Description=IPMonitor Agent
Documentation=https://ipmonitor.yaurima.com
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s run
EnvironmentFile=-/etc/ipmonitor-agent/env
Restart=on-failure
RestartSec=10
StandardOutput=journal
StandardError=journal
SyslogIdentifier=ipmonitor-agent

[Install]
WantedBy=multi-user.target
`, binaryPath)

	if err := os.WriteFile(systemdUnitPath, []byte(unit), 0644); err != nil {
		log.Fatalf("service install: write %s: %v (try sudo)", systemdUnitPath, err)
	}
	fmt.Printf("Wrote %s\n", systemdUnitPath)

	runSystemctl("daemon-reload")
	runSystemctl("enable", "ipmonitor-agent")

	fmt.Println("Service installed and enabled.")
	fmt.Println("Start with: ipmonitor-agent service start")
}

func cmdServiceUninstall() {
	if runtime.GOOS == "windows" {
		log.Fatal("service uninstall: Windows Service support is not yet available")
	}

	runSystemctl("disable", "--now", "ipmonitor-agent")

	if err := os.Remove(systemdUnitPath); err != nil && !os.IsNotExist(err) {
		log.Printf("WARN: could not remove %s: %v", systemdUnitPath, err)
	} else {
		fmt.Printf("Removed %s\n", systemdUnitPath)
	}

	runSystemctl("daemon-reload")
	fmt.Println("Service uninstalled.")
}

func runSystemctl(args ...string) {
	cmd := exec.Command("systemctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("systemctl %v: %v", args, err)
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func envInt(key string, def int) int {
	if s := os.Getenv(key); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	return def
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
