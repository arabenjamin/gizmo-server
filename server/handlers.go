package server

import (
	_ "embed"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/hybridgroup/mjpeg"
)

//go:embed gui.html
var guiHTML []byte

type PingResonse struct {
	Message string `json:"message"`
}

/* Boilerplate */
func ping(res http.ResponseWriter, req *http.Request) {

	if req.Method != http.MethodGet {
		http.Error(res, "Invalid request method", http.StatusMethodNotAllowed)
	}
	payload := map[string]interface{}{
		"message": "pong!",
	}

	json_resp, _ := json.Marshal(payload)
	res.Header().Set("Content-Type", "application/json")
	res.Header().Set("Access-Control-Allow-Origin", "*")
	res.WriteHeader(http.StatusOK)
	res.Write(json_resp)
}

// makeUploadHandler returns a handler that reads JPEG frame data from the
// request body and pushes it into the shared mjpeg.Stream.
func makeUploadHandler(stream *mjpeg.Stream) http.HandlerFunc {
	return func(resp http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(resp, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(resp, "Error reading request body", http.StatusBadRequest)
			return
		}
		defer req.Body.Close()

		stream.UpdateJPEG(body)

		resp.WriteHeader(http.StatusOK)
		resp.Write([]byte("POST request received successfully"))
	}
}

// serveGUI serves the embedded HTML control panel.
func serveGUI(resp http.ResponseWriter, req *http.Request) {
	resp.Header().Set("Content-Type", "text/html; charset=utf-8")
	resp.Write(guiHTML)
}

// makeProxyHandler returns a handler that proxies requests to the robot's API.
// It strips the /api/v1/robot prefix and forwards to the robot URL.
func makeProxyHandler(robotURL string, serverlog *log.Logger) http.HandlerFunc {
	client := &http.Client{Timeout: 10 * time.Second}

	return func(resp http.ResponseWriter, req *http.Request) {
		// Strip the proxy prefix
		path := strings.TrimPrefix(req.URL.Path, "/api/v1/robot/")
		if path == "" {
			path = "ping"
		}

		// Map to robot URL: /ping stays as /ping, everything else goes to /api/v1/
		var targetURL string
		if path == "ping" {
			targetURL = robotURL + "/ping"
		} else {
			targetURL = robotURL + "/api/v1/" + path
		}

		serverlog.Printf("PROXY: %s %s -> %s", req.Method, req.URL.Path, targetURL)

		// Create the proxied request
		proxyReq, err := http.NewRequest(req.Method, targetURL, req.Body)
		if err != nil {
			http.Error(resp, "Failed to create proxy request", http.StatusInternalServerError)
			return
		}
		if req.Header.Get("Content-Type") != "" {
			proxyReq.Header.Set("Content-Type", req.Header.Get("Content-Type"))
		}

		proxyResp, err := client.Do(proxyReq)
		if err != nil {
			serverlog.Printf("PROXY: Error forwarding to robot: %v", err)
			http.Error(resp, "Robot unreachable: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer proxyResp.Body.Close()

		// Copy response headers
		for k, v := range proxyResp.Header {
			for _, val := range v {
				resp.Header().Set(k, val)
			}
		}
		resp.Header().Set("Access-Control-Allow-Origin", "*")
		resp.WriteHeader(proxyResp.StatusCode)
		io.Copy(resp, proxyResp.Body)
	}
}
