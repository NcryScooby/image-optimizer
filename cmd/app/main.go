package main

import (
	"context"
	"errors"
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
	"github.com/notrealscooby/image-optimizer/internal/queue"
	"github.com/notrealscooby/image-optimizer/internal/storage"
	"github.com/notrealscooby/image-optimizer/internal/worker"
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

	stor, err := storage.New(cfg.DataDir)
	if err != nil {
		return err
	}

	q, err := queue.Connect(cfg.RabbitMQURL)
	if err != nil {
		return err
	}
	defer q.Close()

	h := apihttp.NewHandler(store, stor, q, cfg)
	router := apihttp.NewRouter(h)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("http listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down http server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-errCh
	case err := <-errCh:
		return err
	}
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

	stor, err := storage.New(cfg.DataDir)
	if err != nil {
		return err
	}

	q, err := queue.Connect(cfg.RabbitMQURL)
	if err != nil {
		return err
	}
	defer q.Close()

	img := imgproxy.New(cfg.ImgproxyURL)

	err = worker.Run(ctx, worker.Deps{
		DB:       store,
		Storage:  stor,
		Imgproxy: img,
		Queue:    q,
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	slog.Info("worker stopped")
	return nil
}
