package http

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/notrealscooby/image-optimizer/internal/config"
	"github.com/notrealscooby/image-optimizer/internal/db"
	"github.com/notrealscooby/image-optimizer/internal/queue"
	"github.com/notrealscooby/image-optimizer/internal/storage"
)

// Handler holds dependencies for image API endpoints.
type Handler struct {
	store   *db.Store
	storage *storage.Storage
	queue   *queue.Client
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

	r.Route("/images", func(r chi.Router) {
		r.Post("/", h.handleUpload)
		r.Get("/{id}", h.handleGet)
		r.Delete("/{id}", h.handleDelete)
	})

	return r
}
