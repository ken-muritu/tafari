package main

import (
	"database/sql"
	"encoding/binary"
	"strings"
	"testing"
)

func TestTagged(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"@tafari what time is it", "what time is it", true},
		{"Hey @Tafari can you help", "Hey  can you help", true},
		{"tafari, draft a reply", "draft a reply", true},
		{"Tafari: hello", "hello", true},
		{"just chatting about safari", "just chatting about safari", false},
	}
	for _, c := range cases {
		got, ok := tagged(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("tagged(%q) = %q,%v; want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestOggSeconds(t *testing.T) {
	page := func(granule uint64) []byte {
		b := make([]byte, 27)
		copy(b, "OggS")
		binary.LittleEndian.PutUint64(b[6:14], granule)
		return b
	}
	data := append(page(48000), page(48000*7+100)...)
	if got := oggSeconds(data); got != 7 {
		t.Fatalf("oggSeconds = %d, want 7", got)
	}
	if oggSeconds([]byte("nope")) != 0 {
		t.Fatal("non-ogg should be 0")
	}
}

func TestSystemPromptCarriesKnowledgeAndRules(t *testing.T) {
	p := systemPrompt(false)
	for _, must := range []string{"You are **Tafari**", "Never pretend to be Ken", "22,000 / month", "registration is still pending", "someone other than Ken"} {
		if !strings.Contains(p, must) {
			t.Errorf("system prompt missing %q", must)
		}
	}
	if !strings.Contains(systemPrompt(true), "Ken himself") {
		t.Error("Ken's own chat should be flagged in the prompt")
	}
}

func TestRateLimit(t *testing.T) {
	chat := "test@s.whatsapp.net"
	for i := 0; i < 12; i++ {
		if !underLimit(chat) {
			t.Fatalf("call %d should pass", i+1)
		}
	}
	if underLimit(chat) {
		t.Fatal("13th call within an hour should be limited")
	}
}

func TestVoicePreferencePerChat(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	if err := initMemory(db); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TAFARI_VOICE_ID", "female-id")
	t.Setenv("TAFARI_VOICE_ID_MALE", "male-id")
	if id, male := voiceFor("a"); id != "female-id" || male {
		t.Fatalf("default should be female, got %s %v", id, male)
	}
	setPrefMale("a", true)
	if id, male := voiceFor("a"); id != "male-id" || !male {
		t.Fatalf("chat a should be male, got %s %v", id, male)
	}
	if id, _ := voiceFor("b"); id != "female-id" {
		t.Fatal("other chats keep the default")
	}
	setPrefMale("a", false)
	if _, male := voiceFor("a"); male {
		t.Fatal("switch back failed")
	}
}
