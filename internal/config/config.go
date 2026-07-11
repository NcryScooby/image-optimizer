package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds runtime settings loaded from environment variables.
type Config struct {
	DatabaseURL       string
	RabbitMQURL       string
	ImgproxyURL       string
	S3Endpoint        string
	S3Region          string
	S3Bucket          string
	S3AccessKey       string
	S3SecretKey       string
	S3UsePathStyle    bool
	HTTPAddr          string
	MetricsAddr       string
	MaxUploadBytes    int64
	RetryAfterSeconds int
	DefaultQuality    int
}

const (
	defaultHTTPAddr          = ":8080"
	defaultMaxUploadBytes    = int64(10 << 20) // 10485760
	defaultRetryAfterSeconds = 2
	defaultDefaultQuality    = 80
)

// Load reads configuration from the environment.
// DATABASE_URL, RABBITMQ_URL, IMGPROXY_URL, and S3_* are required.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		RabbitMQURL:       os.Getenv("RABBITMQ_URL"),
		ImgproxyURL:       os.Getenv("IMGPROXY_URL"),
		S3Endpoint:        os.Getenv("S3_ENDPOINT"),
		S3Region:          os.Getenv("S3_REGION"),
		S3Bucket:          os.Getenv("S3_BUCKET"),
		S3AccessKey:       os.Getenv("S3_ACCESS_KEY"),
		S3SecretKey:       os.Getenv("S3_SECRET_KEY"),
		HTTPAddr:          getenv("HTTP_ADDR", defaultHTTPAddr),
		MetricsAddr:       os.Getenv("METRICS_ADDR"), // empty default = metrics off
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
	if cfg.S3Endpoint == "" {
		return Config{}, fmt.Errorf("S3_ENDPOINT is required")
	}
	if cfg.S3Region == "" {
		return Config{}, fmt.Errorf("S3_REGION is required")
	}
	if cfg.S3Bucket == "" {
		return Config{}, fmt.Errorf("S3_BUCKET is required")
	}
	if cfg.S3AccessKey == "" {
		return Config{}, fmt.Errorf("S3_ACCESS_KEY is required")
	}
	if cfg.S3SecretKey == "" {
		return Config{}, fmt.Errorf("S3_SECRET_KEY is required")
	}

	pathStyle, err := parseBoolEnv("S3_USE_PATH_STYLE")
	if err != nil {
		return Config{}, err
	}
	cfg.S3UsePathStyle = pathStyle

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

func parseBoolEnv(key string) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return false, fmt.Errorf("%s is required", key)
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return b, nil
}
