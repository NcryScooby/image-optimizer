package variantgen

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/notrealscooby/image-optimizer/internal/db"
	"github.com/notrealscooby/image-optimizer/internal/imgproxy"
	"github.com/notrealscooby/image-optimizer/internal/metrics"
	"github.com/notrealscooby/image-optimizer/internal/transform"
)

const defaultQuality = 80

type imageReader interface {
	GetImage(ctx context.Context, id uuid.UUID) (db.Image, error)
	MarkReady(ctx context.Context, id uuid.UUID, path string) error
}

type imgproxyFetcher interface {
	Fetch(ctx context.Context, path string) ([]byte, error)
}

type variantWriter interface {
	WriteVariant(ctx context.Context, imageID, paramsHash string, data []byte) (string, error)
}

type Deps struct {
	DB       imageReader
	Storage  variantWriter
	Imgproxy imgproxyFetcher
	S3Bucket string
}

func (d Deps) TransformAndPersist(ctx context.Context, v db.Variant) error {
	if d.DB == nil || d.Storage == nil || d.Imgproxy == nil {
		return fmt.Errorf("variantgen: incomplete deps")
	}

	img, err := d.DB.GetImage(ctx, v.ImageID)
	if err != nil {
		return fmt.Errorf("get image: %w", err)
	}

	params, err := ParamsFromJSON(v.ParamsJSON)
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

func ParamsFromJSON(raw []byte) (transform.Params, error) {
	var p transform.Params
	if err := json.Unmarshal(raw, &p); err != nil {
		return transform.Params{}, fmt.Errorf("unmarshal params: %w", err)
	}
	if p.Q == 0 {
		p.Q = defaultQuality
	}
	return p, nil
}
