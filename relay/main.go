// gmrelay: a Google Messages relay small enough for an HP TouchPad.
//
// It pairs with the Google Messages app on an Android phone the way "Messages
// for web" does (a QR code scanned on the phone), keeps that connection up, and
// offers the conversations and messages as plain JSON over HTTP on this machine,
// for a webOS app or service that cannot speak the protocol itself.
//
// The protocol work is libgm, from mautrix-gmessages (AGPL-3.0).
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU Affero General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option) any
// later version.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	qrcode "github.com/skip2/go-qrcode"
	"go.mau.fi/util/exhttp"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/util"
)

// A QR code is good for this long; then the phone wants a new one.
const qrLife = 30 * time.Second

// How many codes are shown before pairing is given up, so an unwatched screen
// does not ask Google for codes forever.
const qrTries = 10

// Events kept for clients that poll.
const eventsKept = 500

// A message, as the clients get it.
type Message struct {
	ID       string  `json:"id"`
	Conv     string  `json:"conv"`
	Time     int64   `json:"time"` // milliseconds since 1970
	FromMe   bool    `json:"fromMe"`
	Sender   string  `json:"sender,omitempty"` // a name, where the phone knows one
	Number   string  `json:"number,omitempty"`
	Text     string  `json:"text"`
	Media    []Media `json:"media,omitempty"`
	Status   string  `json:"status,omitempty"`
	TmpID    string  `json:"tmpId,omitempty"` // the id /send answered with, on our own messages
	Reaction bool    `json:"-"`
}

type Media struct {
	Mime string `json:"mime"`
	Name string `json:"name,omitempty"`
	Size int64  `json:"size,omitempty"`
	// For fetching the media from Google; kept out of what HTTP clients see. A
	// picture may come with only a thumbnail, or small media inline.
	ID       string `json:"-"`
	Key      []byte `json:"-"`
	ThumbID  string `json:"-"`
	ThumbKey []byte `json:"-"`
	Data     []byte `json:"-"`
	ActionID string `json:"-"` // the part's id, for asking the phone for the full-size picture
}

type Person struct {
	Name   string `json:"name,omitempty"`
	Number string `json:"number,omitempty"`
}

// A conversation, as the clients get it.
type Conversation struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Group    bool     `json:"group"`
	Unread   bool     `json:"unread"`
	ReadOnly bool     `json:"readOnly,omitempty"`
	RCS      bool     `json:"rcs"`
	Time     int64    `json:"time"`
	Last     string   `json:"last,omitempty"`
	LastMine bool     `json:"lastMine,omitempty"`
	People   []Person `json:"people,omitempty"`
}

type Event struct {
	Seq          int64         `json:"seq"`
	Kind         string        `json:"kind"` // "message", "conversation" or "state"
	Message      *Message      `json:"message,omitempty"`
	Conversation *Conversation `json:"conversation,omitempty"`
}

type Relay struct {
	dir string
	log zerolog.Logger

	mu      sync.Mutex
	sess    *libgm.AuthData
	cli     *libgm.Client
	state   string // "unpaired", "pairing", "connecting", "ready", "phoneAway", "error"
	errMsg  string
	bridge  *bridge  // the webOS Messaging side, when asked for
	handoff *handoff // the page open for a phone's browser, while pairing that way
	qr      string
	emoji   string // Google-account pairing: the picture to tap on the phone
	paired  bool   // a pairing was completed and its session is in sess
	// The Google account's cookies, kept so a pairing that failed can be tried again.
	cookies map[string]string
	convs   map[string]*gmproto.Conversation
	cancel  context.CancelFunc

	evMu   sync.Mutex
	evCond *sync.Cond
	events []Event
	seq    int64
}

func main() {
	dir := flag.String("dir", ".", "where the session is kept")
	listen := flag.String("listen", "127.0.0.1:7340", "address to serve on")
	verbose := flag.Bool("v", false, "log the protocol's chatter")
	webos := flag.Bool("webos", false, "put the conversations in the webOS Messaging app")
	flag.Parse()

	level := zerolog.InfoLevel
	if *verbose {
		level = zerolog.DebugLevel
	}
	log := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true, TimeFormat: time.Stamp}).
		Level(level).With().Timestamp().Logger()

	r := &Relay{dir: *dir, log: log, state: "unpaired", convs: map[string]*gmproto.Conversation{}}
	r.evCond = sync.NewCond(&r.evMu)
	if *webos {
		r.bridge = newBridge(r)
	}
	// Wake the long polls now and then so they can time out.
	go func() {
		for range time.Tick(time.Second) {
			r.evCond.Broadcast()
		}
	}()
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		log.Fatal().Err(err).Msg("cannot make the session directory")
	}
	r.begin()

	mux := http.NewServeMux()
	mux.HandleFunc("/status", r.handleStatus)
	mux.HandleFunc("/pair", r.handlePair)
	mux.HandleFunc("/pair/google", r.handlePairGoogle)
	mux.HandleFunc("/pair/cancel", r.handlePairCancel)
	mux.HandleFunc("/pair/phone", r.handlePairPhone)
	mux.HandleFunc("/pair/atlas", r.handlePairAtlas)
	mux.HandleFunc("/qr.png", r.handleQR)
	mux.HandleFunc("/emoji.png", r.handleEmoji)
	mux.HandleFunc("/unpair", r.handleUnpair)
	mux.HandleFunc("/conversations", r.handleConversations)
	mux.HandleFunc("/messages", r.handleMessages)
	mux.HandleFunc("/send", r.handleSend)
	mux.HandleFunc("/read", r.handleRead)
	mux.HandleFunc("/events", r.handleEvents)
	srv := &http.Server{Handler: guard(mux)}
	listener, err := listenTCP(*listen)
	if err != nil {
		log.Fatal().Err(err).Msg("cannot listen")
	}
	go func() {
		log.Info().Str("address", *listen).Msg("serving")
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal().Err(err).Msg("cannot serve")
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Info().Msg("stopping")
	r.mu.Lock()
	if r.cli != nil {
		r.cli.Disconnect()
	}
	r.mu.Unlock()
	r.saveSession()
}

// ---- the session ----

func (r *Relay) sessionPath() string { return filepath.Join(r.dir, "session.json") }

// What is kept between runs.
type savedSession struct {
	Paired  bool              `json:"paired"`
	Auth    *libgm.AuthData   `json:"auth,omitempty"`
	Cookies map[string]string `json:"cookies,omitempty"`
}

func (r *Relay) saveSession() {
	r.mu.Lock()
	saved := savedSession{Paired: r.paired, Cookies: r.cookies}
	if r.paired {
		saved.Auth = r.sess
	}
	r.mu.Unlock()
	if !saved.Paired && len(saved.Cookies) == 0 {
		return
	}
	data, err := json.Marshal(&saved)
	if err != nil {
		r.log.Err(err).Msg("cannot encode the session")
		return
	}
	// Written beside and renamed, so a crash never leaves half a session.
	tmp := r.sessionPath() + ".new"
	if err = os.WriteFile(tmp, data, 0o600); err == nil {
		err = os.Rename(tmp, r.sessionPath())
	}
	if err != nil {
		r.log.Err(err).Msg("cannot save the session")
	}
}

func (r *Relay) newClient(sess *libgm.AuthData) {
	r.sess = sess
	r.cli = libgm.NewClient(sess, nil, r.log, exhttp.SensibleClientSettings)
	r.cli.SetEventHandler(r.handleEvent)
}

// Start: connect with a saved session, or wait to be asked to pair.
func (r *Relay) begin() {
	var saved savedSession
	data, err := os.ReadFile(r.sessionPath())
	if err == nil {
		if err = json.Unmarshal(data, &saved); err != nil {
			r.log.Warn().Err(err).Msg("the saved session cannot be read; pairing is needed")
			saved = savedSession{}
		} else if saved.Auth == nil && len(saved.Cookies) == 0 {
			// A file from before pairings were marked as complete: only its cookies are trusted.
			old := libgm.NewAuthData()
			if json.Unmarshal(data, old) == nil {
				saved.Cookies = old.Cookies
			}
		}
	}
	r.mu.Lock()
	r.cookies = saved.Cookies
	r.paired = saved.Paired && saved.Auth != nil && saved.Auth.Mobile != nil
	if r.paired {
		r.newClient(saved.Auth)
	} else {
		r.newClient(libgm.NewAuthData())
	}
	paired := r.paired
	r.mu.Unlock()
	if !paired {
		r.setState("unpaired", "")
		return
	}
	r.setState("connecting", "")
	go r.connect()
}

func (r *Relay) connect() {
	for {
		r.mu.Lock()
		cli := r.cli
		r.mu.Unlock()
		err := cli.Connect(context.Background())
		if err == nil {
			return
		}
		r.log.Err(err).Msg("cannot connect; trying again shortly")
		r.setState("error", err.Error())
		time.Sleep(20 * time.Second)
		r.mu.Lock()
		gone := r.cli != cli
		r.mu.Unlock()
		if gone {
			return
		}
	}
}

func (r *Relay) setState(state, errMsg string) {
	r.mu.Lock()
	changed := r.state != state || r.errMsg != errMsg
	r.state, r.errMsg = state, errMsg
	if state != "pairing" {
		r.qr = ""
		r.emoji = ""
	}
	r.mu.Unlock()
	if changed {
		r.log.Info().Str("state", state).Str("error", errMsg).Msg("state")
		if r.bridge != nil {
			if state == "ready" {
				r.bridge.connected()
			} else {
				r.bridge.setOnline(false)
			}
		}
		r.push(Event{Kind: "state"})
	}
}

// ---- pairing ----

func (r *Relay) startPairing() error {
	r.mu.Lock()
	if r.state == "pairing" {
		r.mu.Unlock()
		return nil
	}
	if r.paired {
		r.mu.Unlock()
		return errors.New("already paired; unpair first")
	}
	if r.cancel != nil {
		r.cancel()
	}
	if r.cli != nil {
		r.cli.Disconnect()
	}
	r.newClient(libgm.NewAuthData())
	cli := r.cli
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.mu.Unlock()

	qr, err := cli.StartLogin(ctx)
	if err != nil {
		cancel()
		r.setState("error", "pairing could not start: "+err.Error())
		return err
	}
	r.mu.Lock()
	r.qr = qr
	r.state, r.errMsg = "pairing", ""
	r.mu.Unlock()
	r.push(Event{Kind: "state"})

	// A new code every half minute until the phone scans one.
	go func() {
		for i := 1; i < qrTries; i++ {
			select {
			case <-ctx.Done():
				return
			case <-time.After(qrLife):
			}
			r.mu.Lock()
			still := r.state == "pairing" && r.cli == cli
			r.mu.Unlock()
			if !still {
				return
			}
			next, err := cli.RefreshPhoneRelay()
			if err != nil {
				r.setState("error", "the pairing code could not be renewed: "+err.Error())
				return
			}
			r.mu.Lock()
			r.qr = next
			r.mu.Unlock()
			r.push(Event{Kind: "state"})
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(qrLife):
		}
		r.mu.Lock()
		still := r.state == "pairing" && r.cli == cli
		r.mu.Unlock()
		if still {
			cli.Disconnect()
			r.setState("unpaired", "")
		}
	}()
	return nil
}

// Pair through the Google account the phone is signed in to. The cookies are
// those of a browser signed in to that account; the phone then shows three
// pictures and the one returned here is the one to tap.
func (r *Relay) startGooglePairing(cookies map[string]string) (string, error) {
	if len(cookies) == 0 {
		// None given: use the ones kept from the last attempt.
		r.mu.Lock()
		cookies = r.cookies
		r.mu.Unlock()
	}
	// The SID family is what signs the requests in. OSID is set by the Messages
	// web page itself and is not needed to pair, so it is not required here; a
	// browser that cannot open that page (such as Atlas on the TouchPad) still has
	// the rest.
	for _, name := range []string{"SID", "HSID", "SSID", "APISID", "SAPISID"} {
		if cookies[name] == "" {
			return "", fmt.Errorf("the %s cookie is missing", name)
		}
	}
	r.mu.Lock()
	if r.paired {
		r.mu.Unlock()
		return "", errors.New("already paired; unpair first")
	}
	if r.cancel != nil {
		r.cancel()
	}
	if r.cli != nil {
		r.cli.Disconnect()
	}
	r.cookies = cookies
	sess := libgm.NewAuthData()
	// The client works on its own copy: it updates cookies as Google renews them.
	sess.Cookies = make(map[string]string, len(cookies))
	for name, value := range cookies {
		sess.Cookies[name] = value
	}
	r.newClient(sess)
	cli := r.cli
	bg, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.mu.Unlock()

	r.saveSession()
	ctx, done := context.WithTimeout(bg, 60*time.Second)
	defer done()
	if err := cli.FetchConfig(ctx); err != nil {
		cancel()
		r.setState("unpaired", "")
		return "", fmt.Errorf("Google did not accept the cookies: %w", err)
	}
	emoji, ps, err := cli.StartGaiaPairing(ctx, bg)
	if err != nil {
		cancel()
		cli.Disconnect()
		r.setState("unpaired", "")
		return "", err
	}
	r.mu.Lock()
	r.emoji = emoji
	r.state, r.errMsg = "pairing", ""
	r.mu.Unlock()
	r.push(Event{Kind: "state"})

	go func() {
		phone, err := cli.FinishGaiaPairing(bg, ps)
		r.mu.Lock()
		still := r.cli == cli
		r.mu.Unlock()
		if !still {
			return
		}
		if err != nil {
			cli.Disconnect()
			r.mu.Lock()
			r.newClient(libgm.NewAuthData())
			r.mu.Unlock()
			if errors.Is(err, context.Canceled) {
				r.setState("unpaired", "")
			} else {
				r.setState("unpaired", "pairing failed: "+err.Error())
			}
			return
		}
		r.log.Info().Str("phone", phone).Msg("paired through the Google account")
		r.mu.Lock()
		r.paired = true
		r.mu.Unlock()
		r.saveSession()
		r.setState("connecting", "")
		cli.Disconnect()
		r.connect()
	}()
	return emoji, nil
}

// Give up a pairing that is under way.
func (r *Relay) cancelPairing() {
	r.stopHandoff()
	r.mu.Lock()
	if r.state != "pairing" {
		r.mu.Unlock()
		return
	}
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	if r.cli != nil {
		r.cli.Disconnect()
	}
	r.newClient(libgm.NewAuthData())
	r.mu.Unlock()
	r.setState("unpaired", "")
}

func (r *Relay) unpair() {
	r.mu.Lock()
	cli := r.cli
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	r.mu.Unlock()
	if cli != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := cli.Unpair(ctx); err != nil {
			r.log.Warn().Err(err).Msg("the phone was not told of the unpairing")
		}
		cancel()
		cli.Disconnect()
	}
	_ = os.Remove(r.sessionPath())
	if r.bridge != nil {
		r.bridge.forget()
	}
	r.mu.Lock()
	r.paired = false
	r.cookies = nil
	r.newClient(libgm.NewAuthData())
	r.convs = map[string]*gmproto.Conversation{}
	r.mu.Unlock()
	r.setState("unpaired", "")
}

// ---- what the phone tells us ----

func (r *Relay) handleEvent(raw any) {
	switch evt := raw.(type) {
	case *events.PairSuccessful:
		r.log.Info().Str("phone", evt.PhoneID).Msg("paired")
		r.mu.Lock()
		r.paired = true
		if r.cancel != nil {
			r.cancel = nil // the long poll goes on; only the code renewal stops, by state
		}
		r.mu.Unlock()
		r.saveSession()
		r.setState("connecting", "")
	case *events.ClientReady:
		r.saveSession()
		r.mu.Lock()
		for _, conv := range evt.Conversations {
			r.convs[conv.GetConversationID()] = conv
		}
		r.mu.Unlock()
		r.setState("ready", "")
	case *events.AuthTokenRefreshed:
		r.saveSession()
	case *events.PhoneNotResponding:
		r.setState("phoneAway", "")
	case *events.PhoneRespondingAgain:
		r.setState("ready", "")
	case *events.ListenRecovered:
		r.setState("ready", "")
	case *events.ListenTemporaryError:
		r.log.Warn().Err(evt.Error).Msg("the connection stumbled")
	case *events.ListenFatalError:
		r.setState("error", "the connection to Google was lost: "+evt.Error.Error())
	case *events.GaiaLoggedOut:
		r.setState("error", "the phone signed this device out")
	case *gmproto.RevokePairData:
		r.log.Warn().Msg("the phone removed this device")
		_ = os.Remove(r.sessionPath())
		r.mu.Lock()
		r.paired = false
		r.newClient(libgm.NewAuthData())
		r.mu.Unlock()
		r.setState("unpaired", "")
	case *libgm.WrappedMessage:
		if evt.IsOld {
			return
		}
		msg := r.plainMessage(evt.Message)
		if r.bridge != nil {
			r.bridge.message(msg)
		}
		r.push(Event{Kind: "message", Message: &msg})
	case *gmproto.Conversation:
		r.mu.Lock()
		r.convs[evt.GetConversationID()] = evt
		r.mu.Unlock()
		if r.bridge != nil {
			r.bridge.remember(evt)
			if !evt.GetUnread() {
				r.bridge.convRead(evt.GetConversationID())
			}
		}
		conv := plainConversation(evt)
		r.push(Event{Kind: "conversation", Conversation: &conv})
	}
}

func (r *Relay) push(e Event) {
	r.evMu.Lock()
	r.seq++
	e.Seq = r.seq
	r.events = append(r.events, e)
	if len(r.events) > eventsKept {
		r.events = r.events[len(r.events)-eventsKept:]
	}
	r.evMu.Unlock()
	r.evCond.Broadcast()
}

// ---- protocol shapes into plain ones ----

func plainConversation(c *gmproto.Conversation) Conversation {
	out := Conversation{
		ID:       c.GetConversationID(),
		Name:     c.GetName(),
		Group:    c.GetIsGroupChat(),
		Unread:   c.GetUnread(),
		ReadOnly: c.GetReadOnly(),
		RCS:      c.GetType() == gmproto.ConversationType_RCS,
		Time:     c.GetLastMessageTimestamp() / 1000,
		Last:     c.GetLatestMessage().GetDisplayContent(),
		LastMine: c.GetLatestMessage().GetFromMe() != 0,
	}
	for _, p := range c.GetParticipants() {
		if p.GetIsMe() || !p.GetIsVisible() {
			continue
		}
		name := p.GetFullName()
		if name == "" {
			name = p.GetFirstName()
		}
		number := p.GetID().GetNumber()
		if number == "" {
			number = p.GetFormattedNumber()
		}
		out.People = append(out.People, Person{Name: name, Number: number})
	}
	return out
}

func (r *Relay) plainMessage(m *gmproto.Message) Message {
	out := Message{
		ID:     m.GetMessageID(),
		Conv:   m.GetConversationID(),
		Time:   m.GetTimestamp() / 1000,
		Status: m.GetMessageStatus().GetStatus().String(),
		TmpID:  m.GetTmpID(),
	}
	sender := m.GetSenderParticipant()
	if sender == nil {
		// Older messages name only the sender's id; the conversation knows who that is.
		r.mu.Lock()
		conv := r.convs[m.GetConversationID()]
		r.mu.Unlock()
		for _, p := range conv.GetParticipants() {
			if p.GetID().GetParticipantID() == m.GetParticipantID() {
				sender = p
				break
			}
		}
	}
	if sender != nil {
		out.FromMe = sender.GetIsMe()
		out.Sender = sender.GetFullName()
		if out.Sender == "" {
			out.Sender = sender.GetFirstName()
		}
		out.Number = sender.GetID().GetNumber()
	}
	for _, part := range m.GetMessageInfo() {
		if text := part.GetMessageContent(); text != nil {
			if out.Text != "" {
				out.Text += "\n"
			}
			out.Text += text.GetContent()
		} else if media := part.GetMediaContent(); media != nil {
			out.Media = append(out.Media, Media{Mime: media.GetMimeType(), Name: media.GetMediaName(), Size: media.GetSize(),
				ID: media.GetMediaID(), Key: media.GetDecryptionKey(),
				ThumbID: media.GetThumbnailMediaID(), ThumbKey: media.GetThumbnailDecryptionKey(),
				Data: media.GetMediaData(), ActionID: part.GetActionMessageID()})
		}
	}
	return out
}

// ---- HTTP ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// The connected client, or an answer saying why there is none.
func (r *Relay) ready(w http.ResponseWriter) *libgm.Client {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != "ready" && r.state != "phoneAway" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "not connected", "state": r.state})
		return nil
	}
	return r.cli
}

func (r *Relay) handleStatus(w http.ResponseWriter, _ *http.Request) {
	r.mu.Lock()
	out := map[string]any{"state": r.state, "pairing": r.state == "pairing"}
	if r.emoji != "" {
		out["emoji"] = r.emoji
		out["emojiImage"] = libgm.GetEmojiSVG(r.emoji)
	}
	if r.qr != "" {
		out["qr"] = true
	}
	if r.errMsg != "" {
		out["error"] = r.errMsg
	}
	if r.paired {
		out["paired"] = true
	}
	if len(r.cookies) > 0 {
		out["cookies"] = true
	}
	if r.handoff != nil {
		out["handoff"] = map[string]string{"url": r.handoff.url, "code": r.handoff.code}
	}
	bridge := r.bridge
	r.mu.Unlock()
	if bridge != nil {
		bridge.mu.Lock()
		out["me"] = bridge.me
		out["account"] = bridge.set
		bridge.mu.Unlock()
	}
	r.evMu.Lock()
	out["seq"] = r.seq
	r.evMu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (r *Relay) handlePair(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	r.setExternal(req.URL.Query().Get("external") != "")
	if err := r.startPairing(); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pairing": true})
}

func (r *Relay) handlePairGoogle(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	var body struct {
		Cookies map[string]string `json:"cookies"`
		// A file holding the cookies instead: a request copied from a browser as cURL, or its Cookie header.
		File string `json:"file"`
		// The account in Messaging is made by whoever asked (the Accounts app), not by the relay.
		External bool `json:"external"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 64<<10)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, errors.New("the cookies could not be read"))
		return
	}
	if body.File != "" {
		cookies, err := cookiesFromFile(body.File)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		body.Cookies = cookies
	}
	r.setExternal(body.External)
	emoji, err := r.startGooglePairing(body.Cookies)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pairing": true, "emoji": emoji, "emojiImage": libgm.GetEmojiSVG(emoji)})
}

// Pair with the Google sign-in the Atlas browser on this TouchPad holds.
// The cookies are read from Atlas itself, so nothing leaves the tablet.
func (r *Relay) handlePairAtlas(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	cookies, err := atlasCookiesRead()
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	r.setExternal(req.URL.Query().Get("external") != "")
	emoji, err := r.startGooglePairing(cookies)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pairing": true, "emoji": emoji, "emojiImage": libgm.GetEmojiSVG(emoji)})
}

func (r *Relay) handlePairCancel(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	r.cancelPairing()
	writeJSON(w, http.StatusOK, map[string]any{"pairing": false})
}

// The cookies a signed-in browser sends, picked out of a file of text and
// the file removed. Only files on the TouchPad's USB drive are read.
func cookiesFromFile(path string) (map[string]string, error) {
	clean := filepath.Clean(path)
	if !strings.HasPrefix(clean, "/media/internal/") {
		return nil, errors.New("the cookies file must be on the USB drive")
	}
	data, err := os.ReadFile(clean)
	if err != nil {
		return nil, errors.New("there is no " + filepath.Base(clean) + " on the USB drive")
	}
	cookies, err := cookiesFromText(data)
	if err != nil {
		return nil, err
	}
	_ = os.Remove(clean)
	return cookies, nil
}

// The cookies pairing needs, picked out of text: a request copied as cURL, a
// Cookie header, or a cookie tool's "name=value; name=value" export.
func cookiesFromText(data []byte) (map[string]string, error) {
	cookies := map[string]string{}
	for _, name := range []string{"SID", "HSID", "OSID", "SSID", "APISID", "SAPISID", "__Secure-1PSIDTS"} {
		re := regexp.MustCompile(`(?:^|[^\w-])` + regexp.QuoteMeta(name) + `=([^;'"\s]+)`)
		if m := re.FindSubmatch(data); m != nil {
			cookies[name] = string(m[1])
		}
	}
	for _, name := range []string{"SID", "HSID", "SSID", "APISID", "SAPISID"} {
		if cookies[name] == "" {
			return nil, fmt.Errorf("there is no %s cookie in it", name)
		}
	}
	return cookies, nil
}

// The picture to tap, fetched for a screen that cannot reach Google itself.
func (r *Relay) handleEmoji(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	emoji := r.emoji
	r.mu.Unlock()
	if emoji == "" {
		writeError(w, http.StatusNotFound, errors.New("not pairing"))
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), 20*time.Second)
	defer cancel()
	// The TouchPad's old web engine does not draw the emoji SVG, so the PNG the
	// same Noto CDN serves is fetched instead (…/<hex>/emoji.svg -> …/<hex>/512.png).
	url := strings.Replace(libgm.GetEmojiSVG(emoji), "/emoji.svg", "/512.png", 1)
	get, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(get)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeError(w, http.StatusBadGateway, fmt.Errorf("the picture could not be fetched (%d)", resp.StatusCode))
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

// Whether the Messaging account is made by someone else. Remembered only
// until that account exists.
func (r *Relay) setExternal(external bool) {
	if r.bridge != nil {
		r.bridge.mu.Lock()
		r.bridge.external = external
		r.bridge.mu.Unlock()
	}
}

// Only this device's own programs are answered. A web page open in the
// tablet's browser could otherwise ask for a text to be sent: it cannot add
// the header asked for here without the relay's leave, which is never given.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		host := req.Host
		if i := strings.LastIndex(host, ":"); i >= 0 {
			host = host[:i]
		}
		if host != "127.0.0.1" && host != "localhost" {
			writeError(w, http.StatusForbidden, errors.New("this address is not served"))
			return
		}
		if req.Method != http.MethodGet && req.Header.Get("X-GMRelay") == "" {
			writeError(w, http.StatusForbidden, errors.New("the X-GMRelay header is needed"))
			return
		}
		next.ServeHTTP(w, req)
	})
}

// The code to scan, as a picture.
func (r *Relay) handleQR(w http.ResponseWriter, _ *http.Request) {
	r.mu.Lock()
	qr := r.qr
	r.mu.Unlock()
	if qr == "" {
		writeError(w, http.StatusNotFound, errors.New("not pairing"))
		return
	}
	png, err := qrcode.Encode(qr, qrcode.Medium, 480)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (r *Relay) handleUnpair(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	r.unpair()
	writeJSON(w, http.StatusOK, map[string]any{"unpaired": true})
}

func (r *Relay) handleConversations(w http.ResponseWriter, req *http.Request) {
	cli := r.ready(w)
	if cli == nil {
		return
	}
	count, _ := strconv.ParseInt(req.URL.Query().Get("count"), 10, 64)
	if count <= 0 || count > 200 {
		count = 50
	}
	ctx, cancel := context.WithTimeout(req.Context(), 40*time.Second)
	defer cancel()
	resp, err := cli.ListConversations(ctx, &gmproto.ListConversationsRequest{
		Count:  count,
		Folder: gmproto.ListConversationsRequest_INBOX,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	out := make([]Conversation, 0, len(resp.GetConversations()))
	r.mu.Lock()
	for _, c := range resp.GetConversations() {
		r.convs[c.GetConversationID()] = c
	}
	r.mu.Unlock()
	for _, c := range resp.GetConversations() {
		out = append(out, plainConversation(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": out})
}

func (r *Relay) handleMessages(w http.ResponseWriter, req *http.Request) {
	cli := r.ready(w)
	if cli == nil {
		return
	}
	conv := req.URL.Query().Get("conv")
	if conv == "" {
		writeError(w, http.StatusBadRequest, errors.New("conv is needed"))
		return
	}
	count, _ := strconv.ParseInt(req.URL.Query().Get("count"), 10, 64)
	if count <= 0 || count > 200 {
		count = 30
	}
	ctx, cancel := context.WithTimeout(req.Context(), 40*time.Second)
	defer cancel()
	resp, err := cli.FetchMessages(ctx, conv, count, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	out := make([]Message, 0, len(resp.GetMessages()))
	for _, m := range resp.GetMessages() {
		out = append(out, r.plainMessage(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": out, "total": resp.GetTotalMessages()})
}

func (r *Relay) handleSend(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	cli := r.ready(w)
	if cli == nil {
		return
	}
	var body struct {
		Conv string `json:"conv"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 64<<10)).Decode(&body); err != nil || body.Conv == "" || body.Text == "" {
		writeError(w, http.StatusBadRequest, errors.New("conv and text are needed"))
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), 40*time.Second)
	defer cancel()
	tmpID, err := r.sendText(ctx, cli, body.Conv, body.Text)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": true, "tmpId": tmpID})
}

// Send a text into a conversation. The id returned comes back on the message
// when the phone reports it, so the two can be matched.
func (r *Relay) sendText(ctx context.Context, cli *libgm.Client, convID, text string) (string, error) {
	return r.sendMessage(ctx, cli, convID, text, "")
}

// Send text, a file (a path on the tablet), or both. The file goes up to Google
// first and is sent as the message's first part, the text after it.
func (r *Relay) sendMessage(ctx context.Context, cli *libgm.Client, convID, text, file string) (string, error) {
	r.mu.Lock()
	conv := r.convs[convID]
	r.mu.Unlock()
	if conv == nil {
		var err error
		if conv, err = cli.GetConversation(ctx, convID); err != nil {
			return "", fmt.Errorf("the conversation could not be read: %w", err)
		}
		r.mu.Lock()
		r.convs[convID] = conv
		r.mu.Unlock()
	}
	var parts []*gmproto.MessageInfo
	if file != "" {
		media, err := uploadFile(cli, file)
		if err != nil {
			return "", err
		}
		parts = append(parts, &gmproto.MessageInfo{Data: &gmproto.MessageInfo_MediaContent{MediaContent: media}})
	}
	if text != "" {
		parts = append(parts, &gmproto.MessageInfo{Data: &gmproto.MessageInfo_MessageContent{MessageContent: &gmproto.MessageContent{Content: text}}})
	}
	tmpID := util.GenerateTmpID()
	resp, err := cli.SendMessage(ctx, &gmproto.SendMessageRequest{
		ConversationID: convID,
		MessagePayload: &gmproto.MessagePayload{
			TmpID:          tmpID,
			ConversationID: convID,
			ParticipantID:  conv.GetDefaultOutgoingID(),
			TmpID2:         tmpID,
			MessageInfo:    parts,
		},
		SIMPayload: conv.GetSimCard().GetSIMData().GetSIMPayload(),
		TmpID:      tmpID,
	})
	if err != nil {
		return "", err
	}
	if status := resp.GetStatus(); status != gmproto.SendMessageResponse_SUCCESS {
		return "", fmt.Errorf("the phone refused the message: %s", status.String())
	}
	return tmpID, nil
}

// Largest file sent: it is held in memory (and encrypted there) to upload.
const maxUpload = 50 << 20

// Mime types by extension, for files picked on the tablet.
var uploadMimes = map[string]string{
	"jpg": "image/jpeg", "jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif",
	"webp": "image/webp", "bmp": "image/bmp",
	"mp4": "video/mp4", "m4v": "video/mp4", "3gp": "video/3gpp", "mov": "video/quicktime", "webm": "video/webm",
	"mp3": "audio/mpeg", "ogg": "audio/ogg", "opus": "audio/ogg", "aac": "audio/aac", "m4a": "audio/mp4",
	"amr": "audio/amr", "wav": "audio/wav",
	"pdf": "application/pdf", "txt": "text/plain", "vcf": "text/x-vcard",
}

// Put a file from the tablet up to Google, ready to be part of a message.
func uploadFile(cli *libgm.Client, path string) (*gmproto.MediaContent, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("the attachment could not be read: %w", err)
	}
	if fi.Size() > maxUpload {
		return nil, fmt.Errorf("the attachment is too big (%d MB; at most %d)", fi.Size()>>20, maxUpload>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the attachment could not be read: %w", err)
	}
	name := filepath.Base(path)
	mime := uploadMimes[strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))]
	if mime == "" {
		mime = http.DetectContentType(data)
		if i := strings.IndexByte(mime, ';'); i >= 0 {
			mime = mime[:i]
		}
	}
	media, err := cli.UploadMedia(data, name, mime)
	if err != nil {
		return nil, fmt.Errorf("the attachment could not be uploaded: %w", err)
	}
	return media, nil
}

func (r *Relay) handleRead(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	cli := r.ready(w)
	if cli == nil {
		return
	}
	var body struct {
		Conv    string `json:"conv"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 8<<10)).Decode(&body); err != nil || body.Conv == "" || body.Message == "" {
		writeError(w, http.StatusBadRequest, errors.New("conv and message are needed"))
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), 20*time.Second)
	defer cancel()
	if err := cli.MarkRead(ctx, body.Conv, body.Message); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"read": true})
}

// What has happened since event number "since"; waits up to "wait" seconds for something.
func (r *Relay) handleEvents(w http.ResponseWriter, req *http.Request) {
	since, _ := strconv.ParseInt(req.URL.Query().Get("since"), 10, 64)
	wait, _ := strconv.Atoi(req.URL.Query().Get("wait"))
	if wait < 0 || wait > 55 {
		wait = 25
	}
	deadline := time.Now().Add(time.Duration(wait) * time.Second)
	r.evMu.Lock()
	for r.seq <= since && time.Now().Before(deadline) && req.Context().Err() == nil {
		r.evCond.Wait()
	}
	out := []Event{}
	for _, e := range r.events {
		if e.Seq > since {
			out = append(out, e)
		}
	}
	seq := r.seq
	// A client that fell further behind than is kept must start over.
	missed := len(r.events) > 0 && since > 0 && r.events[0].Seq > since+1
	r.evMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"events": out, "seq": seq, "missed": missed})
}
