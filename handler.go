package main

// handler.go: when Tafari speaks, and to whom.
//
//   - Ken's own "Message yourself" chat: everything Ken writes or records goes to Tafari.
//   - Any other chat: Tafari answers only when a message mentions "@tafari" (or starts with
//     "tafari,"/"tafari:"), and only for senders allowed by TAFARI_ALLOW.
//   - Voice notes are transcribed in Ken's chat, or elsewhere when Ken's caption/next text tags her.
//   - Replies are text; Tafari answers with a voice note when the incoming message was a voice
//     note, or when the text starts with "@tafari voice".
//   - Message contents are never written to logs.

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

const signature = "Tafari (Ken's AI assistant): "

var quiet = os.Getenv("TAFARI_DEBUG") == "" // logf prints only with TAFARI_DEBUG=1

func logf(f string, a ...any) {
	if !quiet {
		log.Printf(f, a...)
	}
}

// ---- who is who -------------------------------------------------------------------------

func isSelfChat(m *events.Message) bool {
	if cli.Store.ID == nil {
		return false
	}
	c := m.Info.Chat
	if c.User == cli.Store.ID.User {
		return true
	}
	return !cli.Store.LID.IsEmpty() && c.User == cli.Store.LID.User
}

// allowed: TAFARI_ALLOW = "all" (default: anyone who tags her), "ken" (only Ken),
// or a comma list of phone numbers (digits, international format).
func allowed(m *events.Message) bool {
	if m.Info.IsFromMe {
		return true
	}
	rule := strings.ToLower(strings.TrimSpace(os.Getenv("TAFARI_ALLOW")))
	switch rule {
	case "", "all":
		return true
	case "ken":
		return false
	}
	sender := m.Info.Sender.User
	if !m.Info.SenderAlt.IsEmpty() && m.Info.SenderAlt.Server == types.DefaultUserServer {
		sender = m.Info.SenderAlt.User // LID-addressed chats carry the phone JID here
	}
	for _, p := range strings.Split(rule, ",") {
		if strings.Trim(strings.TrimSpace(p), "+") == sender {
			return true
		}
	}
	return false
}

// ---- rate limit: N replies per chat per hour (Ken's own chat is unlimited) ----------------

var (
	rlMu   sync.Mutex
	rlSeen = map[string][]time.Time{}
)

func underLimit(chat string) bool {
	limit := 12
	rlMu.Lock()
	defer rlMu.Unlock()
	cut := time.Now().Add(-time.Hour)
	keep := rlSeen[chat][:0]
	for _, t := range rlSeen[chat] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	if len(keep) >= limit {
		rlSeen[chat] = keep
		return false
	}
	rlSeen[chat] = append(keep, time.Now())
	return true
}

// ---- message handling ------------------------------------------------------------------

func messageText(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if t := msg.GetConversation(); t != "" {
		return t
	}
	if t := msg.GetExtendedTextMessage().GetText(); t != "" {
		return t
	}
	return ""
}

func tagged(text string) (string, bool) {
	l := strings.ToLower(text)
	for _, tag := range []string{"@tafari", "tafari,", "tafari:"} {
		if i := strings.Index(l, tag); i >= 0 {
			return strings.TrimSpace(text[:i] + text[i+len(tag):]), true
		}
	}
	return text, false
}

func handleMessage(m *events.Message) {
	if m.Message == nil {
		return
	}
	self := isSelfChat(m)
	text := messageText(m.Message)
	audio := m.Message.GetAudioMessage()

	// Ignore Tafari's own replies (they carry the signature or are her voice notes in Ken's chat).
	if m.Info.IsFromMe && strings.HasPrefix(text, signature) {
		return
	}
	if self && audio != nil && sentByTafari(m.Info.ID) {
		return
	}

	chat := m.Info.Chat.String()
	if !self {
		var ok bool
		if text, ok = tagged(text); !ok || !allowed(m) {
			return
		}
		if !underLimit(chat) {
			return
		}
	}

	lower := strings.ToLower(strings.TrimSpace(text))
	switch lower {
	case "ping":
		reply(m.Info.Chat, "pong")
		return
	case "male voice", "voice male", "use male voice":
		if maleVoice() == "" {
			reply(m.Info.Chat, signature+"The male voice isn't set up yet.")
			return
		}
		setPrefMale(chat, true)
		reply(m.Info.Chat, signature+"Okay, I'll use the male voice for voice notes in this chat.")
		return
	case "female voice", "voice female", "use female voice":
		setPrefMale(chat, false)
		reply(m.Info.Chat, signature+"Okay, back to my usual voice.")
		return
	case "reset", "forget", "start over":
		forget(chat)
		reply(m.Info.Chat, signature+"Done, I've cleared our conversation.")
		return
	case "", "hi", "hello":
		if audio == nil {
			if !self {
				reply(m.Info.Chat, signature+"Hi! I'm Tafari, Ken's AI assistant. How can I help?")
			} else if lower != "" {
				reply(m.Info.Chat, signature+"Hi Ken. What do you need?")
			}
			return
		}
	}
	wantVoice := audio != nil
	if strings.HasPrefix(lower, "voice ") {
		wantVoice, text = true, strings.TrimSpace(text[6:])
	}

	go func() {
		ctx := context.Background()
		if audio != nil {
			if !self { // in other chats voice notes are only read when Ken tags Tafari himself
				return
			}
			data, err := cli.Download(ctx, audio)
			if err != nil {
				reply(m.Info.Chat, signature+"I couldn't open that voice note.")
				return
			}
			heard, err := transcribe(data)
			if err != nil || heard == "" {
				reply(m.Info.Chat, signature+"I couldn't make out that voice note.")
				return
			}
			text = heard
		}
		_ = cli.SendChatPresence(ctx, m.Info.Chat, types.ChatPresenceComposing, types.ChatPresenceMediaText)
		answer, err := ask(chat, text, self)
		_ = cli.SendChatPresence(ctx, m.Info.Chat, types.ChatPresencePaused, types.ChatPresenceMediaText)
		if err != nil {
			log.Printf("brain error: %v", err)
			reply(m.Info.Chat, signature+"I can't reach my brain right now. I'll be back shortly.")
			return
		}
		if wantVoice && voiceEnabled() {
			vid, male := voiceFor(chat)
			spoken := answer
			if male { // Ken's own cloned voice: always say it is the assistant, never pass as Ken
				spoken = "Tafari here, Ken's AI assistant. " + answer
			}
			if ogg, err := speak(spoken, vid); err == nil {
				if err := sendVoiceNoteTracked(ctx, m.Info.Chat, ogg); err == nil {
					return
				}
			}
		}
		reply(m.Info.Chat, signature+answer)
	}()
}

// Track Tafari's own voice-note message IDs so she doesn't answer herself in Ken's chat.
var (
	sentMu  sync.Mutex
	sentIDs = map[string]time.Time{}
)

func sentByTafari(id string) bool {
	sentMu.Lock()
	defer sentMu.Unlock()
	_, ok := sentIDs[id]
	return ok
}

func sendVoiceNoteTracked(ctx context.Context, jid types.JID, ogg []byte) error {
	id := cli.GenerateMessageID()
	sentMu.Lock()
	sentIDs[id] = time.Now()
	for k, t := range sentIDs {
		if time.Since(t) > time.Hour {
			delete(sentIDs, k)
		}
	}
	sentMu.Unlock()
	return sendVoiceNoteWithID(ctx, jid, ogg, id)
}

func reply(jid types.JID, text string) {
	if _, err := cli.SendMessage(context.Background(), jid, newText(text)); err != nil {
		log.Printf("failed to reply: %v", err)
	}
}
