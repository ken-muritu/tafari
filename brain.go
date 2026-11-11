package main

// brain.go: Tafari's persona, per-chat memory and the LLM call.
// The persona and knowledge live in knowledge/*.md (embedded at build time), so facts are
// reviewed in git instead of hidden in code.

import (
	"bytes"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

//go:embed knowledge/*.md
var knowledgeFS embed.FS

// memDB holds Tafari's own tables (conversation memory); it is the same SQLite file as the session.
var memDB *sql.DB

const memoryTurns = 12 // past messages per chat sent to the model

func systemPrompt(isKen bool) string {
	entries, _ := knowledgeFS.ReadDir("knowledge")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names) // ken.md, persona.md, services.md: stable order
	var sb strings.Builder
	for _, n := range names {
		b, _ := knowledgeFS.ReadFile("knowledge/" + n)
		sb.Write(b)
		sb.WriteString("\n\n")
	}
	now := time.Now().In(nairobi()).Format("Monday 2 January 2006, 15:04")
	sb.WriteString("Current time in Nairobi: " + now + ".\n")
	if isKen {
		sb.WriteString("You are talking to Ken himself, in his private chat with you.\n")
	} else {
		sb.WriteString("You are talking to someone other than Ken, who tagged you in a WhatsApp chat. Introduce yourself as Ken's AI assistant on first contact.\n")
	}
	return sb.String()
}

func nairobi() *time.Location {
	if loc, err := time.LoadLocation("Africa/Nairobi"); err == nil {
		return loc
	}
	return time.FixedZone("EAT", 3*3600)
}

func initMemory(db *sql.DB) error {
	memDB = db
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS tafari_turns (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		chat TEXT NOT NULL, role TEXT NOT NULL, content TEXT NOT NULL, ts INTEGER NOT NULL)`)
	if err == nil {
		_, err = db.Exec(`CREATE INDEX IF NOT EXISTS tafari_turns_chat ON tafari_turns(chat, id)`)
	}
	return err
}

type turn struct{ Role, Content string }

func history(chat string) []turn {
	rows, err := memDB.Query(`SELECT role, content FROM tafari_turns WHERE chat = ? ORDER BY id DESC LIMIT ?`, chat, memoryTurns)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []turn
	for rows.Next() {
		var t turn
		if rows.Scan(&t.Role, &t.Content) == nil {
			out = append([]turn{t}, out...)
		}
	}
	return out
}

func remember(chat, role, content string) {
	_, _ = memDB.Exec(`INSERT INTO tafari_turns(chat, role, content, ts) VALUES (?,?,?,?)`, chat, role, content, time.Now().Unix())
	// Keep the table small: 200 rows per chat at most.
	_, _ = memDB.Exec(`DELETE FROM tafari_turns WHERE chat = ? AND id NOT IN (SELECT id FROM tafari_turns WHERE chat = ? ORDER BY id DESC LIMIT 200)`, chat, chat)
}

func forget(chat string) { _, _ = memDB.Exec(`DELETE FROM tafari_turns WHERE chat = ?`, chat) }

// ask sends persona + recent history + the new message to the configured model.
// Content is never logged; only sizes and status.
func ask(chat, userText string, isKen bool) (string, error) {
	apiKey := agentKey()
	if apiKey == "" {
		return "", fmt.Errorf("no AI key set (AGENT_API_KEY)")
	}
	format, base, model := agentFormat(), agentBaseURL(), agentModel()
	sys := systemPrompt(isKen)
	msgs := []map[string]string{}
	for _, t := range history(chat) {
		msgs = append(msgs, map[string]string{"role": t.Role, "content": t.Content})
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": userText})

	var payload []byte
	if format == "openai" {
		all := append([]map[string]string{{"role": "system", "content": sys}}, msgs...)
		payload, _ = json.Marshal(map[string]any{"model": model, "messages": all, "max_tokens": 700, "temperature": 0.5})
	} else {
		payload, _ = json.Marshal(map[string]any{"model": model, "system": sys, "messages": msgs, "max_tokens": 700})
	}
	req, err := http.NewRequest("POST", base, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	if format == "openai" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	} else {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("Authorization", "Bearer "+apiKey) // AgentRouter-style proxies
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	logf("[brain] model=%s status=%d in=%dB out=%dB", model, resp.StatusCode, len(payload), len(body))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("AI service returned HTTP %d", resp.StatusCode)
	}
	var text string
	if format == "openai" {
		var out struct {
			Choices []struct {
				Message struct{ Content string } `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal(body, &out) == nil && len(out.Choices) > 0 {
			text = out.Choices[0].Message.Content
		}
	} else {
		var out struct {
			Content []struct{ Type, Text string } `json:"content"`
		}
		if json.Unmarshal(body, &out) == nil {
			for _, c := range out.Content {
				if c.Type == "text" {
					text += c.Text
				}
			}
		}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("AI service returned no text")
	}
	remember(chat, "user", userText)
	remember(chat, "assistant", text)
	return text, nil
}
