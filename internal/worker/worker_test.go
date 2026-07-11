package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/notrealscooby/image-optimizer/internal/db"
	"github.com/notrealscooby/image-optimizer/internal/metrics"
	"github.com/notrealscooby/image-optimizer/internal/queue"
	"github.com/notrealscooby/image-optimizer/internal/transform"
)

type mockStore struct {
	variant      db.Variant
	variantErr   error
	image        db.Image
	imageErr     error
	attempts     int
	incrErr      error
	markFailErr  error
	markReadyErr error
	failedMarked bool
	readyMarked  bool
}

func (m *mockStore) GetVariantByID(ctx context.Context, id uuid.UUID) (db.Variant, error) {
	if m.variantErr != nil {
		return db.Variant{}, m.variantErr
	}
	return m.variant, nil
}

func (m *mockStore) GetImage(ctx context.Context, id uuid.UUID) (db.Image, error) {
	if m.imageErr != nil {
		return db.Image{}, m.imageErr
	}
	return m.image, nil
}

func (m *mockStore) IncrAttempts(ctx context.Context, id uuid.UUID) (int, error) {
	if m.incrErr != nil {
		return 0, m.incrErr
	}
	return m.attempts, nil
}

func (m *mockStore) MarkFailed(ctx context.Context, id uuid.UUID, lastError string) error {
	if m.markFailErr != nil {
		return m.markFailErr
	}
	m.failedMarked = true
	return nil
}

func (m *mockStore) MarkReady(ctx context.Context, id uuid.UUID, path string) error {
	if m.markReadyErr != nil {
		return m.markReadyErr
	}
	m.readyMarked = true
	return nil
}

type mockImgproxy struct {
	data  []byte
	err   error
	calls int
}

func (m *mockImgproxy) Fetch(ctx context.Context, path string) ([]byte, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.data, nil
}

type mockStorage struct {
	rel   string
	err   error
	calls int
}

func (m *mockStorage) WriteVariant(ctx context.Context, imageID, paramsHash string, data []byte) (string, error) {
	m.calls++
	if m.err != nil {
		return "", m.err
	}
	if m.rel != "" {
		return m.rel, nil
	}
	return "variants/" + imageID + "/" + paramsHash + ".avif", nil
}

type mockQueue struct {
	depth    int
	depthErr error
}

func (m *mockQueue) Consume(ctx context.Context, handler queue.Handler) error {
	<-ctx.Done()
	return ctx.Err()
}

func (m *mockQueue) QueueInspect() (int, error) {
	if m.depthErr != nil {
		return 0, m.depthErr
	}
	return m.depth, nil
}

func pendingVariant() db.Variant {
	id := uuid.New()
	imageID := uuid.New()
	paramsJSON := transform.CacheKeyJSON(transform.Params{})
	return db.Variant{
		ID:         id,
		ImageID:    imageID,
		ParamsHash: "abc123def456abc123def456abc123de",
		ParamsJSON: paramsJSON,
		Status:     db.StatusPending,
	}
}

func counterValue(t *testing.T, result string) float64 {
	t.Helper()
	return testutil.ToFloat64(metrics.JobsProcessedTotal.WithLabelValues(result))
}

func histogramSampleCount(t *testing.T, obs prometheus.Observer) uint64 {
	t.Helper()
	h, ok := obs.(prometheus.Histogram)
	if !ok {
		t.Fatalf("observer is not a Histogram")
	}
	var m dto.Metric
	if err := h.Write(&m); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	return m.GetHistogram().GetSampleCount()
}

func TestProcess_Success_RecordsMetrics(t *testing.T) {
	v := pendingVariant()
	store := &mockStore{
		variant:  v,
		image:    db.Image{ID: v.ImageID, OriginalPath: "originals/" + v.ImageID.String() + ".jpg"},
		attempts: 1,
	}
	img := &mockImgproxy{data: []byte("avif-bytes")}
	stor := &mockStorage{}

	beforeSuccess := counterValue(t, metrics.ResultSuccess)
	beforeFailed := counterValue(t, metrics.ResultFailed)
	beforeRequeued := counterValue(t, metrics.ResultRequeued)
	beforeJobHist := histogramSampleCount(t, metrics.WorkerJobDurationSeconds.WithLabelValues(metrics.ResultSuccess))
	beforeFetch := histogramSampleCount(t, metrics.WorkerImgproxyFetchDurationSeconds)
	beforeWrite := histogramSampleCount(t, metrics.WorkerDiskWriteDurationSeconds)

	deps := Deps{DB: store, Storage: stor, Imgproxy: img, Queue: &mockQueue{}, S3Bucket: "images"}
	err := deps.process(context.Background(), v.ID.String())
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if !store.readyMarked {
		t.Fatal("expected MarkReady")
	}
	if img.calls != 1 || stor.calls != 1 {
		t.Fatalf("fetch/write calls: img=%d storage=%d", img.calls, stor.calls)
	}

	if got := counterValue(t, metrics.ResultSuccess) - beforeSuccess; got != 1 {
		t.Fatalf("success counter delta: got %v want 1", got)
	}
	if got := counterValue(t, metrics.ResultFailed) - beforeFailed; got != 0 {
		t.Fatalf("failed counter delta: got %v want 0", got)
	}
	if got := counterValue(t, metrics.ResultRequeued) - beforeRequeued; got != 0 {
		t.Fatalf("requeued counter delta: got %v want 0", got)
	}
	if got := histogramSampleCount(t, metrics.WorkerJobDurationSeconds.WithLabelValues(metrics.ResultSuccess)) - beforeJobHist; got != 1 {
		t.Fatalf("job duration samples: got %d want 1", got)
	}
	if got := histogramSampleCount(t, metrics.WorkerImgproxyFetchDurationSeconds) - beforeFetch; got != 1 {
		t.Fatalf("imgproxy fetch samples: got %d want 1", got)
	}
	if got := histogramSampleCount(t, metrics.WorkerDiskWriteDurationSeconds) - beforeWrite; got != 1 {
		t.Fatalf("disk write samples: got %d want 1", got)
	}
}

func TestProcess_Failed_AfterMaxAttempts(t *testing.T) {
	v := pendingVariant()
	store := &mockStore{
		variant:  v,
		image:    db.Image{ID: v.ImageID, OriginalPath: "originals/x.jpg"},
		attempts: maxAttempts,
	}
	img := &mockImgproxy{err: errors.New("imgproxy down")}

	var waitCalls int
	orig := waitBackoffFn
	waitBackoffFn = func(ctx context.Context, attempts int) error {
		waitCalls++
		return nil
	}
	t.Cleanup(func() { waitBackoffFn = orig })

	beforeFailed := counterValue(t, metrics.ResultFailed)
	beforeSuccess := counterValue(t, metrics.ResultSuccess)
	beforeRequeued := counterValue(t, metrics.ResultRequeued)
	beforeJobHist := histogramSampleCount(t, metrics.WorkerJobDurationSeconds.WithLabelValues(metrics.ResultFailed))

	deps := Deps{DB: store, Storage: &mockStorage{}, Imgproxy: img, Queue: &mockQueue{}, S3Bucket: "images"}
	err := deps.process(context.Background(), v.ID.String())
	if err != nil {
		t.Fatalf("process: expected nil (ack), got %v", err)
	}
	if !store.failedMarked {
		t.Fatal("expected MarkFailed")
	}
	if waitCalls != 0 {
		t.Fatalf("MarkFailed path must not wait backoff: calls=%d", waitCalls)
	}

	if got := counterValue(t, metrics.ResultFailed) - beforeFailed; got != 1 {
		t.Fatalf("failed counter delta: got %v want 1", got)
	}
	if got := counterValue(t, metrics.ResultSuccess) - beforeSuccess; got != 0 {
		t.Fatalf("success counter delta: got %v want 0", got)
	}
	if got := counterValue(t, metrics.ResultRequeued) - beforeRequeued; got != 0 {
		t.Fatalf("requeued counter delta: got %v want 0", got)
	}
	if got := histogramSampleCount(t, metrics.WorkerJobDurationSeconds.WithLabelValues(metrics.ResultFailed)) - beforeJobHist; got != 1 {
		t.Fatalf("job duration failed samples: got %d want 1", got)
	}
}

func TestProcess_Requeued_OnTransientFailure(t *testing.T) {
	v := pendingVariant()
	store := &mockStore{
		variant:  v,
		image:    db.Image{ID: v.ImageID, OriginalPath: "originals/x.jpg"},
		attempts: 1,
	}
	img := &mockImgproxy{err: errors.New("transient")}

	var waitCalls, waitAttempts int
	orig := waitBackoffFn
	waitBackoffFn = func(ctx context.Context, attempts int) error {
		waitCalls++
		waitAttempts = attempts
		return nil // no-op: avoid 1s sleep in CI
	}
	t.Cleanup(func() { waitBackoffFn = orig })

	beforeRequeued := counterValue(t, metrics.ResultRequeued)
	beforeFailed := counterValue(t, metrics.ResultFailed)
	beforeSuccess := counterValue(t, metrics.ResultSuccess)
	beforeJobHist := histogramSampleCount(t, metrics.WorkerJobDurationSeconds.WithLabelValues(metrics.ResultRequeued))

	deps := Deps{DB: store, Storage: &mockStorage{}, Imgproxy: img, Queue: &mockQueue{}, S3Bucket: "images"}
	err := deps.process(context.Background(), v.ID.String())
	if err == nil {
		t.Fatal("expected error for nack+requeue")
	}
	if store.failedMarked {
		t.Fatal("should not MarkFailed before max attempts")
	}
	if waitCalls != 1 {
		t.Fatalf("requeue path must wait backoff once: calls=%d", waitCalls)
	}
	if waitAttempts != 1 {
		t.Fatalf("waitBackoff attempts: got %d want 1", waitAttempts)
	}

	if got := counterValue(t, metrics.ResultRequeued) - beforeRequeued; got != 1 {
		t.Fatalf("requeued counter delta: got %v want 1", got)
	}
	if got := counterValue(t, metrics.ResultFailed) - beforeFailed; got != 0 {
		t.Fatalf("failed counter delta: got %v want 0", got)
	}
	if got := counterValue(t, metrics.ResultSuccess) - beforeSuccess; got != 0 {
		t.Fatalf("success counter delta: got %v want 0", got)
	}
	if got := histogramSampleCount(t, metrics.WorkerJobDurationSeconds.WithLabelValues(metrics.ResultRequeued)) - beforeJobHist; got != 1 {
		t.Fatalf("job duration requeued samples: got %d want 1", got)
	}
}

func TestBackoffDuration(t *testing.T) {
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{1, time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
	}
	for _, tc := range cases {
		if got := backoffDuration(tc.attempts); got != tc.want {
			t.Errorf("backoffDuration(%d): got %v want %v", tc.attempts, got, tc.want)
		}
	}
}

func TestWaitBackoff_Cancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := waitBackoff(ctx, 1)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitBackoff: got %v want context.Canceled", err)
	}
	if elapsed >= time.Second {
		t.Fatalf("cancelled wait should return early, took %v", elapsed)
	}
}

func TestProcess_SkipTerminal_NoMetrics(t *testing.T) {
	v := pendingVariant()
	v.Status = db.StatusReady
	store := &mockStore{variant: v}

	beforeSuccess := counterValue(t, metrics.ResultSuccess)
	beforeFailed := counterValue(t, metrics.ResultFailed)
	beforeRequeued := counterValue(t, metrics.ResultRequeued)

	deps := Deps{DB: store, Storage: &mockStorage{}, Imgproxy: &mockImgproxy{}, Queue: &mockQueue{}, S3Bucket: "images"}
	err := deps.process(context.Background(), v.ID.String())
	if err != nil {
		t.Fatalf("process: %v", err)
	}

	if counterValue(t, metrics.ResultSuccess)-beforeSuccess != 0 ||
		counterValue(t, metrics.ResultFailed)-beforeFailed != 0 ||
		counterValue(t, metrics.ResultRequeued)-beforeRequeued != 0 {
		t.Fatal("terminal skip should not record job metrics")
	}
}

func TestUpdateQueueDepth(t *testing.T) {
	q := &mockQueue{depth: 7}
	deps := Deps{Queue: q}
	before := testutil.ToFloat64(metrics.QueueDepth)
	deps.updateQueueDepth()
	got := testutil.ToFloat64(metrics.QueueDepth)
	if got != 7 {
		t.Fatalf("queue_depth: got %v want 7 (before was %v)", got, before)
	}
}
