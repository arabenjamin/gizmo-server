package server

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arabenjamin/gizmo-server/behavior"
	"github.com/arabenjamin/gizmo-server/perception"
	"github.com/arabenjamin/gizmo-server/robotapi"
	"github.com/hybridgroup/mjpeg"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func testOperator(robotURL string) *Operator {
	return NewOperator(robotapi.New(robotURL), nil, mjpeg.NewStream(), log.New(io.Discard, "", 0))
}

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics: %d %s", rec.Code, rec.Body)
	}
	if dir := os.Getenv("GIZMO_METRICS_OUT"); dir != "" {
		// For promtool: GIZMO_METRICS_OUT=dir go test -run Metrics ./server
		_ = os.WriteFile(filepath.Join(dir, "metrics.prom"), rec.Body.Bytes(), 0o644)
	}
	return rec.Body.String()
}

func TestMetricsReportTheOperator(t *testing.T) {
	op := testOperator("http://127.0.0.1:1")
	m := newMetrics(op)
	now := time.Now()
	op.mu.Lock()
	op.framesSeen, op.facesSeen = 12, 5
	op.faces = []perception.Face{{X: 1, Y: 2, Size: 30}, {X: 5, Y: 6, Size: 40}}
	op.detectMs = 25
	op.frameTimes = []time.Time{now.Add(-time.Second), now.Add(-500 * time.Millisecond), now}
	op.connected, op.tracking = true, true
	op.mode, op.token = behavior.ModeTracking, "lease"
	op.mu.Unlock()

	body := scrape(t, m)
	for _, want := range []string{
		"go_goroutines ",
		"gizmo_server_frames_total 12",
		"gizmo_server_faces_detected_total 5",
		"gizmo_server_perception_fps 2",
		"gizmo_server_faces 2",
		"gizmo_server_detect_duration_seconds 0.025",
		"gizmo_server_robot_stream_connected 1",
		`gizmo_server_behavior_enabled{behavior="tracking"} 1`,
		`gizmo_server_behavior_enabled{behavior="scanning"} 0`,
		`gizmo_server_tracker_mode{mode="tracking"} 1`,
		`gizmo_server_tracker_mode{mode="idle"} 0`,
		"gizmo_server_control_held 1",
		"gizmo_server_gui_stream_clients 0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}
}

// A fresh operator scrapes cleanly: nothing seen, nothing held.
func TestMetricsBeforeAnyFrame(t *testing.T) {
	body := scrape(t, newMetrics(testOperator("http://127.0.0.1:1")))
	for _, want := range []string{
		"gizmo_server_frames_total 0",
		"gizmo_server_perception_fps 0",
		"gizmo_server_control_held 0",
		`gizmo_server_tracker_mode{mode="idle"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}
}

func TestMetricsAreGetOnly(t *testing.T) {
	m := newMetrics(testOperator("http://127.0.0.1:1"))
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /metrics: %d, want 405", rec.Code)
	}
}

// Robot API calls are timed by method and path; failures are counted by
// kind, and a 409 (someone else holds control) is an answer, not one.
func TestRobotAPICallsAreTimedAndFailuresCounted(t *testing.T) {
	robot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/control" && r.Method == http.MethodGet:
			w.Write([]byte(`{"control":{"held":false}}`))
		case r.URL.Path == "/api/v1/control":
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":"held by tab 2"}`))
		case r.URL.Path == "/api/v1/look":
			http.Error(w, "servo fault", http.StatusInternalServerError)
		}
	}))
	client := robotapi.New(robot.URL)
	m := newMetrics(testOperator(robot.URL))
	client.HTTP.Transport = m.RobotTransport(client.HTTP.Transport)
	ctx := context.Background()

	if _, err := client.Control(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Acquire(ctx, "", "gizmo-gui", "human", false); err == nil {
		t.Fatal("acquire: want the 409")
	}
	if err := client.Look(ctx, "tok", 0, 0, 10); err == nil {
		t.Fatal("look: want the 500")
	}
	robot.Close()
	if _, err := client.Control(ctx); err == nil {
		t.Fatal("control: want a transport error")
	}

	if got := testutil.ToFloat64(m.apiErrs.WithLabelValues("POST", "/api/v1/look", "status")); got != 1 {
		t.Errorf("look status errors = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.apiErrs.WithLabelValues("GET", "/api/v1/control", "transport")); got != 1 {
		t.Errorf("control transport errors = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.apiErrs.WithLabelValues("POST", "/api/v1/control", "status")); got != 0 {
		t.Errorf("a 409 counted as an error: %v", got)
	}
	body := scrape(t, m)
	for _, want := range []string{
		`gizmo_server_robot_api_request_duration_seconds_count{code="200",method="GET",path="/api/v1/control"} 1`,
		`gizmo_server_robot_api_request_duration_seconds_count{code="409",method="POST",path="/api/v1/control"} 1`,
		`gizmo_server_robot_api_request_duration_seconds_count{code="500",method="POST",path="/api/v1/look"} 1`,
		`gizmo_server_robot_api_request_duration_seconds_count{code="error",method="GET",path="/api/v1/control"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}
	if strings.Contains(body, "servo fault") || strings.Contains(body, "tab 2") {
		t.Error("an error message became a label")
	}
}

func TestRobotPathsAreBounded(t *testing.T) {
	for in, want := range map[string]string{
		"/api/v1/look":      "/api/v1/look",
		"/api/v1/control":   "/api/v1/control",
		"/api/v1/whatever":  "other",
		"/api/v1/look/9999": "other",
	} {
		if got := robotPath(in); got != want {
			t.Errorf("robotPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGUIStreamClientsAreCounted(t *testing.T) {
	m := newMetrics(testOperator("http://127.0.0.1:1"))
	inside, release := make(chan struct{}), make(chan struct{})
	h := m.Viewing(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(inside)
		<-release
	}))
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/stream", nil))
		close(done)
	}()
	<-inside
	if got := testutil.ToFloat64(m.viewers); got != 1 {
		t.Errorf("while watching: %v, want 1", got)
	}
	close(release)
	<-done
	if got := testutil.ToFloat64(m.viewers); got != 0 {
		t.Errorf("after: %v, want 0", got)
	}
}

// The operator's metrics follow Prometheus' naming rules (what promtool checks).
func TestMetricsLint(t *testing.T) {
	problems, err := testutil.CollectAndLint(&operatorCollector{op: testOperator("http://127.0.0.1:1")})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Errorf("%s: %s", p.Metric, p.Text)
	}
}

func TestFPSDropsWhenFramesStop(t *testing.T) {
	op := testOperator("http://127.0.0.1:1")
	t0 := time.Unix(1_000, 0)
	op.frameTimes = []time.Time{t0, t0.Add(500 * time.Millisecond), t0.Add(time.Second)}
	if got := op.fpsLocked(t0.Add(time.Second)); got != 2 {
		t.Errorf("3 frames over 1s: %v, want 2", got)
	}
	if got := op.fpsLocked(t0.Add(10 * time.Second)); got != 0 {
		t.Errorf("long after the last frame: %v, want 0", got)
	}
}
