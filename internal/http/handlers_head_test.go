package http

import (
	"io"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/notrealscooby/image-optimizer/internal/db"
)

func TestHandleHead_Ready(t *testing.T) {
	imageID := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	variantID := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	relPath := "variants/v.avif"

	blob := &mockBlob{} // no getPath — Get must not be called
	q := &mockQueue{}
	h := newTestHandler(&mockStore{
		image:   db.Image{ID: imageID},
		variant: db.Variant{ID: variantID, Status: db.StatusReady, Path: &relPath},
	}, q, blob)

	before := snapCounters()
	resp := doHead(t, h, imageID)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/avif" {
		t.Fatalf("Content-Type = %q, want image/avif", ct)
	}
	if len(body) != 0 {
		t.Fatalf("body len = %d, want 0", len(body))
	}
	if blob.getCalls != 0 {
		t.Fatalf("blob.Get calls = %d, want 0", blob.getCalls)
	}
	got := before.delta(snapCounters())
	want := counterSnap{headHits: 1}
	if got != want {
		t.Fatalf("counter deltas = %+v, want %+v", got, want)
	}
}

func TestHandleHead_PendingPoll(t *testing.T) {
	imageID := uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")
	variantID := uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd")

	q := &mockQueue{}
	h := newTestHandler(&mockStore{
		image:   db.Image{ID: imageID},
		variant: db.Variant{ID: variantID, Status: db.StatusPending},
	}, q, &mockBlob{})

	before := snapCounters()
	resp := doHead(t, h, imageID)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "2" {
		t.Fatalf("Retry-After = %q, want 2", ra)
	}
	if len(body) != 0 {
		t.Fatalf("body len = %d, want 0", len(body))
	}
	if len(q.published) != 0 {
		t.Fatalf("published = %v, want none", q.published)
	}
	got := before.delta(snapCounters())
	want := counterSnap{headPending: 1}
	if got != want {
		t.Fatalf("counter deltas = %+v, want %+v", got, want)
	}
}

func TestHandleHead_ColdMiss(t *testing.T) {
	imageID := uuid.MustParse("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee")
	variantID := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")

	q := &mockQueue{}
	h := newTestHandler(&mockStore{
		image:         db.Image{ID: imageID},
		variantErr:    db.ErrNotFound,
		upsertVariant: db.Variant{ID: variantID, Status: db.StatusPending},
		upsertCreated: true,
	}, q, &mockBlob{})

	before := snapCounters()
	resp := doHead(t, h, imageID)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if len(body) != 0 {
		t.Fatalf("body len = %d, want 0", len(body))
	}
	if len(q.published) != 1 || q.published[0] != variantID.String() {
		t.Fatalf("published = %v, want [%s]", q.published, variantID)
	}
	got := before.delta(snapCounters())
	want := counterSnap{headMisses: 1, enqueued: 1}
	if got != want {
		t.Fatalf("counter deltas = %+v, want %+v (must not touch cache_misses)", got, want)
	}
	if got.misses != 0 {
		t.Fatalf("cache_misses delta = %v, want 0", got.misses)
	}
}

func TestHandleHead_Failed(t *testing.T) {
	imageID := uuid.MustParse("11111111-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	variantID := uuid.MustParse("22222222-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	msg := "boom"

	h := newTestHandler(&mockStore{
		image:   db.Image{ID: imageID},
		variant: db.Variant{ID: variantID, Status: db.StatusFailed, LastError: &msg},
	}, &mockQueue{}, &mockBlob{})

	before := snapCounters()
	resp := doHead(t, h, imageID)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if len(body) != 0 {
		t.Fatalf("body len = %d, want 0", len(body))
	}
	got := before.delta(snapCounters())
	want := counterSnap{headFailed: 1}
	if got != want {
		t.Fatalf("counter deltas = %+v, want %+v", got, want)
	}
}

func TestHandleHead_NotFoundImage(t *testing.T) {
	imageID := uuid.MustParse("33333333-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

	h := newTestHandler(&mockStore{
		imageErr: db.ErrNotFound,
	}, &mockQueue{}, &mockBlob{})

	before := snapCounters()
	resp := doHead(t, h, imageID)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	if len(body) != 0 {
		t.Fatalf("body len = %d, want 0", len(body))
	}
	got := before.delta(snapCounters())
	if got != (counterSnap{}) {
		t.Fatalf("counter deltas = %+v, want zero", got)
	}
}

func TestHandleHead_BadRequestParams(t *testing.T) {
	imageID := uuid.MustParse("44444444-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	h := newTestHandler(&mockStore{}, &mockQueue{}, &mockBlob{})

	resp := doHeadRaw(t, h, imageID.String(), "w=0")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	if len(body) != 0 {
		t.Fatalf("body len = %d, want 0", len(body))
	}
}

func TestHandleHead_BadRequestID(t *testing.T) {
	h := newTestHandler(&mockStore{}, &mockQueue{}, &mockBlob{})

	resp := doHeadRaw(t, h, "not-a-uuid", "w=100")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	if len(body) != 0 {
		t.Fatalf("body len = %d, want 0", len(body))
	}
}

func TestHandleHead_IsolationFromGetHits(t *testing.T) {
	imageID := uuid.MustParse("55555555-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	variantID := uuid.MustParse("66666666-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	relPath := "variants/v.avif"

	blob := &mockBlob{}
	h := newTestHandler(&mockStore{
		image:   db.Image{ID: imageID},
		variant: db.Variant{ID: variantID, Status: db.StatusReady, Path: &relPath},
	}, &mockQueue{}, blob)

	before := snapCounters()
	resp := doHead(t, h, imageID)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	got := before.delta(snapCounters())
	if got.hits != 0 {
		t.Fatalf("cache_hits_total delta = %v, want 0 (HEAD must not Inc GET hits)", got.hits)
	}
	if got.headHits != 1 {
		t.Fatalf("cache_head_hits_total delta = %v, want 1", got.headHits)
	}
}
