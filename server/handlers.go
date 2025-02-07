package server

import (
	"encoding/json"
	"net/http"
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
