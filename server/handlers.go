package server

import (

  "log"
  "encoding/json"
  "net/http"
) 


/* Boilerplate */
func ping(resp http.Response, req *http.Request){

  payload := map[string]interface{} {
    "message": "pong!"
  }

  json_payload, _ := json.Marshall(payload)
  res.Header().Set("Content-Type", "application/json")
	res.Header().Set("Access-Control-Allow-Origin", "*")
	res.WriteHeader(http.StatusOK)
	res.Write(json_resp)
}
