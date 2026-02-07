package server

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	rec := httptest.NewRecorder()
	ping(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "pong") {
		t.Fatalf("expected pong in body, got %s", rec.Body.String())
	}
}

func TestServeGUI(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	serveGUI(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Fatalf("expected text/html content-type, got %s", ct)
	}
	if !strings.Contains(rec.Body.String(), "Gizmatron Control Panel") {
		t.Fatalf("expected GUI HTML in body")
	}
}

func TestMakeProxyHandler_RobotUnreachable(t *testing.T) {
	// Proxy to a URL that won't respond
	serverlog := log.New(io.Discard, "", 0)
	handler := makeProxyHandler("http://127.0.0.1:1", serverlog)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/robot/ping", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Robot unreachable") {
		t.Fatalf("expected 'Robot unreachable' in body, got %s", rec.Body.String())
	}
}

func TestMakeProxyHandler_ForwardsToRobot(t *testing.T) {
	// Create a fake robot server
	robot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"path":"` + r.URL.Path + `","method":"` + r.Method + `"}`))
	}))
	defer robot.Close()

	serverlog := log.New(io.Discard, "", 0)
	handler := makeProxyHandler(robot.URL, serverlog)

	tests := []struct {
		name         string
		path         string
		expectedPath string
	}{
		{"ping", "/api/v1/robot/ping", "/ping"},
		{"bot-status", "/api/v1/robot/bot-status", "/api/v1/bot-status"},
		{"bot-start", "/api/v1/robot/bot-start", "/api/v1/bot-start"},
		{"start/stream", "/api/v1/robot/start/stream", "/api/v1/start/stream"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			handler(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.expectedPath) {
				t.Fatalf("expected path %s in body, got %s", tc.expectedPath, body)
			}
		})
	}
}

func TestMakeProxyHandler_PostBody(t *testing.T) {
	robot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"received":"` + string(body) + `"}`))
	}))
	defer robot.Close()

	serverlog := log.New(io.Discard, "", 0)
	handler := makeProxyHandler(robot.URL, serverlog)

	bodyStr := `{"enable":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/robot/detectfaces", strings.NewReader(bodyStr))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), bodyStr) {
		t.Fatalf("expected body to be forwarded, got %s", rec.Body.String())
	}
}
