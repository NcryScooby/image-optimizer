package http

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/notrealscooby/image-optimizer/internal/config"
	"github.com/notrealscooby/image-optimizer/internal/db"
	"github.com/notrealscooby/image-optimizer/internal/metrics"
)

type mockStore struct {
	image          db.Image
	imageErr       error
	variant        db.Variant
	variantErr     error
	upsertVariant  db.Variant
	upsertCreated  bool
	upsertErr      error
	deletePendingN int
	pingErr        error
}

func (m *mockStore) InsertImage(context.Context, uuid.UUID, string, string, int64) (db.Image, error) {
	return db.Image{}, errors.New("not implemented")
}
func (m *mockStore) GetImage(context.Context, uuid.UUID) (db.Image, error) {
	if m.imageErr != nil {
		return db.Image{}, m.imageErr
	}
	return m.image, nil
}
func (m *mockStore) DeleteImage(context.Context, uuid.UUID) error {
	return errors.New("not implemented")
}
func (m *mockStore) GetVariantByHash(context.Context, uuid.UUID, string) (db.Variant, error) {
	if m.variantErr != nil {
		return db.Variant{}, m.variantErr
	}
	return m.variant, nil
}
func (m *mockStore) UpsertPendingVariant(context.Context, uuid.UUID, string, []byte) (db.Variant, bool, error) {
	if m.upsertErr != nil {
		return db.Variant{}, false, m.upsertErr
	}
	return m.upsertVariant, m.upsertCreated, nil
}
func (m *mockStore) DeletePendingVariant(context.Context, uuid.UUID) error {
	m.deletePendingN++
	return nil
}

func (m *mockStore) Ping(context.Context) error { return m.pingErr }

type mockQueue struct {
	err       error
	published []string
	pingErr   error
}

func (m *mockQueue) Publish(_ context.Context, variantID string) error {
	if m.err != nil {
		return m.err
	}
	m.published = append(m.published, variantID)
	return nil
}

func (m *mockQueue) Ping(context.Context) error { return m.pingErr }

type mockBlob struct {
	getPath  string
	getCalls int
}

func (m *mockBlob) SaveOriginal(context.Context, string, string, []byte) (string, error) {
	return "", errors.New("not implemented")
}
func (m *mockBlob) DeleteImageFiles(context.Context, string, string) error {
	return errors.New("not implemented")
}
func (m *mockBlob) Get(_ context.Context, _ string) (io.ReadCloser, int64, error) {
	m.getCalls++
	if m.getPath == "" {
		return nil, 0, errors.New("no file")
	}
	f, err := os.Open(m.getPath)
	if err != nil {
		return nil, 0, err
	}
	stat, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, stat.Size(), nil
}

type counterSnap struct {
	hits, misses, pending, failed, enqueued       float64
	headHits, headMisses, headPending, headFailed float64
}

func snapCounters() counterSnap {
	return counterSnap{
		hits:        testutil.ToFloat64(metrics.CacheHitsTotal),
		misses:      testutil.ToFloat64(metrics.CacheMissesTotal),
		pending:     testutil.ToFloat64(metrics.CachePendingTotal),
		failed:      testutil.ToFloat64(metrics.CacheFailedTotal),
		enqueued:    testutil.ToFloat64(metrics.JobsEnqueuedTotal),
		headHits:    testutil.ToFloat64(metrics.CacheHeadHitsTotal),
		headMisses:  testutil.ToFloat64(metrics.CacheHeadMissesTotal),
		headPending: testutil.ToFloat64(metrics.CacheHeadPendingTotal),
		headFailed:  testutil.ToFloat64(metrics.CacheHeadFailedTotal),
	}
}

func (c counterSnap) delta(after counterSnap) counterSnap {
	return counterSnap{
		hits:        after.hits - c.hits,
		misses:      after.misses - c.misses,
		pending:     after.pending - c.pending,
		failed:      after.failed - c.failed,
		enqueued:    after.enqueued - c.enqueued,
		headHits:    after.headHits - c.headHits,
		headMisses:  after.headMisses - c.headMisses,
		headPending: after.headPending - c.headPending,
		headFailed:  after.headFailed - c.headFailed,
	}
}

func newTestHandler(store *mockStore, q *mockQueue, blob *mockBlob) *Handler {
	return &Handler{
		store:   store,
		storage: blob,
		queue:   q,
		cfg:     config.Config{RetryAfterSeconds: 2},
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func doGet(t *testing.T, h *Handler, imageID uuid.UUID) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/images/"+imageID.String()+"?w=100", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", imageID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	h.handleGet(rr, req)
	return rr.Result()
}

func doHead(t *testing.T, h *Handler, imageID uuid.UUID) *http.Response {
	t.Helper()
	return doHeadRaw(t, h, imageID.String(), "w=100")
}

func doHeadRaw(t *testing.T, h *Handler, idParam, rawQuery string) *http.Response {
	t.Helper()
	u := "/images/" + idParam
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodHead, u, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", idParam)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	h.handleHead(rr, req)
	return rr.Result()
}

func mustTempAVIF(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "v.avif")
	if err := os.WriteFile(path, []byte("avif-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHandleGet_CacheMetrics(t *testing.T) {
	imageID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	variantID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	relPath := "variants/v.avif"

	tests := []struct {
		name       string
		setup      func(t *testing.T) (*Handler, *mockQueue)
		wantStatus int
		want       counterSnap
		checkQueue func(t *testing.T, q *mockQueue)
	}{
		{
			name: "ready hit increments cache_hits only",
			setup: func(t *testing.T) (*Handler, *mockQueue) {
				path := mustTempAVIF(t)
				q := &mockQueue{}
				h := newTestHandler(&mockStore{
					image:      db.Image{ID: imageID},
					variant:    db.Variant{ID: variantID, Status: db.StatusReady, Path: &relPath},
					variantErr: nil,
				}, q, &mockBlob{getPath: path})
				return h, q
			},
			wantStatus: http.StatusOK,
			want:       counterSnap{hits: 1},
		},
		{
			name: "cold miss increments misses and enqueued",
			setup: func(t *testing.T) (*Handler, *mockQueue) {
				q := &mockQueue{}
				h := newTestHandler(&mockStore{
					image:         db.Image{ID: imageID},
					variantErr:    db.ErrNotFound,
					upsertVariant: db.Variant{ID: variantID, Status: db.StatusPending},
					upsertCreated: true,
				}, q, &mockBlob{})
				return h, q
			},
			wantStatus: http.StatusAccepted,
			want:       counterSnap{misses: 1, enqueued: 1},
			checkQueue: func(t *testing.T, q *mockQueue) {
				if len(q.published) != 1 || q.published[0] != variantID.String() {
					t.Fatalf("published = %v, want [%s]", q.published, variantID)
				}
			},
		},
		{
			name: "pending poll increments pending not miss",
			setup: func(t *testing.T) (*Handler, *mockQueue) {
				q := &mockQueue{}
				h := newTestHandler(&mockStore{
					image:   db.Image{ID: imageID},
					variant: db.Variant{ID: variantID, Status: db.StatusPending},
				}, q, &mockBlob{})
				return h, q
			},
			wantStatus: http.StatusAccepted,
			want:       counterSnap{pending: 1},
			checkQueue: func(t *testing.T, q *mockQueue) {
				if len(q.published) != 0 {
					t.Fatalf("published = %v, want none", q.published)
				}
			},
		},
		{
			name: "failed increments cache_failed",
			setup: func(t *testing.T) (*Handler, *mockQueue) {
				q := &mockQueue{}
				msg := "boom"
				h := newTestHandler(&mockStore{
					image:   db.Image{ID: imageID},
					variant: db.Variant{ID: variantID, Status: db.StatusFailed, LastError: &msg},
				}, q, &mockBlob{})
				return h, q
			},
			wantStatus: http.StatusUnprocessableEntity,
			want:       counterSnap{failed: 1},
		},
		{
			name: "race ready after upsert increments hits",
			setup: func(t *testing.T) (*Handler, *mockQueue) {
				path := mustTempAVIF(t)
				q := &mockQueue{}
				h := newTestHandler(&mockStore{
					image:         db.Image{ID: imageID},
					variantErr:    db.ErrNotFound,
					upsertVariant: db.Variant{ID: variantID, Status: db.StatusReady, Path: &relPath},
					upsertCreated: false,
				}, q, &mockBlob{getPath: path})
				return h, q
			},
			wantStatus: http.StatusOK,
			want:       counterSnap{hits: 1},
		},
		{
			name: "upsert race pending without create increments pending not miss",
			setup: func(t *testing.T) (*Handler, *mockQueue) {
				q := &mockQueue{}
				h := newTestHandler(&mockStore{
					image:         db.Image{ID: imageID},
					variantErr:    db.ErrNotFound,
					upsertVariant: db.Variant{ID: variantID, Status: db.StatusPending},
					upsertCreated: false,
				}, q, &mockBlob{})
				return h, q
			},
			wantStatus: http.StatusAccepted,
			want:       counterSnap{pending: 1},
			checkQueue: func(t *testing.T, q *mockQueue) {
				if len(q.published) != 0 {
					t.Fatalf("published = %v, want none", q.published)
				}
			},
		},
		{
			name: "publish fail increments neither miss nor enqueued",
			setup: func(t *testing.T) (*Handler, *mockQueue) {
				q := &mockQueue{err: errors.New("amqp down")}
				h := newTestHandler(&mockStore{
					image:         db.Image{ID: imageID},
					variantErr:    db.ErrNotFound,
					upsertVariant: db.Variant{ID: variantID, Status: db.StatusPending},
					upsertCreated: true,
				}, q, &mockBlob{})
				return h, q
			},
			wantStatus: http.StatusServiceUnavailable,
			want:       counterSnap{},
		},
		{
			name: "race failed after upsert increments failed",
			setup: func(t *testing.T) (*Handler, *mockQueue) {
				q := &mockQueue{}
				h := newTestHandler(&mockStore{
					image:         db.Image{ID: imageID},
					variantErr:    db.ErrNotFound,
					upsertVariant: db.Variant{ID: variantID, Status: db.StatusFailed},
					upsertCreated: false,
				}, q, &mockBlob{})
				return h, q
			},
			wantStatus: http.StatusUnprocessableEntity,
			want:       counterSnap{failed: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, q := tt.setup(t)
			before := snapCounters()
			resp := doGet(t, h, imageID)
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			got := before.delta(snapCounters())
			if got != tt.want {
				t.Fatalf("counter deltas = %+v, want %+v", got, tt.want)
			}
			if tt.checkQueue != nil {
				tt.checkQueue(t, q)
			}
		})
	}
}

func TestHandleGet_MissIsNotPending(t *testing.T) {
	imageID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	variantID := uuid.MustParse("44444444-4444-4444-4444-444444444444")

	// Cold miss
	before := snapCounters()
	hMiss, _ := func() (*Handler, *mockQueue) {
		q := &mockQueue{}
		return newTestHandler(&mockStore{
			image:         db.Image{ID: imageID},
			variantErr:    db.ErrNotFound,
			upsertVariant: db.Variant{ID: variantID, Status: db.StatusPending},
			upsertCreated: true,
		}, q, &mockBlob{}), q
	}()
	resp := doGet(t, hMiss, imageID)
	resp.Body.Close()
	missDelta := before.delta(snapCounters())
	if missDelta.misses != 1 || missDelta.pending != 0 || missDelta.enqueued != 1 {
		t.Fatalf("cold miss deltas = %+v, want misses=1 pending=0 enqueued=1", missDelta)
	}

	// Pending poll
	before = snapCounters()
	hPend, q := func() (*Handler, *mockQueue) {
		q := &mockQueue{}
		return newTestHandler(&mockStore{
			image:   db.Image{ID: imageID},
			variant: db.Variant{ID: variantID, Status: db.StatusPending},
		}, q, &mockBlob{}), q
	}()
	resp = doGet(t, hPend, imageID)
	resp.Body.Close()
	pendDelta := before.delta(snapCounters())
	if pendDelta.pending != 1 || pendDelta.misses != 0 || pendDelta.enqueued != 0 {
		t.Fatalf("pending poll deltas = %+v, want pending=1 misses=0 enqueued=0", pendDelta)
	}
	if len(q.published) != 0 {
		t.Fatalf("pending poll must not publish, got %v", q.published)
	}
}
