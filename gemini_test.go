package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// "gemini:" models go to GEMINI_BASE_URL with GEMINI_API_KEY and the bare model
// name; other models keep using the default provider and key.
func TestGeminiPrefixRoutesToGeminiProvider(t *testing.T) {
	db, err := sql.Open("sqlite", "file:geminitest?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	if err := initMemory(db); err != nil {
		t.Fatal(err)
	}
	var gotAuth, gotBody string
	gem := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotBody = req.Model
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"from gemini"}}]}`))
	}))
	defer gem.Close()
	t.Setenv("AGENT_API_KEY", "or-key")
	t.Setenv("AGENT_FORMAT", "openai")
	t.Setenv("AGENT_BASE_URL", "http://127.0.0.1:1/unused")
	t.Setenv("GEMINI_API_KEY", "gem-key")
	t.Setenv("GEMINI_BASE_URL", gem.URL)
	t.Setenv("AGENT_MODEL", "gemini:gemini-3.5-flash-lite,other/model:free")

	got, err := ask("chat-gemini", "hi", false)
	if err != nil || got != "from gemini" {
		t.Fatalf("got %q, err %v", got, err)
	}
	if gotAuth != "Bearer gem-key" {
		t.Fatalf("auth header %q", gotAuth)
	}
	if gotBody != "gemini-3.5-flash-lite" {
		t.Fatalf("request should carry the bare model name, got %q", gotBody)
	}
}
