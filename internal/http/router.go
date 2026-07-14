package http

import (
	"context"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/notrealscooby/image-optimizer/internal/config"
	"github.com/notrealscooby/image-optimizer/internal/db"
	"github.com/notrealscooby/image-optimizer/internal/imgproxy"
	_ "github.com/notrealscooby/image-optimizer/internal/metrics"
	"github.com/notrealscooby/image-optimizer/internal/queue"
	"github.com/notrealscooby/image-optimizer/internal/storage"
	"github.com/notrealscooby/image-optimizer/internal/variantgen"
)

type imageStore interface {
	InsertImage(ctx context.Context, id uuid.UUID, originalPath, contentType string, sizeBytes int64) (db.Image, error)
	GetImage(ctx context.Context, id uuid.UUID) (db.Image, error)
	DeleteImage(ctx context.Context, id uuid.UUID) error
	GetVariantByHash(ctx context.Context, imageID uuid.UUID, paramsHash string) (db.Variant, error)
	GetVariantByID(ctx context.Context, id uuid.UUID) (db.Variant, error)
	UpsertPendingVariant(ctx context.Context, imageID uuid.UUID, paramsHash string, paramsJSON []byte) (db.Variant, bool, error)
	MarkReady(ctx context.Context, id uuid.UUID, path string) error
	Ping(ctx context.Context) error
}

type jobQueue interface {
	Publish(ctx context.Context, variantID string) error
	Ping(ctx context.Context) error
}

type blobStore interface {
	SaveOriginal(ctx context.Context, folder, id, ext string, data []byte) (string, error)
	WriteVariant(ctx context.Context, imageID, paramsHash string, data []byte) (string, error)
	DeleteImageFiles(ctx context.Context, imageID, originalPath string) error
	Get(ctx context.Context, path string) (io.ReadCloser, int64, error)
}

type imgproxyFetcher interface {
	Fetch(ctx context.Context, path string) ([]byte, error)
}

type Handler struct {
	store    imageStore
	storage  blobStore
	queue    jobQueue
	imgproxy imgproxyFetcher
	cfg      config.Config
	log      *slog.Logger
}

func NewHandler(store *db.Store, stor *storage.Storage, q *queue.Client, img *imgproxy.Client, cfg config.Config) *Handler {
	return &Handler{
		store:    store,
		storage:  stor,
		queue:    q,
		imgproxy: img,
		cfg:      cfg,
		log:      slog.Default(),
	}
}

func (h *Handler) variantGen() variantgen.Deps {
	return variantgen.Deps{
		DB:       h.store,
		Storage:  h.storage,
		Imgproxy: h.imgproxy,
		S3Bucket: h.cfg.S3Bucket,
	}
}

func NewRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	r.Get("/health", h.handleHealth)
	r.Get("/ready", h.handleReady)
	r.Handle("/metrics", promhttp.Handler())
	r.Route("/images", func(r chi.Router) {
		r.Post("/", h.handleUpload)
		r.Post("/{id}/copy", h.handleCopy)
		r.Get("/{id}", h.handleGet)
		r.Head("/{id}", h.handleHead)
		r.Delete("/{id}", h.handleDelete)
	})
	return r
}

func NewPublicRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	mountPublicRoutes(r, h)
	return r
}

func NewWriteRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	mountWriteRoutes(r, h)
	return r
}

func mountPublicRoutes(r chi.Router, h *Handler) {
	r.Get("/health", h.handleHealth)
	r.Get("/ready", h.handleReady)
	r.Handle("/metrics", promhttp.Handler())
	r.Route("/images", func(r chi.Router) {
		r.Get("/{id}", h.handleGet)
		r.Head("/{id}", h.handleHead)
	})
}

func mountWriteRoutes(r chi.Router, h *Handler) {
	r.Route("/images", func(r chi.Router) {
		r.Post("/", h.handleUpload)
		r.Post("/{id}/copy", h.handleCopy)
		r.Delete("/{id}", h.handleDelete)
	})
}
