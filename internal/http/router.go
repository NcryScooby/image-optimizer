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
	_ "github.com/notrealscooby/image-optimizer/internal/metrics" // register Prometheus metrics
	"github.com/notrealscooby/image-optimizer/internal/queue"
	"github.com/notrealscooby/image-optimizer/internal/storage"
)

// imageStore is the persistence surface used by handlers.
type imageStore interface {
	InsertImage(ctx context.Context, id uuid.UUID, originalPath, contentType string, sizeBytes int64) (db.Image, error)
	GetImage(ctx context.Context, id uuid.UUID) (db.Image, error)
	DeleteImage(ctx context.Context, id uuid.UUID) error
	GetVariantByHash(ctx context.Context, imageID uuid.UUID, paramsHash string) (db.Variant, error)
	UpsertPendingVariant(ctx context.Context, imageID uuid.UUID, paramsHash string, paramsJSON []byte) (db.Variant, bool, error)
	DeletePendingVariant(ctx context.Context, id uuid.UUID) error
	Ping(ctx context.Context) error
}

// jobQueue publishes variant processing jobs.
type jobQueue interface {
	Publish(ctx context.Context, variantID string) error
	Ping(ctx context.Context) error
}

// blobStore reads and writes image blobs (S3/MinIO).
type blobStore interface {
	SaveOriginal(ctx context.Context, id, ext string, data []byte) (string, error)
	DeleteImageFiles(ctx context.Context, imageID, originalPath string) error
	Get(ctx context.Context, path string) (io.ReadCloser, int64, error)
}

// Handler holds dependencies for image API endpoints.
type Handler struct {
	store   imageStore
	storage blobStore
	queue   jobQueue
	cfg     config.Config
	log     *slog.Logger
}

// NewHandler wires Store, Storage, Queue, and Config into HTTP handlers.
func NewHandler(store *db.Store, stor *storage.Storage, q *queue.Client, cfg config.Config) *Handler {
	return &Handler{
		store:   store,
		storage: stor,
		queue:   q,
		cfg:     cfg,
		log:     slog.Default(),
	}
}

// NewRouter builds the chi router with all v1 routes.
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
		r.Get("/{id}", h.handleGet)
		r.Head("/{id}", h.handleHead)
		r.Delete("/{id}", h.handleDelete)
	})

	return r
}
