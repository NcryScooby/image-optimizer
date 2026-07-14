package http

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	byID           db.Variant
	byIDErr        error
	byIDFn         func() (db.Variant, error)
	markReadyPath  string
	markReadyCalls int
	pingErr        error
	insertPath     string
	insertCalls    int
}

func (m *mockStore) InsertImage(_ context.Context, id uuid.UUID, originalPath, contentType string, sizeBytes int64) (db.Image, error) {
	m.insertCalls++
	m.insertPath = originalPath
	return db.Image{ID: id, OriginalPath: originalPath, ContentType: contentType, SizeBytes: sizeBytes}, nil
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
func (m *mockStore) GetVariantByID(context.Context, uuid.UUID) (db.Variant, error) {
	if m.byIDFn != nil {
		return m.byIDFn()
	}
	if m.byIDErr != nil {
		return db.Variant{}, m.byIDErr
	}
	if m.byID.ID != uuid.Nil {
		return m.byID, nil
	}
	return m.upsertVariant, nil
}
func (m *mockStore) UpsertPendingVariant(context.Context, uuid.UUID, string, []byte) (db.Variant, bool, error) {
	if m.upsertErr != nil {
		return db.Variant{}, false, m.upsertErr
	}
	return m.upsertVariant, m.upsertCreated, nil
}
func (m *mockStore) MarkReady(_ context.Context, id uuid.UUID, path string) error {
	m.markReadyCalls++
	m.markReadyPath = path
	ready := m.upsertVariant
	if ready.ID == uuid.Nil {
		ready = m.variant
	}
	ready.ID = id
	ready.Status = db.StatusReady
	ready.Path = &path
	m.byID = ready
	m.upsertVariant = ready
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
	getPath       string
	getCalls      int
	writeCalls    int
	writePath     string
	saveFolder    string
	saveID        string
	saveCalls     int
	saveErr       error
	writeErr      error
}

func (m *mockBlob) SaveOriginal(_ context.Context, folder, id, ext string, data []byte) (string, error) {
	m.saveCalls++
	m.saveFolder = folder
	m.saveID = id
	if m.saveErr != nil {
		return "", m.saveErr
	}
	return folder + "/" + id + "." + ext, nil
}
func (m *mockBlob) WriteVariant(_ context.Context, imageID, paramsHash string, data []byte) (string, error) {
	m.writeCalls++
	if m.writeErr != nil {
		return "", m.writeErr
	}
	m.writePath = "variants/" + imageID + "/" + paramsHash + ".avif"
	return m.writePath, nil
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

type mockImgproxy struct {
	data  []byte
	err   error
	calls int
}

func (m *mockImgproxy) Fetch(context.Context, string) ([]byte, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	if m.data != nil {
		return m.data, nil
	}
	return []byte("avif-bytes"), nil
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
	return newTestHandlerWithImg(store, q, blob, &mockImgproxy{})
}

func newTestHandlerWithImg(store *mockStore, q *mockQueue, blob *mockBlob, img *mockImgproxy) *Handler {
	return &Handler{
		store:    store,
		storage:  blob,
		queue:    q,
		imgproxy: img,
		cfg: config.Config{
			RetryAfterSeconds:    2,
			SyncTransformTimeout: 200 * time.Millisecond,
			S3Bucket:             "images",
			MaxUploadBytes:       10 << 20,
		},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
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

func jpegBytes() []byte {
	return []byte{
		0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01,
		0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xff, 0xd9,
	}
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
					image:   db.Image{ID: imageID, OriginalPath: "storely/1/catalog/" + imageID.String() + ".jpg"},
					variant: db.Variant{ID: variantID, Status: db.StatusReady, Path: &relPath},
				}, q, &mockBlob{getPath: path})
				return h, q
			},
			wantStatus: http.StatusOK,
			want:       counterSnap{hits: 1},
		},
		{
			name: "cold miss sync returns 200 and increments misses and enqueued",
			setup: func(t *testing.T) (*Handler, *mockQueue) {
				path := mustTempAVIF(t)
				q := &mockQueue{}
				store := &mockStore{
					image: db.Image{
						ID:           imageID,
						OriginalPath: "storely/1/catalog/" + imageID.String() + ".jpg",
					},
					variantErr: db.ErrNotFound,
					upsertVariant: db.Variant{
						ID:         variantID,
						ImageID:    imageID,
						ParamsHash: "abc",
						ParamsJSON: []byte(`{"w":100,"h":0,"crop":"center","q":80,"fit":"cover"}`),
						Status:     db.StatusPending,
					},
					upsertCreated: true,
				}
				blob := &mockBlob{getPath: path}
				h := newTestHandler(store, q, blob)
				return h, q
			},
			wantStatus: http.StatusOK,
			want:       counterSnap{misses: 1, enqueued: 1},
			checkQueue: func(t *testing.T, q *mockQueue) {
				if len(q.published) != 1 || q.published[0] != variantID.String() {
					t.Fatalf("published = %v, want [%s]", q.published, variantID)
				}
			},
		},
		{
			name: "pending wait then ready increments pending",
			setup: func(t *testing.T) (*Handler, *mockQueue) {
				path := mustTempAVIF(t)
				q := &mockQueue{}
				calls := 0
				store := &mockStore{
					image:   db.Image{ID: imageID, OriginalPath: "storely/1/catalog/x.jpg"},
					variant: db.Variant{ID: variantID, Status: db.StatusPending},
					byIDFn: func() (db.Variant, error) {
						calls++
						if calls < 2 {
							return db.Variant{ID: variantID, Status: db.StatusPending}, nil
						}
						return db.Variant{ID: variantID, Status: db.StatusReady, Path: &relPath}, nil
					},
				}
				h := newTestHandler(store, q, &mockBlob{getPath: path})
				return h, q
			},
			wantStatus: http.StatusOK,
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
			name: "publish fail still syncs on cold miss",
			setup: func(t *testing.T) (*Handler, *mockQueue) {
				path := mustTempAVIF(t)
				q := &mockQueue{err: errors.New("amqp down")}
				store := &mockStore{
					image: db.Image{
						ID:           imageID,
						OriginalPath: "storely/1/catalog/x.jpg",
					},
					variantErr: db.ErrNotFound,
					upsertVariant: db.Variant{
						ID:         variantID,
						ImageID:    imageID,
						ParamsHash: "abc",
						ParamsJSON: []byte(`{"w":100,"h":0,"crop":"center","q":80,"fit":"cover"}`),
						Status:     db.StatusPending,
					},
					upsertCreated: true,
				}
				h := newTestHandler(store, q, &mockBlob{getPath: path})
				return h, q
			},
			wantStatus: http.StatusOK,
			want:       counterSnap{misses: 1},
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

func TestHandleGet_ColdMissNever202(t *testing.T) {
	imageID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	variantID := uuid.MustParse("44444444-4444-4444-4444-444444444444")
	path := mustTempAVIF(t)

	store := &mockStore{
		image: db.Image{
			ID:           imageID,
			OriginalPath: "storely/1/catalog/x.jpg",
		},
		variantErr: db.ErrNotFound,
		upsertVariant: db.Variant{
			ID:         variantID,
			ImageID:    imageID,
			ParamsHash: "abc",
			ParamsJSON: []byte(`{"w":100,"h":0,"crop":"center","q":80,"fit":"cover"}`),
			Status:     db.StatusPending,
		},
		upsertCreated: true,
	}
	h := newTestHandler(store, &mockQueue{}, &mockBlob{getPath: path})
	resp := doGet(t, h, imageID)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "image/avif" {
		t.Fatalf("Content-Type = %q", resp.Header.Get("Content-Type"))
	}
	if store.markReadyCalls != 1 {
		t.Fatalf("MarkReady calls = %d, want 1", store.markReadyCalls)
	}
}

func TestHandleUpload_FolderKey(t *testing.T) {
	blob := &mockBlob{}
	store := &mockStore{}
	h := newTestHandler(store, &mockQueue{}, blob)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("folder", "storely/1/catalog")
	part, err := w.CreateFormFile("file", "sample.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(jpegBytes()); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/images", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rr := httptest.NewRecorder()
	h.handleUpload(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if blob.saveFolder != "storely/1/catalog" {
		t.Fatalf("save folder = %q", blob.saveFolder)
	}
	if store.insertCalls != 1 {
		t.Fatalf("insertCalls = %d", store.insertCalls)
	}
	if store.insertPath != "storely/1/catalog/"+blob.saveID+".jpg" {
		t.Fatalf("insertPath = %q", store.insertPath)
	}
}

func TestHandleUpload_MissingFolder(t *testing.T) {
	h := newTestHandler(&mockStore{}, &mockQueue{}, &mockBlob{})

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", "sample.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(jpegBytes()); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/images", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rr := httptest.NewRecorder()
	h.handleUpload(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestHandleCopy_CopiesOriginalToNewFolder(t *testing.T) {
	tmp := t.TempDir()
	srcFile := filepath.Join(tmp, "src.jpg")
	jpeg := jpegBytes()
	if err := os.WriteFile(srcFile, jpeg, 0o644); err != nil {
		t.Fatal(err)
	}

	srcID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	store := &mockStore{
		image: db.Image{
			ID:           srcID,
			OriginalPath: "storely/1/catalog/" + srcID.String() + ".jpg",
			ContentType:  "image/jpeg",
			SizeBytes:    int64(len(jpeg)),
		},
	}
	blob := &mockBlob{getPath: srcFile}
	h := newTestHandler(store, &mockQueue{}, blob)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("folder", "storely/1/catalog")
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/images/"+srcID.String()+"/copy", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", srcID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	h.handleCopy(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if blob.getCalls != 1 {
		t.Fatalf("getCalls = %d", blob.getCalls)
	}
	if blob.saveFolder != "storely/1/catalog" {
		t.Fatalf("save folder = %q", blob.saveFolder)
	}
	if blob.saveID == srcID.String() {
		t.Fatal("copy must allocate a new image id")
	}
	if store.insertCalls != 1 {
		t.Fatalf("insertCalls = %d", store.insertCalls)
	}
	if store.insertPath != "storely/1/catalog/"+blob.saveID+".jpg" {
		t.Fatalf("insertPath = %q", store.insertPath)
	}
}

func TestHandleCopy_NotFound(t *testing.T) {
	srcID := uuid.New()
	store := &mockStore{imageErr: db.ErrNotFound}
	h := newTestHandler(store, &mockQueue{}, &mockBlob{})

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("folder", "storely/1/catalog")
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/images/"+srcID.String()+"/copy", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", srcID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	h.handleCopy(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}
