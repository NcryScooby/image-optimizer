package config

import (
	"os"
	"testing"
	"time"
)

func TestLoad_MTLSDisabledByDefault(t *testing.T) {
	clearAppEnv(t)
	setRequiredEnv(t)
	t.Setenv("MTLS_ENABLED", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MTLSEnabled {
		t.Fatal("MTLSEnabled want false")
	}
	if cfg.SyncTransformTimeout != 25*time.Second {
		t.Fatalf("SyncTransformTimeout = %v, want 25s", cfg.SyncTransformTimeout)
	}
}

func TestLoad_MTLSRequiresCertsAndWriteAddr(t *testing.T) {
	clearAppEnv(t)
	setRequiredEnv(t)
	t.Setenv("MTLS_ENABLED", "true")

	_, err := Load()
	if err == nil {
		t.Fatal("Load err = nil, want TLS_CERT_FILE error")
	}

	t.Setenv("TLS_CERT_FILE", "/certs/server.crt")
	t.Setenv("TLS_KEY_FILE", "/certs/server.key")
	t.Setenv("TLS_CLIENT_CA_FILE", "/certs/ca.crt")
	_, err = Load()
	if err == nil {
		t.Fatal("Load err = nil, want WRITE_HTTP_ADDR error")
	}

	t.Setenv("WRITE_HTTP_ADDR", ":8443")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.MTLSEnabled {
		t.Fatal("MTLSEnabled want true")
	}
	if cfg.WriteHTTPAddr != ":8443" {
		t.Fatalf("WriteHTTPAddr = %q", cfg.WriteHTTPAddr)
	}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost/db")
	t.Setenv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/")
	t.Setenv("IMGPROXY_URL", "http://imgproxy:8080")
	t.Setenv("S3_ENDPOINT", "http://minio:9000")
	t.Setenv("S3_REGION", "us-east-1")
	t.Setenv("S3_BUCKET", "images")
	t.Setenv("S3_ACCESS_KEY", "minio")
	t.Setenv("S3_SECRET_KEY", "minio")
	t.Setenv("S3_USE_PATH_STYLE", "true")
}

func clearAppEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"DATABASE_URL", "RABBITMQ_URL", "IMGPROXY_URL",
		"S3_ENDPOINT", "S3_REGION", "S3_BUCKET", "S3_ACCESS_KEY", "S3_SECRET_KEY", "S3_USE_PATH_STYLE",
		"HTTP_ADDR", "WRITE_HTTP_ADDR", "METRICS_ADDR",
		"MAX_UPLOAD_BYTES", "RETRY_AFTER_SECONDS", "DEFAULT_QUALITY", "SYNC_TRANSFORM_TIMEOUT",
		"MTLS_ENABLED", "TLS_CERT_FILE", "TLS_KEY_FILE", "TLS_CLIENT_CA_FILE",
	}
	for _, k := range keys {
		_ = os.Unsetenv(k)
	}
}
