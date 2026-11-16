package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A rate-limited model and an empty-text model must be skipped; the third answers.
func TestAskFallsBackAcrossModels(t *testing.T) {
	db, err := sql.Open("sqlite", "file:fallback?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	if err := initMemory(db); err != nil {
		t.Fatal(err)
	}
	var tried []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		tried = append(tried, req.Model)
		switch req.Model {
		case "limited":
			w.WriteHeader(http.StatusTooManyRequests)
		case "empty":
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":""}}]}`))
		default:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Tafari here, an AI."}}]}`))
		}
	}))
	defer srv.Close()
	t.Setenv("AGENT_API_KEY", "test")
	t.Setenv("AGENT_FORMAT", "openai")
	t.Setenv("AGENT_BASE_URL", srv.URL)
	t.Setenv("AGENT_MODEL", "limited, empty ,good")

	got, err := ask("chat-fallback", "hi", false)
	if err != nil || got != "Tafari here, an AI." {
		t.Fatalf("got %q, err %v", got, err)
	}
	if len(tried) != 3 || tried[0] != "limited" || tried[1] != "empty" || tried[2] != "good" {
		t.Fatalf("tried %v", tried)
	}

	t.Setenv("AGENT_MODEL", "limited")
	if _, err := ask("chat-fallback", "hi", false); err == nil {
		t.Fatal("expected an error when every model fails")
	}
}
