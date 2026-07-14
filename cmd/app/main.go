package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/notrealscooby/image-optimizer/internal/config"
	"github.com/notrealscooby/image-optimizer/internal/db"
	apihttp "github.com/notrealscooby/image-optimizer/internal/http"
	"github.com/notrealscooby/image-optimizer/internal/imgproxy"
	_ "github.com/notrealscooby/image-optimizer/internal/metrics"
	"github.com/notrealscooby/image-optimizer/internal/queue"
	"github.com/notrealscooby/image-optimizer/internal/storage"
	"github.com/notrealscooby/image-optimizer/internal/worker"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if len(os.Args) < 2 {
		slog.Error("usage: app <serve|worker>")
		os.Exit(2)
	}

	switch os.Args[1] {
	case "serve":
		if err := runServe(); err != nil {
			slog.Error("serve failed", "err", err)
			os.Exit(1)
		}
	case "worker":
		if err := runWorker(); err != nil {
			slog.Error("worker failed", "err", err)
			os.Exit(1)
		}
	default:
		slog.Error("unknown command", "command", os.Args[1])
		os.Exit(2)
	}
}

func runServe() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	store := db.NewStore(pool)

	stor, err := storage.New(ctx, cfg)
	if err != nil {
		return err
	}

	q, err := queue.Connect(cfg.RabbitMQURL)
	if err != nil {
		return err
	}
	defer q.Close()

	img := imgproxy.New(cfg.ImgproxyURL)
	h := apihttp.NewHandler(store, stor, q, img, cfg)

	if cfg.MTLSEnabled {
		return serveDual(ctx, h, cfg)
	}
	return serveSingle(ctx, h, cfg)
}

func serveSingle(ctx context.Context, h *apihttp.Handler, cfg config.Config) error {
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           apihttp.NewRouter(h),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("http listening", "addr", cfg.HTTPAddr, "mtls", false)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	return waitShutdown(ctx, errCh, srv)
}

func serveDual(ctx context.Context, h *apihttp.Handler, cfg config.Config) error {
	tlsCfg, err := clientAuthTLSConfig(cfg)
	if err != nil {
		return err
	}

	publicSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           apihttp.NewPublicRouter(h),
		ReadHeaderTimeout: 10 * time.Second,
	}
	writeSrv := &http.Server{
		Addr:              cfg.WriteHTTPAddr,
		Handler:           apihttp.NewWriteRouter(h),
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		slog.Info("http public listening", "addr", cfg.HTTPAddr)
		if err := publicSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	go func() {
		slog.Info("https write listening", "addr", cfg.WriteHTTPAddr, "mtls", true)
		if err := writeSrv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down http servers")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = publicSrv.Shutdown(shutdownCtx)
		_ = writeSrv.Shutdown(shutdownCtx)
		var first error
		for i := 0; i < 2; i++ {
			if err := <-errCh; err != nil && first == nil {
				first = err
			}
		}
		return first
	case err := <-errCh:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = publicSrv.Shutdown(shutdownCtx)
		_ = writeSrv.Shutdown(shutdownCtx)
		return err
	}
}

func waitShutdown(ctx context.Context, errCh <-chan error, servers ...*http.Server) error {
	select {
	case <-ctx.Done():
		slog.Info("shutting down http server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, srv := range servers {
			if err := srv.Shutdown(shutdownCtx); err != nil {
				return err
			}
		}
		return <-errCh
	case err := <-errCh:
		return err
	}
}

func clientAuthTLSConfig(cfg config.Config) (*tls.Config, error) {
	caPEM, err := os.ReadFile(cfg.TLSClientCAFile)
	if err != nil {
		return nil, fmt.Errorf("read TLS_CLIENT_CA_FILE: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("TLS_CLIENT_CA_FILE: no certificates found")
	}
	return &tls.Config{
		ClientCAs:  pool,
		ClientAuth: tls.RequireAndVerifyClientCert,
		MinVersion: tls.VersionTLS12,
	}, nil
}

func runWorker() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	store := db.NewStore(pool)

	stor, err := storage.New(ctx, cfg)
	if err != nil {
		return err
	}

	q, err := queue.Connect(cfg.RabbitMQURL)
	if err != nil {
		return err
	}
	defer q.Close()

	img := imgproxy.New(cfg.ImgproxyURL)

	var metricsSrv *http.Server
	if cfg.MetricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		})
		mux.HandleFunc("/ready", apihttp.ReadyHandler(store, q))
		metricsSrv = &http.Server{
			Addr:              cfg.MetricsAddr,
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			slog.Info("metrics listening", "addr", cfg.MetricsAddr)
			if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("metrics server failed", "err", err)
			}
		}()
	}

	err = worker.Run(ctx, worker.Deps{
		DB:       store,
		Storage:  stor,
		Imgproxy: img,
		Queue:    q,
		S3Bucket: cfg.S3Bucket,
	})

	if metricsSrv != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutErr := metricsSrv.Shutdown(shutdownCtx); shutErr != nil {
			slog.Error("metrics server shutdown", "err", shutErr)
		}
	}

	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	slog.Info("worker stopped")
	return nil
}
