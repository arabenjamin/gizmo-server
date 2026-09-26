package server

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arabenjamin/gizmo-server/perception"
	"github.com/arabenjamin/gizmo-server/robotapi"
	"github.com/hybridgroup/mjpeg"
)

// fakeRobot records look commands and can revoke the operator's control.
type fakeRobot struct {
	mu      sync.Mutex
	looks   []map[string]any
	tokens  []string
	revoked bool
}

func (f *fakeRobot) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/control", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.Write([]byte(`{"status":"ok","token":"op-token"}`))
		case http.MethodGet:
			w.Write([]byte(`{"control":{"held":true,"holder":"gizmo-gui","class":"human"}}`))
		}
	})
	mux.HandleFunc("/api/v1/look", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodGet {
			w.Write([]byte(`{"limits":{"pan_min":-90,"pan_max":90,"tilt_min":-130,"tilt_max":50}}`))
			return
		}
		if f.revoked {
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":"you no longer have control: control was taken over by tab 2 (human)"}`))
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.looks = append(f.looks, body)
		f.tokens = append(f.tokens, r.Header.Get(robotapi.ControlHeader))
		w.Write([]byte(`{"status":"ok"}`))
	})
	return mux
}

func (f *fakeRobot) lookCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.looks)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOperatorTracksAFaceAndStopsWhenControlIsLost(t *testing.T) {
	fake := &fakeRobot{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	det, err := perception.NewDetector(perception.DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	op := NewOperator(robotapi.New(srv.URL), det, mjpeg.NewStream(), log.New(io.Discard, "", 0))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go op.commandLoop(ctx)

	// Behaviours need control.
	on := true
	if err := op.SetBehavior(&on, nil); err != errNoControl {
		t.Fatalf("tracking without control: got %v, want errNoControl", err)
	}
	if err := op.TakeControl(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := op.SetBehavior(&on, nil); err != nil {
		t.Fatal(err)
	}

	// Pigo's sample portrait: the face sits LEFT of a 640-wide frame's centre,
	// so the head must pan POSITIVE (measured PanSign -1).
	face, _ := os.ReadFile("../perception/testdata/sample.jpg")
	op.onFrame(robotapi.Frame{JPEG: face, Seq: 1, CapturedAt: time.Now()})
	waitFor(t, "a look command", func() bool { return fake.lookCount() > 0 })

	fake.mu.Lock()
	first, tok := fake.looks[0], fake.tokens[0]
	fake.mu.Unlock()
	if tok != "op-token" {
		t.Errorf("look sent with token %q, want the operator's", tok)
	}
	if pan := first["pan"].(float64); pan <= 0 || pan > 8 {
		t.Errorf("first pan %.1f, want a positive step capped at 8", pan)
	}

	// Someone else takes control: the next look is refused and tracking stops.
	fake.mu.Lock()
	fake.revoked = true
	fake.mu.Unlock()
	op.onFrame(robotapi.Frame{JPEG: face, Seq: 2, CapturedAt: time.Now()})
	waitFor(t, "control loss", func() bool { return op.Token() == "" })

	st := op.State(ctx)
	if st["tracking"] != false || !strings.Contains(st["lost_reason"].(string), "tab 2") {
		t.Errorf("after losing control: tracking=%v lost_reason=%q", st["tracking"], st["lost_reason"])
	}
}

func TestProxyStampsOperatorToken(t *testing.T) {
	var got string
	robot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(robotapi.ControlHeader)
		w.Write([]byte(`{}`))
	}))
	defer robot.Close()
	op := NewOperator(robotapi.New(robot.URL), nil, mjpeg.NewStream(), log.New(io.Discard, "", 0))
	h := withControlToken(op, makeProxyHandler(robot.URL, log.New(io.Discard, "", 0)))

	// A browser-supplied token is never forwarded.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/robot/look", strings.NewReader(`{}`))
	req.Header.Set(robotapi.ControlHeader, "forged")
	h(httptest.NewRecorder(), req)
	if got != "" {
		t.Errorf("without control, forwarded token %q; want none", got)
	}

	op.mu.Lock()
	op.token = "op-token"
	op.mu.Unlock()
	h(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/robot/look", strings.NewReader(`{}`)))
	if got != "op-token" {
		t.Errorf("with control, forwarded token %q; want op-token", got)
	}
}
