package server

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/hybridgroup/mjpeg"
)

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
