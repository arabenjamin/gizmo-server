package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/arabenjamin/gizmo-server/behavior"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics is gizmo-server's Prometheus registry: the Go runtime and process,
// the operator's perception, behaviour and control state (read when
// Prometheus scrapes), its calls to the robot's API, and the GUI's viewers.
type Metrics struct {
	registry *prometheus.Registry
	robotAPI *prometheus.HistogramVec
	apiErrs  *prometheus.CounterVec
	viewers  prometheus.Gauge
}

func newMetrics(op *Operator) *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		robotAPI: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "gizmo_server",
			Name:      "robot_api_request_duration_seconds",
			Help:      "Calls to the robot's API by method, path and status code (error: no answer).",
			Buckets:   []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		}, []string{"method", "path", "code"}),
		apiErrs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "gizmo_server",
			Name:      "robot_api_errors_total",
			Help:      "Failed calls to the robot's API: kind=transport (no answer) or status (an HTTP error status).",
		}, []string{"method", "path", "kind"}),
		viewers: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "gizmo_server",
			Name:      "gui_stream_clients",
			Help:      "GUI clients watching the operator's video stream now.",
		}),
	}
	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.robotAPI,
		m.apiErrs,
		m.viewers,
		&operatorCollector{op: op},
	)
	return m
}

// Handler serves the exposition: open, GET and HEAD only.
func (m *Metrics) Handler() http.Handler {
	h := promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.ServeHTTP(w, req)
	})
}

// Viewing counts h's requests in flight as GUI stream clients.
func (m *Metrics) Viewing(h http.Handler) http.Handler {
	return promhttp.InstrumentHandlerInFlight(m.viewers, h)
}

// robotPaths are the robot API paths the operator calls; anything else is
// "other", so a path never makes a series of its own.
var robotPaths = []string{"/api/v1/control", "/api/v1/look"}

func robotPath(p string) string {
	for _, known := range robotPaths {
		if p == known {
			return known
		}
	}
	return "other"
}

// RobotTransport times next's calls to the robot and counts their failures,
// by method and path, never by error message (which would be a series per
// message).
func (m *Metrics) RobotTransport(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return roundTripper(func(req *http.Request) (*http.Response, error) {
		start := time.Now()
		resp, err := next.RoundTrip(req)
		path := robotPath(req.URL.Path)
		code := "error"
		switch {
		case err != nil:
			m.apiErrs.WithLabelValues(req.Method, path, "transport").Inc()
		default:
			code = strconv.Itoa(resp.StatusCode)
			// 409 is the robot saying someone else holds control: an answer,
			// not a failure of its API.
			if resp.StatusCode >= 400 && resp.StatusCode != http.StatusConflict {
				m.apiErrs.WithLabelValues(req.Method, path, "status").Inc()
			}
		}
		m.robotAPI.WithLabelValues(req.Method, path, code).Observe(time.Since(start).Seconds())
		return resp, err
	})
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

var (
	descFrames = prometheus.NewDesc("gizmo_server_frames_total",
		"Robot frames the operator has run face detection on.", nil, nil)
	descFacesSeen = prometheus.NewDesc("gizmo_server_faces_detected_total",
		"Faces detected, summed over frames.", nil, nil)
	descFPS = prometheus.NewDesc("gizmo_server_perception_fps",
		"Frames per second over the last two seconds (0 when frames stop).", nil, nil)
	descFaces = prometheus.NewDesc("gizmo_server_faces",
		"Faces in the latest frame.", nil, nil)
	descDetect = prometheus.NewDesc("gizmo_server_detect_duration_seconds",
		"How long face detection took on the latest frame.", nil, nil)
	descConnected = prometheus.NewDesc("gizmo_server_robot_stream_connected",
		"1 while the robot's video stream is connected, else 0.", nil, nil)
	descBehavior = prometheus.NewDesc("gizmo_server_behavior_enabled",
		"1 while a behaviour is switched on: tracking, or scanning when no one is seen.",
		[]string{"behavior"}, nil)
	descMode = prometheus.NewDesc("gizmo_server_tracker_mode",
		"1 for the tracker's current mode.", []string{"mode"}, nil)
	descHeld = prometheus.NewDesc("gizmo_server_control_held",
		"1 while the operator holds the robot's control lease, else 0.", nil, nil)
)

// trackerModes are every mode, so each has a series (0 or 1) and a query
// for one that isn't current still finds it.
var trackerModes = []behavior.Mode{
	behavior.ModeIdle, behavior.ModeTracking, behavior.ModeScanning, behavior.ModeHolding,
}

// operatorCollector reads the operator when Prometheus scrapes.
type operatorCollector struct{ op *Operator }

func (c *operatorCollector) Describe(ch chan<- *prometheus.Desc) {
	prometheus.DescribeByCollect(c, ch)
}

func (c *operatorCollector) Collect(ch chan<- prometheus.Metric) {
	o := c.op
	o.mu.Lock()
	frames, faces := o.framesSeen, o.facesSeen
	fps := o.fpsLocked(time.Now())
	latest, detectMs := len(o.faces), o.detectMs
	connected, tracking, scan := o.connected, o.tracking, o.scan
	mode, held := o.mode, o.token != ""
	o.mu.Unlock()

	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
	}
	ch <- prometheus.MustNewConstMetric(descFrames, prometheus.CounterValue, float64(frames))
	ch <- prometheus.MustNewConstMetric(descFacesSeen, prometheus.CounterValue, float64(faces))
	gauge(descFPS, fps)
	gauge(descFaces, float64(latest))
	gauge(descDetect, detectMs/1000)
	gauge(descConnected, flag(connected))
	gauge(descBehavior, flag(tracking), "tracking")
	gauge(descBehavior, flag(scan), "scanning")
	for _, m := range trackerModes {
		gauge(descMode, flag(m == mode), string(m))
	}
	gauge(descHeld, flag(held))
}

func flag(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
