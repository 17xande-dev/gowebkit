package middleware

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccessLogPatternAndRequestID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /lookup/{code}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Handler-ID", ID(r.Context()))
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusAccepted)
	})
	mux.Handle("/connect/{code}/", http.NotFoundHandler())
	mux.Handle("/all-methods/{code}", http.NotFoundHandler())
	for _, tt := range []struct {
		method, target, pattern string
		status                  int
	}{
		{"GET", "/lookup/path-secret?key=query-secret", "GET /lookup/{code}", http.StatusAccepted},
		{"GET", "/missing/path-secret?key=query-secret", "unmatched", http.StatusNotFound},
		{"GET", "/lookup/./path-secret", "GET /lookup/{code}", http.StatusTemporaryRedirect},
		{"CONNECT", "/connect/path-secret", "unmatched", http.StatusTemporaryRedirect},
		{"DELETE", "/all-methods/path-secret", "unmatched", http.StatusNotFound},
	} {
		t.Run(tt.method+tt.target, func(t *testing.T) {
			var logs bytes.Buffer
			h := RequestID(AccessLog(slog.New(slog.NewJSONHandler(&logs, nil)), mux, mux))
			req := httptest.NewRequest(tt.method, tt.target, nil)
			req.Header.Set("X-Request-ID", "caller-secret")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			log := logs.String()
			if strings.Contains(log, "secret") {
				t.Fatalf("access log leaked request input: %s", log)
			}
			id := w.Header().Get("X-Request-ID")
			if decoded, err := hex.DecodeString(id); len(decoded) != 16 || err != nil {
				t.Fatalf("not a minted ID: %q", id)
			}
			if tt.status == http.StatusAccepted && w.Header().Get("X-Handler-ID") != id {
				t.Fatal("handler and response disagree on request ID")
			}
			var event struct {
				Method    string
				Pattern   string
				Status    int
				Duration  *int64
				RequestID string `json:"request_id"`
			}
			dec := json.NewDecoder(&logs)
			if err := dec.Decode(&event); err != nil {
				t.Fatal(err)
			}
			if event.Method != tt.method || event.Pattern != tt.pattern || event.RequestID != id ||
				event.Status != tt.status || event.Duration == nil || *event.Duration < 0 {
				t.Fatalf("unexpected event: %s", log)
			}
			if err := dec.Decode(&event); err != io.EOF {
				t.Fatalf("expected exactly one log event, got %v", err)
			}
		})
	}
}
