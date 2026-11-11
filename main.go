// whatsapp-bot — minimal whatsmeow example.
//
// Pairing modes:
//
//	go run .                                  # QR pairing (scan the printed QR)
//	go run . -phone +254****4826             # phone-number pairing (type the shown code into WA)
//	go run . -send +155****4567 -message "hi" # send one message and exit
//
// After pairing, session is saved to whatsapp.db and you just run `go run .` to reconnect.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/skip2/go-qrcode"
)

var cli *whatsmeow.Client

// AgentRouter (Anthropic-compatible) config — key read from env at runtime.
// AI backend config — all read from env at runtime so you can switch providers
// without code changes. Defaults point at AgentRouter (Anthropic-compatible),
// but AgentRouter is behind a WAF that blocks Render's IPs, so for Render use an
// OpenAI-compatible provider (OpenRouter/Groq/OpenAI) by setting AGENT_FORMAT=openai.
//
// Env vars:
//
//	AGENT_FORMAT   "anthropic" (default) or "openai"
//	AGENT_BASE_URL full URL to the messages endpoint
//	               anthropic default: https://agentrouter.org/v1/messages
//	               openai default:    https://openrouter.ai/api/v1/chat/completions
//	AGENT_API_KEY  your provider key
//	AGENT_MODEL    model name (e.g. claude-opus-4-6 or openai/gpt-4o-mini)
//	AGENTROUTER_API_KEY is accepted as a fallback for AGENT_API_KEY.
func agentBaseURL() string {
	if v := os.Getenv("AGENT_BASE_URL"); v != "" {
		return v
	}
	if os.Getenv("AGENT_FORMAT") == "openai" {
		return "https://openrouter.ai/api/v1/chat/completions"
	}
	return "https://agentrouter.org/v1/messages"
}
func agentModel() string {
	if v := os.Getenv("AGENT_MODEL"); v != "" {
		return v
	}
	if os.Getenv("AGENT_FORMAT") == "openai" {
		return "openai/gpt-4o-mini"
	}
	return "claude-opus-4-6"
}
func agentKey() string {
	candidates := []string{"AGENT_API_KEY", "AGENTROUTER_API_KEY", "OPENROUTER_API_KEY", "AGENT_KEY"}
	for _, name := range candidates {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			log.Printf("[config] using AI key from env %s (len=%d)", name, len(v))
			return v
		}
	}
	return ""
}
func agentFormat() string {
	if v := os.Getenv("AGENT_FORMAT"); v != "" {
		return v
	}
	return "anthropic"
}

var connectedSig = make(chan struct{}, 1)

func main() {
	sendTo := flag.String("send", "", "phone number in international format (e.g. +254700000000) to send to, then exit")
	sendMsg := flag.String("message", "", "message body to send with -send")
	pairPhone := flag.String("phone", "", "phone number in international format (e.g. +254700000000) to pair via WhatsApp code")
	flag.Parse()

	// Lightweight HTTP health endpoint for Render / container health checks.
	go func() {
		http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
		addr := ":" + os.Getenv("PORT")
		if addr == ":" {
			addr = ":8080"
		}
		_ = http.ListenAndServe(addr, nil)
	}()

	// Self-ping to keep a Render Free instance from spinning down after 15m
	// idle. Pinging the local health endpoint keeps the container active so the
	// WhatsApp WebSocket stays connected and @tafari keeps answering.
	go func() {
		selfAddr := "http://localhost:" + os.Getenv("PORT")
		if selfAddr == "http://localhost:" {
			selfAddr = "http://localhost:8080"
		}
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if resp, err := http.Get(selfAddr + "/healthz"); err == nil {
				resp.Body.Close()
			}
		}
	}()

	ctx := context.Background()
	dbLog := waLog.Stdout("Database", "INFO", true)

	// Data directory: TAFARI_DATA_DIR (set on Render's persistent disk) or "." locally.
	dataDir := os.Getenv("TAFARI_DATA_DIR")
	if dataDir == "" {
		dataDir = "."
	}
	// Create the data directory if it doesn't exist (e.g. on a mounted disk).
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("failed to create data dir %s: %v", dataDir, err)
	}
	dbPath := "file:" + dataDir + "/whatsapp.db?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL"

	// Open the SQLite DB with WAL + busy_timeout to avoid SQLITE_BUSY locks
	// under whatsmeow's concurrent reads/writes, and limit to one connection.
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	if _, err := sqlDB.Exec("PRAGMA busy_timeout=5000"); err != nil {
		log.Printf("warning: failed to set busy_timeout: %v", err)
	}

	if err := initMemory(sqlDB); err != nil {
		log.Fatalf("failed to init memory: %v", err)
	}

	container, err := sqlstore.New(ctx, "sqlite", dbPath, dbLog)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		log.Fatalf("failed to get device: %v", err)
	}

	clientLog := waLog.Stdout("Client", "INFO", true)
	cli = whatsmeow.NewClient(deviceStore, clientLog)
	cli.AddEventHandler(eventHandler)

	if *sendTo != "" {
		sendOnce(*sendTo, *sendMsg)
		return
	}

	// Connect first (required before either pairing method).
	qrConnected := make(chan struct{}, 1)
	go func() {
		// Wait for the first QR event, which signals the websocket is established.
		// With phone pairing we ignore the QR itself, but must wait for readiness.
		select {
		case <-qrConnected:
		case <-time.After(8 * time.Second):
		}
	}()
	_ = qrConnected

	if err := cli.Connect(); err != nil {
		log.Fatalf("failed to connect: %v", err)
	}

	// Phone-number pairing mode.
	if *pairPhone != "" {
		pairViaPhone(ctx, *pairPhone)
		// Keep running after pairing so incoming messages are handled.
		waitForever()
		return
	}

	// QR pairing mode.
	if cli.Store.ID == nil {
		qrChan, _ := cli.GetQRChannel(ctx)
		go func() {
			for evt := range qrChan {
				if evt.Event == "code" {
					printQR(evt.Code)
					if png, err := qrcode.Encode(evt.Code, qrcode.Medium, 512); err == nil {
						_ = os.WriteFile("qr.png", png, 0600)
						log.Println("QR code also written to qr.png")
					}
				} else if evt.Event == "success" {
					log.Println("paired successfully")
				} else {
					log.Println("QR event:", evt.Event)
				}
			}
		}()
		waitForever()
	} else {
		waitForever()
	}
}

// pairViaPhone uses WhatsApp's "link with phone number" code flow.
func pairViaPhone(ctx context.Context, phone string) {
	// Wait until the websocket is actually connected (not just the QR channel).
	log.Printf("waiting for WhatsApp connection before requesting code for %s ...", phone)
	select {
	case <-connectedSig:
		log.Println("connected - requesting pairing code")
	case <-time.After(30 * time.Second):
		log.Println("warning: connection wait timed out; attempting PairPhone anyway")
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		log.Printf("requesting pairing code (attempt %d) for %s ...", attempt, phone)
		code, err := cli.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Tafari (Linux)")
		if err == nil {
			_ = os.WriteFile("pairing-code.txt", []byte(code), 0600)
			fmt.Println()
			fmt.Println("======================================================")
			fmt.Printf("  Your pairing code is:  %s\n", code)
			fmt.Println("  Enter this in WhatsApp: Linked devices -> Link with phone number")
			fmt.Println("======================================================")
			return
		}
		lastErr = err
		log.Printf("PairPhone attempt %d failed: %v", attempt, err)
		// Reconnect and retry (bad-request often means not fully ready / rate-limited).
		cli.Disconnect()
		time.Sleep(3 * time.Second)
		if e := cli.Connect(); e != nil {
			log.Printf("reconnect failed: %v", e)
		}
		select {
		case <-connectedSig:
		case <-time.After(15 * time.Second):
		}
	}
	log.Fatalf("failed to request pairing code after retries: %v", lastErr)
}

// waitForQR blocks until the websocket is ready (first QR event) or 10s pass.
func waitForQR(ctx context.Context) {
	qrChan, _ := cli.GetQRChannel(ctx)
	select {
	case evt := <-qrChan:
		log.Printf("connection established (event: %s)", evt.Event)
	case <-time.After(10 * time.Second):
		log.Println("connection timeout waiting for QR event; proceeding anyway")
	}
}

func waitForever() {
	log.Println("Connected. Press Ctrl+C to disconnect.")
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
	cli.Disconnect()
}

func eventHandler(evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		handleMessage(v)
	case *events.QR:
		// Used only as a readiness signal for phone pairing; QR itself ignored.
	case *events.Connected:
		log.Println("event: connected to WhatsApp")
		select {
		case connectedSig <- struct{}{}:
		default:
		}
	case *events.Disconnected:
		log.Println("event: disconnected from WhatsApp")
	case *events.LoggedOut:
		log.Println("event: logged out — delete whatsapp.db and re-pair")
	case *events.PairSuccess:
		log.Println("event: pairing successful! ✅")
	}
}

func sendOnce(phone, text string) {
	ctx := context.Background()
	if cli.Store.ID == nil {
		log.Fatal("not paired yet — run without -send first to pair")
	}
	if err := cli.Connect(); err != nil {
		log.Fatalf("failed to connect: %v", err)
	}
	time.Sleep(2 * time.Second)
	jid, ok := parsePhone(phone)
	if !ok {
		log.Fatalf("invalid phone number %q — use international format like +254****4826", phone)
	}
	resp, err := cli.SendMessage(ctx, jid, newText(text))
	if err != nil {
		log.Fatalf("failed to send: %v", err)
	}
	log.Printf("sent to %s: server id %s", jid, resp.ID)
	cli.Disconnect()
}

func newText(text string) *waE2E.Message {
	return &waE2E.Message{Conversation: &text}
}

func parsePhone(phone string) (types.JID, bool) {
	p := strings.TrimPrefix(strings.TrimSpace(phone), "+")
	if p == "" {
		return types.JID{}, false
	}
	var digits strings.Builder
	for _, r := range p {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	if digits.Len() < 8 {
		return types.JID{}, false
	}
	return types.NewJID(digits.String(), types.DefaultUserServer), true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func printQR(code string) {
	qr, err := qrcode.New(code, qrcode.Low)
	if err != nil {
		log.Printf("failed to build QR: %v", err)
		return
	}
	fmt.Println(qr.ToString(false))
}
