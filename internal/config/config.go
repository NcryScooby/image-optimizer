package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds runtime settings loaded from environment variables.
type Config struct {
	DatabaseURL       string
	RabbitMQURL       string
	ImgproxyURL       string
	DataDir           string
	HTTPAddr          string
	MaxUploadBytes    int64
	RetryAfterSeconds int
	DefaultQuality    int
}

const (
	defaultDataDir           = "/data"
	defaultHTTPAddr          = ":8080"
	defaultMaxUploadBytes    = int64(10 << 20) // 10485760
	defaultRetryAfterSeconds = 2
	defaultDefaultQuality    = 80
)

// Load reads configuration from the environment.
// DATABASE_URL, RABBITMQ_URL, and IMGPROXY_URL are required.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		RabbitMQURL:       os.Getenv("RABBITMQ_URL"),
		ImgproxyURL:       os.Getenv("IMGPROXY_URL"),
		DataDir:           getenv("DATA_DIR", defaultDataDir),
		HTTPAddr:          getenv("HTTP_ADDR", defaultHTTPAddr),
		MaxUploadBytes:    defaultMaxUploadBytes,
		RetryAfterSeconds: defaultRetryAfterSeconds,
		DefaultQuality:    defaultDefaultQuality,
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.RabbitMQURL == "" {
		return Config{}, fmt.Errorf("RABBITMQ_URL is required")
	}
	if cfg.ImgproxyURL == "" {
		return Config{}, fmt.Errorf("IMGPROXY_URL is required")
	}

	if v := os.Getenv("MAX_UPLOAD_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("MAX_UPLOAD_BYTES: %w", err)
		}
		if n <= 0 {
			return Config{}, fmt.Errorf("MAX_UPLOAD_BYTES must be > 0")
		}
		cfg.MaxUploadBytes = n
	}

	if v := os.Getenv("RETRY_AFTER_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("RETRY_AFTER_SECONDS: %w", err)
		}
		if n < 0 {
			return Config{}, fmt.Errorf("RETRY_AFTER_SECONDS must be >= 0")
		}
		cfg.RetryAfterSeconds = n
	}

	if v := os.Getenv("DEFAULT_QUALITY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("DEFAULT_QUALITY: %w", err)
		}
		if n < 1 || n > 100 {
			return Config{}, fmt.Errorf("DEFAULT_QUALITY must be between 1 and 100")
		}
		cfg.DefaultQuality = n
	}

	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
