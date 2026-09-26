package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/arabenjamin/gizmo-server/robotapi"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// stateHandler: GET /api/v1/state -- everything the GUI shows.
func stateHandler(op *Operator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, op.State(r.Context()))
	}
}

// controlHandler: POST /api/v1/control {"force":bool} takes control for the
// operator; DELETE gives it back.
func controlHandler(op *Operator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var body struct {
				Force bool `json:"force"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if err := op.TakeControl(r.Context(), body.Force); err != nil {
				status := http.StatusBadGateway
				var held *robotapi.ErrControlHeld
				if errors.As(err, &held) {
					status = http.StatusConflict
				}
				writeJSON(w, status, map[string]any{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, op.State(r.Context()))
		case http.MethodDelete:
			if err := op.ReleaseControl(r.Context()); err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, op.State(r.Context()))
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// behaviorHandler: POST /api/v1/behavior {"tracking":bool,"scan":bool}.
func behaviorHandler(op *Operator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Tracking *bool `json:"tracking"`
			Scan     *bool `json:"scan"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		if err := op.SetBehavior(body.Tracking, body.Scan); err != nil {
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, op.State(r.Context()))
	}
}

// activityHandler: POST /api/v1/activity -- the GUI reports operator
// interaction, which keeps a held lease alive.
func activityHandler(op *Operator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		op.Touch()
		w.WriteHeader(http.StatusNoContent)
	}
}

// withControlToken stamps robot-bound requests with the operator's control
// token (when it holds control), so manual commands from the GUI are made as
// the human in control. Any such request also counts as operator activity.
func withControlToken(op *Operator, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del(robotapi.ControlHeader) // never trust a browser-supplied token
		if tok := op.Token(); tok != "" {
			r.Header.Set(robotapi.ControlHeader, tok)
			op.Touch()
		}
		next(w, r)
	}
}
