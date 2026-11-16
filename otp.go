package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// OTP delivery for the Daelus builder. The builder generates and verifies codes;
// Tafari only delivers them. Codes and phone numbers are never logged.
//
//	POST /otp/send   Authorization: Bearer $TAFARI_OTP_KEY
//	{"phone":"+254700000000","code":"123456","brand":"Daelus"}
//
// 202 sent, 400 bad input, 401 bad key, 404 {"error":"not_on_whatsapp"} (builder
// falls back to SMS), 429 rate limited, 503 WhatsApp not connected.
// Disabled (404) when TAFARI_OTP_KEY is unset.

var (
	codeRe  = regexp.MustCompile(`^[0-9]{4,8}$`)
	brandRe = regexp.MustCompile(`^[A-Za-z0-9 ._-]{1,24}$`)
)

// otpTransport is what the handler needs from WhatsApp; faked in tests.
type otpTransport interface {
	Connected() bool
	OnWhatsApp(ctx context.Context, phone string) (bool, error)
	SendText(ctx context.Context, phone, text string) error
}

type waTransport struct{}

func (waTransport) Connected() bool { return cli != nil && cli.Store.ID != nil && cli.IsConnected() }

func (waTransport) OnWhatsApp(ctx context.Context, phone string) (bool, error) {
	res, err := cli.IsOnWhatsApp(ctx, []string{"+" + phone})
	if err != nil {
		return false, err
	}
	return len(res) > 0 && res[0].IsIn, nil
}

func (waTransport) SendText(ctx context.Context, phone, text string) error {
	_, err := cli.SendMessage(ctx, types.NewJID(phone, types.DefaultUserServer), newText(text))
	return err
}

var _ = whatsmeow.PairClientChrome // keep the import explicit for the transport above

// otpLimiter caps sends so a bug or abuse cannot get the number banned:
// per phone, per hour overall, per day overall.
type otpLimiter struct {
	mu                sync.Mutex
	perPhone          map[string][]time.Time
	all               []time.Time
	phoneMax, hourMax int
	dayMax            int
	phoneWindow       time.Duration
	now               func() time.Time
}

func newOTPLimiter() *otpLimiter {
	atoi := func(env string, def int) int {
		if n, err := strconv.Atoi(os.Getenv(env)); err == nil && n > 0 {
			return n
		}
		return def
	}
	return &otpLimiter{
		perPhone: map[string][]time.Time{}, phoneMax: atoi("TAFARI_OTP_PER_PHONE", 3),
		hourMax: atoi("TAFARI_OTP_PER_HOUR", 30), dayMax: atoi("TAFARI_OTP_PER_DAY", 150),
		phoneWindow: 10 * time.Minute, now: time.Now,
	}
}

func prune(ts []time.Time, since time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if t.After(since) {
			out = append(out, t)
		}
	}
	return out
}

// allow records a send if every cap has room.
func (l *otpLimiter) allow(phone string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.now()
	l.perPhone[phone] = prune(l.perPhone[phone], n.Add(-l.phoneWindow))
	l.all = prune(l.all, n.Add(-24*time.Hour))
	hour := 0
	for _, t := range l.all {
		if t.After(n.Add(-time.Hour)) {
			hour++
		}
	}
	if len(l.perPhone[phone]) >= l.phoneMax || hour >= l.hourMax || len(l.all) >= l.dayMax {
		return false
	}
	l.perPhone[phone] = append(l.perPhone[phone], n)
	l.all = append(l.all, n)
	return true
}

func otpHandler(tr otpTransport, lim *otpLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(os.Getenv("TAFARI_OTP_KEY"))
		if key == "" {
			http.NotFound(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method"}`, http.StatusMethodNotAllowed)
			return
		}
		var in struct{ Phone, Code, Brand string }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&in); err != nil {
			http.Error(w, `{"error":"bad_json"}`, http.StatusBadRequest)
			return
		}
		phone := strings.TrimPrefix(strings.TrimSpace(in.Phone), "+")
		if len(phone) < 8 || len(phone) > 15 || strings.Trim(phone, "0123456789") != "" || strings.HasPrefix(phone, "0") {
			http.Error(w, `{"error":"bad_phone"}`, http.StatusBadRequest)
			return
		}
		if !codeRe.MatchString(in.Code) {
			http.Error(w, `{"error":"bad_code"}`, http.StatusBadRequest)
			return
		}
		brand := strings.TrimSpace(in.Brand)
		if brand == "" {
			brand = "Daelus"
		}
		if !brandRe.MatchString(brand) {
			http.Error(w, `{"error":"bad_brand"}`, http.StatusBadRequest)
			return
		}
		if !tr.Connected() {
			http.Error(w, `{"error":"whatsapp_unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		ctx := r.Context()
		ok, err := tr.OnWhatsApp(ctx, phone)
		if err != nil {
			http.Error(w, `{"error":"whatsapp_unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		if !ok {
			http.Error(w, `{"error":"not_on_whatsapp"}`, http.StatusNotFound)
			return
		}
		if !lim.allow(phone) {
			w.Header().Set("Retry-After", "600")
			http.Error(w, `{"error":"rate_limited"}`, http.StatusTooManyRequests)
			return
		}
		text := brand + " verification code: " + in.Code + "\nIt expires in 10 minutes. Never share this code with anyone. If you didn't ask for it, ignore this message."
		if err := tr.SendText(ctx, phone, text); err != nil {
			logf("[otp] send failed")
			http.Error(w, `{"error":"send_failed"}`, http.StatusBadGateway)
			return
		}
		logf("[otp] sent")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"sent"}`))
	}
}

func registerOTP() {
	http.HandleFunc("/otp/send", otpHandler(waTransport{}, newOTPLimiter()))
}
