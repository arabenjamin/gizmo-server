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

func serveGUI(resp http.ResponseWriter, req *http.Request) {
	resp.Header().Set("Content-Type", "text/html; charset=utf-8")
	resp.Write(guiHTML)
}

func makeProxyHandler(targetURLBase string, serverlog *log.Logger) http.HandlerFunc {
	client := &http.Client{Timeout: 10 * time.Second}

	return func(resp http.ResponseWriter, req *http.Request) {
		var targetURL string
		if strings.HasPrefix(req.URL.Path, "/api/v1/brain/") {
			path := strings.TrimPrefix(req.URL.Path, "/api/v1/brain/")
			targetURL = targetURLBase + "/" + path
		} else {
			path := strings.TrimPrefix(req.URL.Path, "/api/v1/robot/")
			if path == "" {
				path = "ping"
			}
			if path == "ping" {
				targetURL = targetURLBase + "/ping"
			} else {
				targetURL = targetURLBase + "/api/v1/" + path
			}
		}

		serverlog.Printf("PROXY: %s %s -> %s", req.Method, req.URL.Path, targetURL)

		proxyReq, err := http.NewRequest(req.Method, targetURL, req.Body)
		if err != nil {
			http.Error(resp, "Failed to create proxy request", http.StatusInternalServerError)
			return
		}
		if ct := req.Header.Get("Content-Type"); ct != "" {
			proxyReq.Header.Set("Content-Type", ct)
		}
		if v := req.Header.Get("mcp-protocol-version"); v != "" {
			proxyReq.Header.Set("mcp-protocol-version", v)
		}
		if s := req.Header.Get("mcp-session-id"); s != "" {
			proxyReq.Header.Set("mcp-session-id", s)
		}
		if tok := req.Header.Get("X-Gizmatron-Control"); tok != "" {
			proxyReq.Header.Set("X-Gizmatron-Control", tok)
		}

		proxyResp, err := client.Do(proxyReq)
		if err != nil {
			serverlog.Printf("PROXY: Error forwarding: %v", err)
			http.Error(resp, "Target unreachable: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer proxyResp.Body.Close()

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
