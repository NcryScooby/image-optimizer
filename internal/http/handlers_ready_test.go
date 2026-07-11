package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type mockPinger struct {
	err error
}

func (m *mockPinger) Ping(context.Context) error { return m.err }

func TestHandleReady(t *testing.T) {
	tests := []struct {
		name       string
		store      *mockStore
		queue      *mockQueue
		wantStatus int
		wantBody   ReadyResponse
	}{
		{
			name:       "all ok",
			store:      &mockStore{},
			queue:      &mockQueue{},
			wantStatus: http.StatusOK,
			wantBody: ReadyResponse{
				Status: "ok",
				Checks: map[string]string{"postgres": "ok", "rabbitmq": "ok"},
			},
		},
		{
			name:       "postgres fail",
			store:      &mockStore{pingErr: errors.New("db down")},
			queue:      &mockQueue{},
			wantStatus: http.StatusServiceUnavailable,
			wantBody: ReadyResponse{
				Status: "not_ready",
				Checks: map[string]string{"postgres": "fail", "rabbitmq": "ok"},
			},
		},
		{
			name:       "rabbit fail",
			store:      &mockStore{},
			queue:      &mockQueue{pingErr: errors.New("amqp down")},
			wantStatus: http.StatusServiceUnavailable,
			wantBody: ReadyResponse{
				Status: "not_ready",
				Checks: map[string]string{"postgres": "ok", "rabbitmq": "fail"},
			},
		},
		{
			name:       "both fail",
			store:      &mockStore{pingErr: errors.New("db down")},
			queue:      &mockQueue{pingErr: errors.New("amqp down")},
			wantStatus: http.StatusServiceUnavailable,
			wantBody: ReadyResponse{
				Status: "not_ready",
				Checks: map[string]string{"postgres": "fail", "rabbitmq": "fail"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.store, tt.queue, &mockBlob{})
			req := httptest.NewRequest(http.MethodGet, "/ready", nil)
			rr := httptest.NewRecorder()
			h.handleReady(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rr.Code, tt.wantStatus)
			}

			var got ReadyResponse
			if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Status != tt.wantBody.Status {
				t.Fatalf("status = %q, want %q", got.Status, tt.wantBody.Status)
			}
			for k, want := range tt.wantBody.Checks {
				if got.Checks[k] != want {
					t.Fatalf("checks[%s] = %q, want %q", k, got.Checks[k], want)
				}
			}
		})
	}
}

func TestReadyHandler_Reusable(t *testing.T) {
	handler := ReadyHandler(&mockPinger{}, &mockPinger{err: errors.New("down")})
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
	var got ReadyResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != "not_ready" || got.Checks["rabbitmq"] != "fail" || got.Checks["postgres"] != "ok" {
		t.Fatalf("unexpected body: %+v", got)
	}
}

func TestCheckReady_NilPingers(t *testing.T) {
	code, body := CheckReady(context.Background(), nil, nil)
	if code != http.StatusServiceUnavailable || body.Status != "not_ready" {
		t.Fatalf("got code=%d body=%+v", code, body)
	}
	if body.Checks["postgres"] != "fail" || body.Checks["rabbitmq"] != "fail" {
		t.Fatalf("checks = %+v", body.Checks)
	}
}
