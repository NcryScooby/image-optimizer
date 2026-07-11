package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/notrealscooby/image-optimizer/internal/db"
	"github.com/notrealscooby/image-optimizer/internal/imgproxy"
	"github.com/notrealscooby/image-optimizer/internal/queue"
	"github.com/notrealscooby/image-optimizer/internal/storage"
	"github.com/notrealscooby/image-optimizer/internal/transform"
)

// maxAttempts is the total number of processing tries before MarkFailed.
// Spec: max 3 retries → GET returns 422.
const maxAttempts = 3

const defaultQuality = 80

// Deps are the collaborators Run needs. Wired by cmd/worker in Wave 3.
type Deps struct {
	DB       *db.Store
	Storage  *storage.Storage
	Imgproxy *imgproxy.Client
	Queue    *queue.Client
}

// Run consumes image.variants until ctx is cancelled.
// Success → ack. Transient failure with attempts < 3 → nack+requeue.
// After 3 failed attempts → MarkFailed and ack.
func Run(ctx context.Context, deps Deps) error {
	if deps.DB == nil || deps.Storage == nil || deps.Imgproxy == nil || deps.Queue == nil {
		return fmt.Errorf("worker: incomplete deps")
	}
	slog.Info("worker started")
	return deps.Queue.Consume(ctx, deps.process)
}

func (d Deps) process(ctx context.Context, variantID string) error {
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
		return fmt.Errorf("get variant: %w", err)
	}

	switch v.Status {
	case db.StatusReady, db.StatusFailed:
		slog.Info("worker: skip terminal status", "variant_id", variantID, "status", v.Status)
		return nil
	}

	attempts, err := d.DB.IncrAttempts(ctx, id)
	if err != nil {
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
				return fmt.Errorf("mark failed: %w", markErr)
			}
			return nil // ack — retries exhausted
		}
		return err // nack + requeue
	}

	slog.Info("worker: variant ready", "variant_id", variantID, "attempts", attempts)
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

	path := imgproxy.BuildPath(img.OriginalPath, params)
	data, err := d.Imgproxy.Fetch(ctx, path)
	if err != nil {
		return fmt.Errorf("imgproxy fetch: %w", err)
	}

	rel, err := d.Storage.WriteVariant(ctx, v.ImageID.String(), v.ParamsHash, data)
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
