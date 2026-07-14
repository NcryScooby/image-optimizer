package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/notrealscooby/image-optimizer/internal/db"
	"github.com/notrealscooby/image-optimizer/internal/folder"
	"github.com/notrealscooby/image-optimizer/internal/metrics"
	"github.com/notrealscooby/image-optimizer/internal/transform"
)

type variantOutcome int

const (
	outcomeReady variantOutcome = iota
	outcomeFailed
	outcomeBadRequest
	outcomeNotFound
	outcomeUnavailable
	outcomeInternal
)

const peerReadyPollInterval = 50 * time.Millisecond

type resolveResult struct {
	outcome variantOutcome
	variant db.Variant
	errMsg  string
	miss    bool
	waited  bool
}

type uploadResponse struct {
	ID          string `json:"id"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleReady(w http.ResponseWriter, r *http.Request) {
	ReadyHandler(h.store, h.queue)(w, r)
}

func (h *Handler) handleUpload(w http.ResponseWriter, r *http.Request) {
	maxBody := h.cfg.MaxUploadBytes + 64<<10
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)

	if err := r.ParseMultipartForm(h.cfg.MaxUploadBytes); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) || isMaxBytes(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "file exceeds MAX_UPLOAD_BYTES")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}

	folderRaw := r.FormValue("folder")
	mediaFolder, err := folder.Validate(folderRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing multipart field \"file\"")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, h.cfg.MaxUploadBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read file")
		return
	}
	if int64(len(data)) > h.cfg.MaxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "file exceeds MAX_UPLOAD_BYTES")
		return
	}
	if len(data) == 0 {
		writeError(w, http.StatusBadRequest, "empty file")
		return
	}

	contentType, ext, err := detectImageType(data, hdr.Header.Get("Content-Type"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	id := uuid.New()
	relPath, err := h.storage.SaveOriginal(r.Context(), mediaFolder, id.String(), ext, data)
	if err != nil {
		h.log.Error("save original", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to store image")
		return
	}

	img, err := h.store.InsertImage(r.Context(), id, relPath, contentType, int64(len(data)))
	if err != nil {
		_ = h.storage.DeleteImageFiles(r.Context(), id.String(), relPath)
		h.log.Error("insert image", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to persist image")
		return
	}

	writeJSON(w, http.StatusCreated, uploadResponse{
		ID:          img.ID.String(),
		ContentType: img.ContentType,
		Size:        img.SizeBytes,
	})
}

func (h *Handler) handleCopy(w http.ResponseWriter, r *http.Request) {
	sourceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid image id")
		return
	}

	if err := r.ParseMultipartForm(1 << 20); err != nil {
		if err := r.ParseForm(); err != nil {
			writeError(w, http.StatusBadRequest, "invalid form")
			return
		}
	}

	mediaFolder, err := folder.Validate(r.FormValue("folder"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	src, err := h.store.GetImage(r.Context(), sourceID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "image not found")
			return
		}
		h.log.Error("get image for copy", "err", err, "id", sourceID)
		writeError(w, http.StatusInternalServerError, "failed to load image")
		return
	}

	rc, _, err := h.storage.Get(r.Context(), src.OriginalPath)
	if err != nil {
		h.log.Error("read original for copy", "err", err, "id", sourceID)
		writeError(w, http.StatusInternalServerError, "failed to read original")
		return
	}
	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, h.cfg.MaxUploadBytes+1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read original")
		return
	}
	if int64(len(data)) > h.cfg.MaxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "file exceeds MAX_UPLOAD_BYTES")
		return
	}
	if len(data) == 0 {
		writeError(w, http.StatusInternalServerError, "original file empty")
		return
	}

	ext, err := extensionForOriginal(src.OriginalPath, src.ContentType)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	newID := uuid.New()
	relPath, err := h.storage.SaveOriginal(r.Context(), mediaFolder, newID.String(), ext, data)
	if err != nil {
		h.log.Error("save copied original", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to store image")
		return
	}

	img, err := h.store.InsertImage(r.Context(), newID, relPath, src.ContentType, int64(len(data)))
	if err != nil {
		_ = h.storage.DeleteImageFiles(r.Context(), newID.String(), relPath)
		h.log.Error("insert copied image", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to persist image")
		return
	}

	writeJSON(w, http.StatusCreated, uploadResponse{
		ID:          img.ID.String(),
		ContentType: img.ContentType,
		Size:        img.SizeBytes,
	})
}

func extensionForOriginal(originalPath, contentType string) (string, error) {
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(originalPath)), ".")
	if ext != "" && !strings.ContainsAny(ext, `/\`) {
		return ext, nil
	}
	switch strings.ToLower(contentType) {
	case "image/jpeg", "image/jpg":
		return "jpg", nil
	case "image/png":
		return "png", nil
	case "image/webp":
		return "webp", nil
	default:
		return "", fmt.Errorf("cannot determine original extension")
	}
}

func (h *Handler) resolveVariant(ctx context.Context, idStr string, query url.Values) resolveResult {
	id, err := uuid.Parse(idStr)
	if err != nil {
		return resolveResult{outcome: outcomeBadRequest, errMsg: "invalid image id"}
	}

	params, err := transform.Parse(query)
	if err != nil {
		return resolveResult{outcome: outcomeBadRequest, errMsg: err.Error()}
	}
	paramsHash := transform.Hash(params)
	paramsJSON := transform.CacheKeyJSON(params)

	if _, err := h.store.GetImage(ctx, id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return resolveResult{outcome: outcomeNotFound, errMsg: "image not found"}
		}
		h.log.Error("get image", "err", err, "id", id)
		return resolveResult{outcome: outcomeInternal, errMsg: "failed to load image"}
	}

	variant, err := h.store.GetVariantByHash(ctx, id, paramsHash)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		h.log.Error("get variant", "err", err, "image_id", id)
		return resolveResult{outcome: outcomeInternal, errMsg: "failed to load variant"}
	}

	if errors.Is(err, db.ErrNotFound) {
		var created bool
		variant, created, err = h.store.UpsertPendingVariant(ctx, id, paramsHash, paramsJSON)
		if err != nil {
			h.log.Error("upsert pending variant", "err", err, "image_id", id)
			return resolveResult{outcome: outcomeInternal, errMsg: "failed to prepare variant"}
		}
		switch variant.Status {
		case db.StatusReady:
			return resolveResult{outcome: outcomeReady, variant: variant}
		case db.StatusFailed:
			return resolveResult{outcome: outcomeFailed, variant: variant}
		case db.StatusPending:
			if created {
				h.publishBestEffort(ctx, variant.ID)
				return h.syncGenerate(ctx, variant, true)
			}
			return h.waitOrSync(ctx, variant)
		default:
			return resolveResult{outcome: outcomeInternal, errMsg: "unknown variant status"}
		}
	}

	switch variant.Status {
	case db.StatusReady:
		return resolveResult{outcome: outcomeReady, variant: variant}
	case db.StatusPending:
		return h.waitOrSync(ctx, variant)
	case db.StatusFailed:
		return resolveResult{outcome: outcomeFailed, variant: variant}
	default:
		return resolveResult{outcome: outcomeInternal, errMsg: "unknown variant status"}
	}
}

func (h *Handler) publishBestEffort(ctx context.Context, variantID uuid.UUID) {
	if err := h.queue.Publish(ctx, variantID.String()); err != nil {
		h.log.Warn("best-effort enqueue failed", "err", err, "variant_id", variantID)
		return
	}
	metrics.JobsEnqueuedTotal.Inc()
}

func (h *Handler) syncGenerate(ctx context.Context, v db.Variant, miss bool) resolveResult {
	syncCtx, cancel := context.WithTimeout(ctx, h.cfg.SyncTransformTimeout)
	defer cancel()

	if err := h.variantGen().TransformAndPersist(syncCtx, v); err != nil {
		h.log.Error("sync transform failed", "err", err, "variant_id", v.ID)
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(syncCtx.Err(), context.DeadlineExceeded) {
			return resolveResult{outcome: outcomeUnavailable, errMsg: "variant transform timed out", miss: miss}
		}
		return resolveResult{outcome: outcomeInternal, errMsg: "failed to generate variant", miss: miss}
	}

	ready, err := h.store.GetVariantByID(ctx, v.ID)
	if err != nil {
		h.log.Error("reload variant after sync", "err", err, "variant_id", v.ID)
		return resolveResult{outcome: outcomeInternal, errMsg: "failed to load variant", miss: miss}
	}
	if ready.Status != db.StatusReady {
		return resolveResult{outcome: outcomeInternal, errMsg: "variant not ready after sync", miss: miss}
	}
	return resolveResult{outcome: outcomeReady, variant: ready, miss: miss}
}

func (h *Handler) waitOrSync(ctx context.Context, v db.Variant) resolveResult {
	deadline := time.Now().Add(h.cfg.SyncTransformTimeout)
	for time.Now().Before(deadline) {
		current, err := h.store.GetVariantByID(ctx, v.ID)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return resolveResult{outcome: outcomeNotFound, errMsg: "image not found", waited: true}
			}
			h.log.Error("poll variant", "err", err, "variant_id", v.ID)
			return resolveResult{outcome: outcomeInternal, errMsg: "failed to load variant", waited: true}
		}
		switch current.Status {
		case db.StatusReady:
			return resolveResult{outcome: outcomeReady, variant: current, waited: true}
		case db.StatusFailed:
			return resolveResult{outcome: outcomeFailed, variant: current, waited: true}
		}

		timer := time.NewTimer(peerReadyPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return resolveResult{outcome: outcomeUnavailable, errMsg: "request cancelled", waited: true}
		case <-timer.C:
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
	}

	current, err := h.store.GetVariantByID(ctx, v.ID)
	if err == nil {
		switch current.Status {
		case db.StatusReady:
			return resolveResult{outcome: outcomeReady, variant: current, waited: true}
		case db.StatusFailed:
			return resolveResult{outcome: outcomeFailed, variant: current, waited: true}
		case db.StatusPending:
			res := h.syncGenerate(ctx, current, false)
			res.waited = true
			return res
		}
	}
	return resolveResult{outcome: outcomeUnavailable, errMsg: "variant transform timed out", waited: true}
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	res := h.resolveVariant(r.Context(), chi.URLParam(r, "id"), r.URL.Query())
	switch res.outcome {
	case outcomeReady:
		if res.miss {
			metrics.CacheMissesTotal.Inc()
		} else if res.waited {
			metrics.CachePendingTotal.Inc()
		} else {
			metrics.CacheHitsTotal.Inc()
		}
		h.serveVariant(w, r, res.variant)
	case outcomeFailed:
		metrics.CacheFailedTotal.Inc()
		h.writeFailed(w, res.variant)
	case outcomeBadRequest:
		writeError(w, http.StatusBadRequest, res.errMsg)
	case outcomeNotFound:
		writeError(w, http.StatusNotFound, res.errMsg)
	case outcomeUnavailable:
		writeError(w, http.StatusServiceUnavailable, res.errMsg)
	case outcomeInternal:
		writeError(w, http.StatusInternalServerError, res.errMsg)
	}
}

func (h *Handler) handleHead(w http.ResponseWriter, r *http.Request) {
	res := h.resolveVariant(r.Context(), chi.URLParam(r, "id"), r.URL.Query())
	switch res.outcome {
	case outcomeReady:
		if res.miss {
			metrics.CacheHeadMissesTotal.Inc()
		} else if res.waited {
			metrics.CacheHeadPendingTotal.Inc()
		} else {
			metrics.CacheHeadHitsTotal.Inc()
		}
		w.Header().Set("Content-Type", "image/avif")
		w.WriteHeader(http.StatusOK)
	case outcomeFailed:
		metrics.CacheHeadFailedTotal.Inc()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
	case outcomeBadRequest:
		w.WriteHeader(http.StatusBadRequest)
	case outcomeNotFound:
		w.WriteHeader(http.StatusNotFound)
	case outcomeUnavailable:
		w.WriteHeader(http.StatusServiceUnavailable)
	case outcomeInternal:
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid image id")
		return
	}

	img, err := h.store.GetImage(r.Context(), id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "image not found")
			return
		}
		h.log.Error("get image for delete", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError, "failed to load image")
		return
	}

	if err := h.storage.DeleteImageFiles(r.Context(), id.String(), img.OriginalPath); err != nil {
		h.log.Error("delete image files", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError, "failed to delete image files")
		return
	}

	if err := h.store.DeleteImage(r.Context(), id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.log.Error("delete image row", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError, "failed to delete image")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) serveVariant(w http.ResponseWriter, r *http.Request, v db.Variant) {
	if v.Path == nil || *v.Path == "" {
		h.log.Error("ready variant missing path", "variant_id", v.ID)
		writeError(w, http.StatusInternalServerError, "variant file missing")
		return
	}

	body, size, err := h.storage.Get(r.Context(), *v.Path)
	if err != nil {
		h.log.Error("get variant", "err", err, "path", *v.Path)
		writeError(w, http.StatusInternalServerError, "failed to open variant")
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", "image/avif")
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, body); err != nil {
		h.log.Error("copy variant", "err", err, "path", *v.Path)
	}
}

func (h *Handler) writeFailed(w http.ResponseWriter, v db.Variant) {
	msg := "variant processing failed"
	if v.LastError != nil && *v.LastError != "" {
		msg = *v.LastError
	}
	writeError(w, http.StatusUnprocessableEntity, msg)
}

func detectImageType(data []byte, declared string) (contentType, ext string, err error) {
	sniffed := http.DetectContentType(data)
	ct := sniffed
	if !isAllowedImageType(sniffed) && isAllowedImageType(declared) {
		ct = declared
	}
	if !isAllowedImageType(ct) {
		if isWebP(data) {
			ct = "image/webp"
		} else {
			return "", "", fmt.Errorf("unsupported content type: only JPEG, PNG, WebP allowed")
		}
	}

	switch ct {
	case "image/jpeg":
		return ct, "jpg", nil
	case "image/png":
		return ct, "png", nil
	case "image/webp":
		return ct, "webp", nil
	default:
		return "", "", fmt.Errorf("unsupported content type: only JPEG, PNG, WebP allowed")
	}
}

func isAllowedImageType(ct string) bool {
	ct = strings.TrimSpace(strings.ToLower(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "image/jpeg", "image/png", "image/webp":
		return true
	default:
		return false
	}
}

func isWebP(data []byte) bool {
	return len(data) >= 12 &&
		string(data[0:4]) == "RIFF" &&
		string(data[8:12]) == "WEBP"
}

func isMaxBytes(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "http: request body too large") ||
		strings.Contains(msg, "multipart: message too large")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}
