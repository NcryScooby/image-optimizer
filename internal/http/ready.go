package http

import (
	"context"
	"net/http"
	"time"
)

// ReadyTimeout is the overall budget for GET /ready dependency checks.
const ReadyTimeout = 2 * time.Second

// Pinger reports whether a dependency is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// ReadyResponse is the JSON body for GET /ready.
type ReadyResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// CheckReady pings postgres and rabbitmq under a short timeout.
// Returns 200 + status "ok" when both succeed; otherwise 503 + "not_ready".
func CheckReady(ctx context.Context, postgres, rabbitmq Pinger) (int, ReadyResponse) {
	ctx, cancel := context.WithTimeout(ctx, ReadyTimeout)
	defer cancel()

	checks := map[string]string{
		"postgres": "ok",
		"rabbitmq": "ok",
	}
	ready := true

	if postgres == nil || postgres.Ping(ctx) != nil {
		checks["postgres"] = "fail"
		ready = false
	}
	if rabbitmq == nil || rabbitmq.Ping(ctx) != nil {
		checks["rabbitmq"] = "fail"
		ready = false
	}

	if ready {
		return http.StatusOK, ReadyResponse{Status: "ok", Checks: checks}
	}
	return http.StatusServiceUnavailable, ReadyResponse{Status: "not_ready", Checks: checks}
}

// ReadyHandler returns a reusable GET /ready handler (API or worker metrics mux).
func ReadyHandler(postgres, rabbitmq Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, body := CheckReady(r.Context(), postgres, rabbitmq)
		writeJSON(w, code, body)
	}
}
