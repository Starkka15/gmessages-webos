package main

// Pairing with nothing but the phone. Google-account pairing needs the cookies
// of a browser signed in to Google, and the TouchPad's browser is too old to
// sign in. The phone's is not: so for a few minutes the relay offers a small
// page on the Wi-Fi network, the phone's browser opens it, and the cookies are
// pasted in there. A code shown on the TouchPad keeps strangers on the network
// out.

import (
	"crypto/rand"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
)

const (
	handoffPort  = 7341
	handoffLife  = 10 * time.Minute
	handoffTries = 5
)

type handoff struct {
	mu       sync.Mutex
	url      string
	code     string
	listener net.Listener
	tries    int
	external bool
	timer    *time.Timer
}

// This TouchPad's address on the network the phone can reach it by.
func lanAddress() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP.String(), nil
		}
	}
	return "", errors.New("this TouchPad is not on a network")
}

func newCode() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = '0' + b[i]%10
	}
	return string(b)
}

// Open the page for the phone. Returns its address and the code to type there.
func (r *Relay) startHandoff(external bool) (string, string, error) {
	r.mu.Lock()
	paired := r.paired
	r.mu.Unlock()
	if paired {
		return "", "", errors.New("already paired; unpair first")
	}
	r.stopHandoff()
	ip, err := lanAddress()
	if err != nil {
		return "", "", err
	}
	listener, err := listenTCP(fmt.Sprintf("0.0.0.0:%d", handoffPort))
	if err != nil {
		return "", "", fmt.Errorf("the page for the phone could not be opened: %w", err)
	}
	h := &handoff{
		url:      fmt.Sprintf("http://%s:%d", ip, handoffPort),
		code:     newCode(),
		listener: listener,
		external: external,
	}
	h.timer = time.AfterFunc(handoffLife, r.stopHandoff)
	r.mu.Lock()
	r.handoff = h
	r.mu.Unlock()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) { r.handoffPage(h, w, req) })
	go func() { _ = (&http.Server{Handler: mux, ReadHeaderTimeout: 15 * time.Second}).Serve(listener) }()
	r.log.Info().Str("address", h.url).Msg("the page for the phone is open")
	r.push(Event{Kind: "state"})
	return h.url, h.code, nil
}

func (r *Relay) stopHandoff() {
	r.mu.Lock()
	h := r.handoff
	r.handoff = nil
	r.mu.Unlock()
	if h == nil {
		return
	}
	h.timer.Stop()
	_ = h.listener.Close()
	r.log.Info().Msg("the page for the phone is closed")
	r.push(Event{Kind: "state"})
}

const handoffStyle = `<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
body{font-family:sans-serif;margin:0;padding:18px;background:#f3f3f3;color:#222;max-width:560px}
h1{font-size:22px} p,li{font-size:16px;line-height:1.4}
input,textarea{width:100%;box-sizing:border-box;font-size:18px;padding:10px;margin:6px 0 14px;border:1px solid #999;border-radius:6px}
textarea{height:150px;font-size:13px}
button{width:100%;font-size:19px;padding:13px;border:0;border-radius:6px;background:#1a73e8;color:#fff}
.bad{background:#c2372d;color:#fff;padding:10px;border-radius:6px} .pic{display:block;margin:10px auto;width:180px;height:180px}
</style>`

func (r *Relay) handoffPage(h *handoff, w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	form := func(problem string) {
		note := ""
		if problem != "" {
			note = `<p class="bad">` + html.EscapeString(problem) + `</p>`
		}
		fmt.Fprint(w, `<!doctype html><title>Pair the TouchPad</title>`+handoffStyle+`
<h1>Pair the TouchPad with Google Messages</h1>`+note+`
<ol>
<li>In this browser, sign in to Google and open <b>messages.google.com/web/config</b>.</li>
<li>On that page, open the <b>Cookie-Editor</b> add-on, choose <b>Export</b>, then <b>Header String</b>.</li>
<li>Come back here, type the code the TouchPad is showing, and paste.</li>
</ol>
<form method="post" action="/">
<label>Code on the TouchPad<input name="code" inputmode="numeric" autocomplete="off" maxlength="6"></label>
<label>Cookies<textarea name="cookies" autocomplete="off" spellcheck="false"></textarea></label>
<button type="submit">Pair</button>
</form>
<p>What you paste is a full sign-in to your Google account. It goes to the TouchPad over this Wi-Fi
network without encryption, and is kept on the TouchPad.</p>`)
	}
	if req.URL.Path != "/" {
		http.NotFound(w, req)
		return
	}
	if req.Method != http.MethodPost {
		form("")
		return
	}
	req.Body = http.MaxBytesReader(w, req.Body, 64<<10)
	if err := req.ParseForm(); err != nil {
		form("That could not be read. Try again.")
		return
	}
	h.mu.Lock()
	h.tries++
	spent := h.tries > handoffTries
	h.mu.Unlock()
	if spent {
		r.stopHandoff()
		fmt.Fprint(w, `<!doctype html>`+handoffStyle+`<p class="bad">Too many wrong codes. Start again on the TouchPad.</p>`)
		return
	}
	if strings.TrimSpace(req.PostForm.Get("code")) != h.code {
		form("That is not the code the TouchPad is showing.")
		return
	}
	cookies, err := cookiesFromText([]byte(req.PostForm.Get("cookies")))
	if err != nil {
		// A right code does not use up a try.
		h.mu.Lock()
		h.tries--
		h.mu.Unlock()
		form(strings.ToUpper(err.Error()[:1]) + err.Error()[1:] + ". Export from the messages.google.com page, signed in.")
		return
	}
	r.setExternal(h.external)
	emoji, err := r.startGooglePairing(cookies)
	if err != nil {
		h.mu.Lock()
		h.tries--
		h.mu.Unlock()
		form("Pairing could not start: " + err.Error())
		return
	}
	r.stopHandoff()
	fmt.Fprint(w, `<!doctype html><title>Pair the TouchPad</title>`+handoffStyle+`
<h1>Almost there</h1>
<p>Google Messages on this phone is about to show three pictures. Tap this one:</p>
<img class="pic" src="`+html.EscapeString(libgm.GetEmojiSVG(emoji))+`" alt="`+html.EscapeString(emoji)+`">
<p>The TouchPad shows the same picture. You can close this page.</p>`)
}

// ---- asked for from the TouchPad ----

func (r *Relay) handlePairPhone(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	url, code, err := r.startHandoff(req.URL.Query().Get("external") != "")
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url, "code": code})
}
