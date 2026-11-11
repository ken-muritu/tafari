package main

// voice.go: ElevenLabs speech-to-text for incoming voice notes and text-to-speech for
// Tafari's voice-note replies. ElevenLabs returns Ogg Opus, WhatsApp's native PTT format,
// so no ffmpeg is needed.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func elevenKey() string { return strings.TrimSpace(os.Getenv("ELEVENLABS_API_KEY")) }

// Tafari's default (female) voice is TAFARI_VOICE_ID; the optional male voice (Ken's own clone)
// is TAFARI_VOICE_ID_MALE. Each chat can switch with "male voice" / "female voice".
func tafariVoice() string { return strings.TrimSpace(os.Getenv("TAFARI_VOICE_ID")) }
func maleVoice() string   { return strings.TrimSpace(os.Getenv("TAFARI_VOICE_ID_MALE")) }

func voiceFor(chat string) (id string, male bool) {
	if prefMale(chat) && maleVoice() != "" {
		return maleVoice(), true
	}
	return tafariVoice(), false
}

func voiceEnabled() bool { return elevenKey() != "" && tafariVoice() != "" }

// transcribe sends audio bytes to ElevenLabs Scribe and returns the text.
func transcribe(audio []byte) (string, error) {
	if elevenKey() == "" {
		return "", fmt.Errorf("ELEVENLABS_API_KEY not set")
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("model_id", envOr("TAFARI_STT_MODEL", "scribe_v1"))
	fw, _ := w.CreateFormFile("file", "voice.ogg")
	_, _ = fw.Write(audio)
	_ = w.Close()
	req, _ := http.NewRequest("POST", "https://api.elevenlabs.io/v1/speech-to-text", &buf)
	req.Header.Set("xi-api-key", elevenKey())
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("speech-to-text HTTP %d", resp.StatusCode)
	}
	var out struct{ Text string }
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.Text), nil
}

// speak turns text into an Ogg Opus voice note in the given voice.
func speak(text, voiceID string) ([]byte, error) {
	payload, _ := json.Marshal(map[string]any{
		"text":           text,
		"model_id":       envOr("TAFARI_TTS_MODEL", "eleven_flash_v2_5"),
		"voice_settings": map[string]any{"stability": 0.5, "similarity_boost": 0.8},
	})
	url := "https://api.elevenlabs.io/v1/text-to-speech/" + voiceID + "?output_format=opus_48000_64"
	req, _ := http.NewRequest("POST", url, bytes.NewReader(payload))
	req.Header.Set("xi-api-key", elevenKey())
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	audio, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !bytes.HasPrefix(audio, []byte("OggS")) {
		return nil, fmt.Errorf("text-to-speech HTTP %d", resp.StatusCode)
	}
	return audio, nil
}

// oggSeconds reads the last Ogg page's granule position (48 kHz for Opus) to get the duration.
func oggSeconds(b []byte) uint32 {
	i := bytes.LastIndex(b, []byte("OggS"))
	if i < 0 || i+14 > len(b) {
		return 0
	}
	granule := binary.LittleEndian.Uint64(b[i+6 : i+14])
	return uint32(granule / 48000)
}

// sendVoiceNoteWithID uploads Ogg Opus audio and sends it as a push-to-talk voice note with a known message ID.
func sendVoiceNoteWithID(ctx context.Context, jid types.JID, audio []byte, id types.MessageID) error {
	up, err := cli.Upload(ctx, audio, whatsmeow.MediaAudio)
	if err != nil {
		return err
	}
	msg := &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
		URL:           proto.String(up.URL),
		DirectPath:    proto.String(up.DirectPath),
		MediaKey:      up.MediaKey,
		FileEncSHA256: up.FileEncSHA256,
		FileSHA256:    up.FileSHA256,
		FileLength:    proto.Uint64(up.FileLength),
		Mimetype:      proto.String("audio/ogg; codecs=opus"),
		Seconds:       proto.Uint32(oggSeconds(audio)),
		PTT:           proto.Bool(true),
	}}
	_, err = cli.SendMessage(ctx, jid, msg, whatsmeow.SendRequestExtra{ID: id})
	return err
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
