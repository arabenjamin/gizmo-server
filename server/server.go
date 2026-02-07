package server

import (
	"log"
	"net/http"

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

func Start(serverlog *log.Logger, robotURL string) error {

	stream := mjpeg.NewStream()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ping", Chain(ping, logger(serverlog)))
	mux.HandleFunc("/api/v1/upload", makeUploadHandler(stream))
	mux.Handle("/api/v1/stream", stream)

	// GUI and robot proxy routes
	mux.HandleFunc("/api/v1/robot/", makeProxyHandler(robotURL, serverlog))
	mux.HandleFunc("/", serveGUI)

	serverlog.Printf("Robot proxy target: %s", robotURL)
	err := http.ListenAndServe(":9090", mux)
	if err != nil {

		return err
	}
	return nil
}
