package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeWA struct {
	connected bool
	onWA      bool
	sent      []string
}

func (f *fakeWA) Connected() bool { return f.connected }
func (f *fakeWA) OnWhatsApp(context.Context, string) (bool, error) {
	return f.onWA, nil
}
func (f *fakeWA) SendText(_ context.Context, phone, text string) error {
	f.sent = append(f.sent, phone+"|"+text)
	return nil
}

func post(h http.HandlerFunc, auth, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/otp/send", strings.NewReader(body))
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestOTPHandler(t *testing.T) {
	t.Setenv("TAFARI_OTP_KEY", "secret")
	fw := &fakeWA{connected: true, onWA: true}
	h := otpHandler(fw, newOTPLimiter())
	good := `{"phone":"+254700000001","code":"123456","brand":"Daelus"}`

	if c := post(h, "Bearer wrong", good).Code; c != 401 {
		t.Fatalf("bad key: %d", c)
	}
	for _, body := range []string{
		`{"phone":"0700000001","code":"123456"}`, `{"phone":"+2547","code":"123456"}`,
		`{"phone":"+254700000001","code":"12"}`, `{"phone":"+254700000001","code":"12a456"}`,
		`{"phone":"+254700000001","code":"123456","brand":"<script>"}`, `nope`,
	} {
		if c := post(h, "Bearer secret", body).Code; c != 400 {
			t.Fatalf("expected 400 for %s, got %d", body, c)
		}
	}
	if len(fw.sent) != 0 {
		t.Fatal("nothing may be sent for invalid input")
	}
	if c := post(h, "Bearer secret", good).Code; c != 202 {
		t.Fatalf("good request: %d", c)
	}
	if len(fw.sent) != 1 || !strings.Contains(fw.sent[0], "254700000001|Daelus verification code: 123456") {
		t.Fatalf("sent: %v", fw.sent)
	}
	fw.onWA = false
	if c := post(h, "Bearer secret", good).Code; c != 404 {
		t.Fatalf("not on whatsapp: %d", c)
	}
	fw.onWA, fw.connected = true, false
	if c := post(h, "Bearer secret", good).Code; c != 503 {
		t.Fatalf("disconnected: %d", c)
	}
}

func TestOTPDisabledWithoutKey(t *testing.T) {
	t.Setenv("TAFARI_OTP_KEY", "")
	h := otpHandler(&fakeWA{connected: true, onWA: true}, newOTPLimiter())
	if c := post(h, "Bearer ", `{"phone":"+254700000001","code":"123456"}`).Code; c != 404 {
		t.Fatalf("unset key must disable the route, got %d", c)
	}
}

func TestOTPRateLimits(t *testing.T) {
	t.Setenv("TAFARI_OTP_KEY", "secret")
	fw := &fakeWA{connected: true, onWA: true}
	lim := newOTPLimiter()
	clock := time.Now()
	lim.now = func() time.Time { return clock }
	h := otpHandler(fw, lim)
	body := `{"phone":"+254700000002","code":"654321"}`
	for i := 0; i < 3; i++ {
		if c := post(h, "Bearer secret", body).Code; c != 202 {
			t.Fatalf("send %d: %d", i, c)
		}
	}
	if c := post(h, "Bearer secret", body).Code; c != 429 {
		t.Fatalf("4th send to one phone within 10 minutes must be limited, got %d", c)
	}
	clock = clock.Add(11 * time.Minute)
	if c := post(h, "Bearer secret", body).Code; c != 202 {
		t.Fatalf("after the window it must work again, got %d", c)
	}
	// overall hourly cap
	lim2 := newOTPLimiter()
	lim2.hourMax = 2
	h2 := otpHandler(fw, lim2)
	post(h2, "Bearer secret", `{"phone":"+254700000010","code":"111111"}`)
	post(h2, "Bearer secret", `{"phone":"+254700000011","code":"111111"}`)
	if c := post(h2, "Bearer secret", `{"phone":"+254700000012","code":"111111"}`).Code; c != 429 {
		t.Fatalf("hourly cap: %d", c)
	}
}
