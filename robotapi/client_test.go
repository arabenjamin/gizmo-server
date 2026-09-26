package robotapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeRobot serves an MJPEG stream shaped exactly like gizmatron's.
func fakeRobot(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/video", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=gizmatronframe")
		for i, pan := range []string{"-3.50", "0.00"} {
			jpeg := []byte{0xff, 0xd8, byte(i)}
			fmt.Fprintf(w, "--gizmatronframe\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n"+
				"X-Frame-Seq: %d\r\nX-Captured-At: 2026-09-26T12:00:0%d.5Z\r\nX-Head-Pan: %s\r\n"+
				"X-Head-Tilt: 12.25\r\nX-Head-Moving: %v\r\n\r\n", len(jpeg), i+7, i, pan, i == 0)
			w.Write(jpeg)
			w.Write([]byte("\r\n"))
		}
	})
	mux.HandleFunc("/api/v1/control", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(ControlHeader) == "stale" {
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"status":"error","error":"the robot is being controlled by a human, tab 2"}`))
			return
		}
		w.Write([]byte(`{"status":"ok","token":"tok123","control":{"held":true}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestStreamFramesParsesTelemetry(t *testing.T) {
	srv := fakeRobot(t)
	var got []Frame
	if err := New(srv.URL).StreamFrames(context.Background(), func(f Frame) { got = append(got, f) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d frames, want 2", len(got))
	}
	f := got[0]
	want := time.Date(2026, 9, 26, 12, 0, 0, 5e8, time.UTC)
	if f.Seq != 7 || f.Pan != -3.5 || f.Tilt != 12.25 || !f.Moving || !f.CapturedAt.Equal(want) {
		t.Errorf("frame 0 = %+v", f)
	}
	if string(f.JPEG) != "\xff\xd8\x00" {
		t.Errorf("jpeg bytes = %q", f.JPEG)
	}
	if got[1].Seq != 8 || got[1].Moving {
		t.Errorf("frame 1 = %+v", got[1])
	}
}

func TestStreamFramesCameraOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if err := New(srv.URL).StreamFrames(context.Background(), func(Frame) {}); !errors.Is(err, ErrCameraOff) {
		t.Errorf("got %v, want ErrCameraOff", err)
	}
}

func TestAcquireAndHeld(t *testing.T) {
	c := New(fakeRobot(t).URL)
	tok, err := c.Acquire(context.Background(), "", "gizmo-gui", "human", false)
	if err != nil || tok != "tok123" {
		t.Fatalf("acquire: %q %v", tok, err)
	}
	_, err = c.Acquire(context.Background(), "stale", "gizmo-gui", "human", false)
	var held *ErrControlHeld
	if !errors.As(err, &held) || held.Message == "" {
		t.Errorf("renew of lost lease: got %v, want ErrControlHeld", err)
	}
}
