package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/notrealscooby/image-optimizer/internal/db"
	"github.com/notrealscooby/image-optimizer/internal/metrics"
	"github.com/notrealscooby/image-optimizer/internal/queue"
	"github.com/notrealscooby/image-optimizer/internal/variantgen"
)

const maxAttempts = 3

const queueDepthInterval = 10 * time.Second

var waitBackoffFn = waitBackoff

type variantStore interface {
	GetVariantByID(ctx context.Context, id uuid.UUID) (db.Variant, error)
	GetImage(ctx context.Context, id uuid.UUID) (db.Image, error)
	IncrAttempts(ctx context.Context, id uuid.UUID) (int, error)
	MarkFailed(ctx context.Context, id uuid.UUID, lastError string) error
	MarkReady(ctx context.Context, id uuid.UUID, path string) error
}

type imgproxyFetcher interface {
	Fetch(ctx context.Context, path string) ([]byte, error)
}

type variantWriter interface {
	WriteVariant(ctx context.Context, imageID, paramsHash string, data []byte) (string, error)
}

type jobQueue interface {
	Consume(ctx context.Context, handler queue.Handler) error
	QueueInspect() (int, error)
}

type Deps struct {
	DB       variantStore
	Storage  variantWriter
	Imgproxy imgproxyFetcher
	Queue    jobQueue
	S3Bucket string
}

func Run(ctx context.Context, deps Deps) error {
	if deps.DB == nil || deps.Storage == nil || deps.Imgproxy == nil || deps.Queue == nil {
		return fmt.Errorf("worker: incomplete deps")
	}
	slog.Info("worker started")
	go deps.pollQueueDepth(ctx)
	return deps.Queue.Consume(ctx, deps.process)
}

func (d Deps) pollQueueDepth(ctx context.Context) {
	d.updateQueueDepth()
	ticker := time.NewTicker(queueDepthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.updateQueueDepth()
		}
	}
}

func (d Deps) updateQueueDepth() {
	n, err := d.Queue.QueueInspect()
	if err != nil {
		slog.Warn("worker: queue inspect failed", "err", err)
		return
	}
	metrics.QueueDepth.Set(float64(n))
}

func (d Deps) process(ctx context.Context, variantID string) (err error) {
	start := time.Now()
	var result string
	defer func() {
		if result == "" {
			return
		}
		metrics.JobsProcessedTotal.WithLabelValues(result).Inc()
		metrics.WorkerJobDurationSeconds.WithLabelValues(result).Observe(time.Since(start).Seconds())
	}()

	id, err := uuid.Parse(variantID)
	if err != nil {
		slog.Error("worker: invalid variant_id", "variant_id", variantID, "err", err)
		return nil
	}

	v, err := d.DB.GetVariantByID(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			slog.Warn("worker: variant not found", "variant_id", variantID)
			return nil
		}
		result = metrics.ResultRequeued
		return fmt.Errorf("get variant: %w", err)
	}

	switch v.Status {
	case db.StatusReady, db.StatusFailed:
		slog.Info("worker: skip terminal status", "variant_id", variantID, "status", v.Status)
		return nil
	}

	attempts, err := d.DB.IncrAttempts(ctx, id)
	if err != nil {
		result = metrics.ResultRequeued
		return fmt.Errorf("incr attempts: %w", err)
	}

	gen := variantgen.Deps{
		DB:       d.DB,
		Storage:  d.Storage,
		Imgproxy: d.Imgproxy,
		S3Bucket: d.S3Bucket,
	}
	if err := gen.TransformAndPersist(ctx, v); err != nil {
		slog.Error("worker: process failed",
			"variant_id", variantID,
			"attempts", attempts,
			"err", err,
		)
		if attempts >= maxAttempts {
			if markErr := d.DB.MarkFailed(ctx, id, truncateErr(err, 500)); markErr != nil {
				result = metrics.ResultRequeued
				return fmt.Errorf("mark failed: %w", markErr)
			}
			result = metrics.ResultFailed
			return nil
		}
		result = metrics.ResultRequeued
		if waitErr := waitBackoffFn(ctx, attempts); waitErr != nil {
			return waitErr
		}
		return err
	}

	slog.Info("worker: variant ready", "variant_id", variantID, "attempts", attempts)
	result = metrics.ResultSuccess
	return nil
}

func truncateErr(err error, max int) string {
	s := err.Error()
	if len(s) > max {
		return s[:max]
	}
	return s
}
