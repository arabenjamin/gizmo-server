package server

import (
	"encoding/json"
	//"net/http"
	"net/http/httptest"
	//"strings"
	"testing"
)

/*Test Ping Endpoint*/
func TestServerPing(t *testing.T) {

	t.Run("Test_returns_200", func(t *testing.T) {

		req := httptest.NewRequest("GET", "/ping", nil)
		res := httptest.NewRecorder()

		ping(res, req)

		resp := res.Result()
		expected := 200

		if resp.StatusCode != expected {
			t.Errorf("Recieved %d, instead got %d", resp.StatusCode, expected)
		}
	})
	t.Run("Test_ensure_method_get", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/ping", nil)
		res := httptest.NewRecorder()

		ping(res, req)

		resp := res.Result()
		defer resp.Body.Close()

		expected := 405

		if resp.StatusCode != expected {
			t.Errorf("Expected %d, instead got %d", expected, resp.StatusCode)
		}

	})
	t.Run("Test_returns_json_pong", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/ping", nil)
		res := httptest.NewRecorder()

		ping(res, req)

		resp := res.Result()
		defer resp.Body.Close()

		var data PingResonse

		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			t.Error(err)
		}

		if data.Message != "pong!" {
			t.Errorf("Expected pong, got %v", data.Message)
		}
	})
}
