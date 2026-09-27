package server

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/arabenjamin/gizmo-server/perception"
	"github.com/arabenjamin/gizmo-server/robotapi"
	"github.com/hybridgroup/mjpeg"
)

type Middleware func(http.HandlerFunc) http.HandlerFunc

func logger(serverlog *log.Logger) Middleware {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(resp http.ResponseWriter, req *http.Request) {
			defer func() {
				serverlog.Printf("[%v] [%v] [%v %v] %v\n", req.RemoteAddr, req.Method, req.Proto, req.URL.Path, req.Header["User-Agent"])
			}()
			next(resp, req)
		}
	}
}

func Chain(f http.HandlerFunc, middlewares ...Middleware) http.HandlerFunc {
	for _, middleware := range middlewares {
		f = middleware(f)
	}
	return f
}

func Start(serverlog *log.Logger, robotURL string, brainURL string) error {
	stream := mjpeg.NewStream()

	det, err := perception.NewDetector(perception.DefaultParams)
	if err != nil {
		return err
	}
	// On SIGTERM/SIGINT (docker stop, Ctrl-C) give control back before
	// exiting. Otherwise the robot keeps our human lease for its idle timeout,
	// and a restarted gizmo-server -- a different controller as far as the
	// robot knows -- is locked out until it lapses.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	op := NewOperator(robotapi.New(robotURL), det, stream, serverlog)
	op.Run(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ping", Chain(ping, logger(serverlog)))
	mux.HandleFunc("/api/v1/upload", makeUploadHandler(stream))
	mux.Handle("/api/v1/stream", stream)

	mux.HandleFunc("/api/v1/state", stateHandler(op))
	mux.HandleFunc("/api/v1/control", Chain(controlHandler(op), logger(serverlog)))
	mux.HandleFunc("/api/v1/behavior", Chain(behaviorHandler(op), logger(serverlog)))
	mux.HandleFunc("/api/v1/activity", activityHandler(op))

	mux.HandleFunc("/api/v1/robot/", withControlToken(op, makeProxyHandler(robotURL, serverlog)))
	mux.HandleFunc("/api/v1/brain/", makeProxyHandler(brainURL, serverlog))
	mux.HandleFunc("/", serveGUI)

	serverlog.Printf("Robot: %s  Brain: %s", robotURL, brainURL)
	srv := &http.Server{Addr: ":9090", Handler: mux}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	serverlog.Printf("Shutting down: releasing control of the robot")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := op.ReleaseControl(shutdown); err != nil {
		serverlog.Printf("Release on shutdown failed: %v", err)
	}
	return srv.Shutdown(shutdown)
}
