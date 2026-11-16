package main

import (
	"crypto/subtle"
	"net/http"
	"os"
	"runtime/pprof"
	"strings"
)

// GET /stacks?key=$TAFARI_LINK_KEY dumps goroutine stacks, to see what a stuck
// connection is waiting on. Stacks contain no message text. Disabled when the key is unset.
func registerDebug() {
	http.HandleFunc("/stacks", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(os.Getenv("TAFARI_LINK_KEY"))
		if key == "" || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("key")), []byte(key)) != 1 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_ = pprof.Lookup("goroutine").WriteTo(w, 2)
	})
}
