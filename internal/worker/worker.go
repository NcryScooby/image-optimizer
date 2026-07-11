package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/notrealscooby/image-optimizer/internal/db"
	"github.com/notrealscooby/image-optimizer/internal/imgproxy"
	"github.com/notrealscooby/image-optimizer/internal/metrics"
	"github.com/notrealscooby/image-optimizer/internal/queue"
	"github.com/notrealscooby/image-optimizer/internal/transform"
)

// maxAttempts is the total number of processing tries before MarkFailed.
// Spec: max 3 retries → GET returns 422.
const maxAttempts = 3

const defaultQuality = 80

const queueDepthInterval = 10 * time.Second

// waitBackoffFn is the requeue delay hook; overridden in tests.
var waitBackoffFn = waitBackoff

// variantStore is the DB surface the worker needs.
type variantStore interface {
	GetVariantByID(ctx context.Context, id uuid.UUID) (db.Variant, error)
	GetImage(ctx context.Context, id uuid.UUID) (db.Image, error)
	IncrAttempts(ctx context.Context, id uuid.UUID) (int, error)
	MarkFailed(ctx context.Context, id uuid.UUID, lastError string) error
	MarkReady(ctx context.Context, id uuid.UUID, path string) error
}

// imgproxyFetcher fetches transformed image bytes.
type imgproxyFetcher interface {
	Fetch(ctx context.Context, path string) ([]byte, error)
}

// variantWriter persists AVIF variant bytes to disk.
type variantWriter interface {
	WriteVariant(ctx context.Context, imageID, paramsHash string, data []byte) (string, error)
}

// jobQueue consumes jobs and reports queue depth.
type jobQueue interface {
	Consume(ctx context.Context, handler queue.Handler) error
	QueueInspect() (int, error)
}

// Deps are the collaborators Run needs. Wired by cmd/app.
type Deps struct {
	DB       variantStore
	Storage  variantWriter
	Imgproxy imgproxyFetcher
	Queue    jobQueue
	S3Bucket string
}

// Run consumes image.variants until ctx is cancelled.
// Success → ack. Transient failure with attempts < 3 → nack+requeue.
// After 3 failed attempts → MarkFailed and ack.
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
		return nil // ack poison
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

	if err := d.transformAndPersist(ctx, v); err != nil {
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
			return nil // ack — retries exhausted
		}
		result = metrics.ResultRequeued
		if waitErr := waitBackoffFn(ctx, attempts); waitErr != nil {
			return waitErr // shutdown: still nack+requeue
		}
		return err // nack + requeue
	}

	slog.Info("worker: variant ready", "variant_id", variantID, "attempts", attempts)
	result = metrics.ResultSuccess
	return nil
}

func (d Deps) transformAndPersist(ctx context.Context, v db.Variant) error {
	img, err := d.DB.GetImage(ctx, v.ImageID)
	if err != nil {
		return fmt.Errorf("get image: %w", err)
	}

	params, err := paramsFromJSON(v.ParamsJSON)
	if err != nil {
		return err
	}

	path := imgproxy.BuildPath(d.S3Bucket, img.OriginalPath, params)

	fetchStart := time.Now()
	data, err := d.Imgproxy.Fetch(ctx, path)
	metrics.WorkerImgproxyFetchDurationSeconds.Observe(time.Since(fetchStart).Seconds())
	if err != nil {
		return fmt.Errorf("imgproxy fetch: %w", err)
	}

	writeStart := time.Now()
	rel, err := d.Storage.WriteVariant(ctx, v.ImageID.String(), v.ParamsHash, data)
	metrics.WorkerDiskWriteDurationSeconds.Observe(time.Since(writeStart).Seconds())
	if err != nil {
		return fmt.Errorf("write variant: %w", err)
	}

	if err := d.DB.MarkReady(ctx, v.ID, rel); err != nil {
		return fmt.Errorf("mark ready: %w", err)
	}
	return nil
}

func paramsFromJSON(raw []byte) (transform.Params, error) {
	var p transform.Params
	if err := json.Unmarshal(raw, &p); err != nil {
		return transform.Params{}, fmt.Errorf("unmarshal params: %w", err)
	}
	// Safety if row was stored without Normalize/CacheKeyJSON defaults.
	if p.Q == 0 {
		p.Q = defaultQuality
	}
	return p, nil
}

func truncateErr(err error, max int) string {
	s := err.Error()
	if len(s) > max {
		return s[:max]
	}
	return s
}
