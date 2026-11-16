package main

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/skip2/go-qrcode"
)

// Hot standby: two copies of Tafari can run at once, each linked to WhatsApp as
// its own device. The primary answers. A standby (TAFARI_STANDBY_OF set to the
// primary's /primary URL) stays connected but silent while the primary reports
// healthy, and starts answering after standbyFailures failed checks in a row.

const standbyFailures = 3

var (
	primaryDown atomic.Bool  // true once the primary has failed standbyFailures checks
	latestQR    atomic.Value // string: most recent QR code while unpaired
	standbyURL  = strings.TrimSpace(os.Getenv("TAFARI_STANDBY_OF"))
)

// standbyHolding reports whether this copy should stay silent.
func standbyHolding() bool {
	return standbyURL != "" && !primaryDown.Load()
}

// checkPrimary updates primaryDown from one health-check result.
func checkPrimary(fails *int, ok bool) {
	if ok {
		*fails = 0
		if primaryDown.Swap(false) {
			logf("[standby] primary is back; going quiet")
		}
		return
	}
	*fails++
	if *fails >= standbyFailures && !primaryDown.Swap(true) {
		logf("[standby] primary down; taking over")
	}
}

func startStandbyWatcher() {
	if standbyURL == "" {
		return
	}
	logf("[standby] watching %s", standbyURL)
	go func() {
		fails := 0
		client := &http.Client{Timeout: 8 * time.Second}
		for {
			ok := false
			if resp, err := client.Get(standbyURL); err == nil {
				ok = resp.StatusCode == http.StatusOK
				resp.Body.Close()
			}
			checkPrimary(&fails, ok)
			time.Sleep(15 * time.Second)
		}
	}()
}

// registerRoutes adds /primary (200 only while WhatsApp is connected, for the
// standby to poll) and /link (QR page for pairing a headless deploy, guarded by
// TAFARI_LINK_KEY; disabled when the key is unset).
func registerRoutes() {
	http.HandleFunc("/primary", func(w http.ResponseWriter, r *http.Request) {
		if cli != nil && cli.Store.ID != nil && cli.IsConnected() {
			_, _ = w.Write([]byte("ok"))
			return
		}
		http.Error(w, "not connected", http.StatusServiceUnavailable)
	})
	authed := func(r *http.Request) bool {
		key := os.Getenv("TAFARI_LINK_KEY")
		return key != "" && subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("key")), []byte(key)) == 1
	}
	http.HandleFunc("/link", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if cli != nil && cli.Store.ID != nil {
			fmt.Fprint(w, "<h2>Tafari is linked.</h2>")
			return
		}
		fmt.Fprintf(w, `<!doctype html><meta charset=utf-8><title>Link Tafari</title>
<body style="font-family:sans-serif;text-align:center;background:#fff">
<h2>Link Tafari</h2><p>WhatsApp, Linked devices, Link a device, scan.</p>
<div style="display:inline-block;padding:40px"><img id=q width=512 height=512></div>
<script>function r(){document.getElementById('q').src='/link.png?key='+encodeURIComponent(new URLSearchParams(location.search).get('key'))+'&t='+Date.now()}r();setInterval(r,4000)</script>`)
	})
	http.HandleFunc("/link.png", func(w http.ResponseWriter, r *http.Request) {
		code, _ := latestQR.Load().(string)
		if !authed(r) || code == "" {
			http.NotFound(w, r)
			return
		}
		png, err := qrcode.Encode(code, qrcode.Medium, 512)
		if err != nil {
			http.Error(w, "qr failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(png)
	})
}
