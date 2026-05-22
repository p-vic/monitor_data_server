package config

import (
	"errors"
	"strings"

	"github.com/spf13/viper"
)

// Config centraliza las variables de entorno inyectables para el Clean Architecture.
type Config struct {
	WorkerID        string
	ControlPlaneURL string
	HMACSecret      string
	InfluxURL       string
	InfluxToken     string
	InfluxOrg       string
	InfluxBucket    string

	// SMTP Notifications Config
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string

	// Telegram Notifications Config (optional)
	TelegramBotToken string
}

// Load lee el entorno utilizando Viper y construye el objeto de configuración.
func Load() (*Config, error) {
	v := viper.New()

	// 1. Configurar lectura automática de variables de entorno
	v.AutomaticEnv()
	// Permite leer variables como INFLUX_URL a través de InfluxURL struct fields u origin
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// 2. Establecimiento Seguro de Defaults (Auditoría)
	// REGLA FUNDAMENTAL: Jamás establecer HMAC_SECRET o URLs de APIs externas vacías como default válido.
	// Fallar rápido (Fail Fast) es más seguro que correr autenticando todo contra "".

	// WorkerID puede auto-resolverse como hostname si no se provee.
	v.SetDefault("WORKER_ID", "default-worker-01")

	var cfg Config

	// 3. Extracción de variables requeridas sin default
	controlPlaneURL := v.GetString("CONTROL_PLANE_URL")
	if controlPlaneURL == "" {
		return nil, errors.New("CONTROL_PLANE_URL environment variable is strict required")
	}
	cfg.ControlPlaneURL = controlPlaneURL

	hmacSecret := v.GetString("HMAC_SECRET")
	if hmacSecret == "" {
		return nil, errors.New("HMAC_SECRET environment variable is strict required")
	}
	if len(hmacSecret) < 32 {
		return nil, errors.New("HMAC_SECRET must be at least 32 characters long to prevent brute-force attacks")
	}
	cfg.HMACSecret = hmacSecret

	influxURL := v.GetString("INFLUX_URL")
	if influxURL == "" {
		return nil, errors.New("INFLUX_URL environment variable is strict required")
	}
	cfg.InfluxURL = influxURL

	cfg.WorkerID = v.GetString("WORKER_ID")

	cfg.InfluxToken = v.GetString("INFLUX_TOKEN")
	if cfg.InfluxToken == "" {
		return nil, errors.New("INFLUX_TOKEN environment variable is strict required")
	}

	cfg.InfluxOrg = v.GetString("INFLUX_ORG")
	if cfg.InfluxOrg == "" {
		return nil, errors.New("INFLUX_ORG environment variable is strict required")
	}

	cfg.InfluxBucket = v.GetString("INFLUX_BUCKET")
	if cfg.InfluxBucket == "" {
		cfg.InfluxBucket = "monitorings" // fallback default
	}

	cfg.SMTPHost = v.GetString("SMTP_HOST")
	cfg.SMTPPort = v.GetInt("SMTP_PORT")
	cfg.SMTPUsername = v.GetString("SMTP_USERNAME")
	cfg.SMTPPassword = v.GetString("SMTP_PASSWORD")
	cfg.SMTPFrom = v.GetString("SMTP_FROM")
	if cfg.SMTPFrom == "" {
		cfg.SMTPFrom = "alerts@monitoring.local"
	}

	cfg.TelegramBotToken = v.GetString("TELEGRAM_BOT_TOKEN")

	return &cfg, nil
}
