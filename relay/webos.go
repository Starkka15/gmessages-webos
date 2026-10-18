package main

// The webOS side: Google Messages conversations as an account in the TouchPad's
// own Messaging app.
//
// Messaging keeps messages as objects in the system database (db8). A transport
// such as this one owns a kind of message; it adds what arrives with
// folder "inbox", and Messaging adds what the person writes with folder "outbox"
// and status "pending" for the transport to send. The system's chat threader
// groups them into conversations by address. This file is that transport. It
// talks to the database through luna-send, which every webOS device has.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

const (
	// The id database calls are made under; it owns the kinds below.
	webosID = "com.palm.stark.gmessages"
	// What Messaging knows this transport as. The account template names the same.
	webosService  = "type_gmessages"
	webosTemplate = "com.palm.stark.gmessages"
	webosProvider = "com.palm.stark.gmessages.im"

	// The message kind is named after com.palm.smsmessage on purpose: the system's chat
	// threader looks contacts up by phone number for kinds whose name starts that
	// way, and by chat address otherwise. These addresses are phone numbers.
	kindMessage = "com.palm.smsmessage.gmessages:1"
	kindCommand = webosID + ".imcommand:1"
	kindLogin   = webosID + ".imloginstate:1"

	// How much of the past is brought in when the relay connects.
	syncConversations = 30
	syncMessages      = 25
)

type bridge struct {
	r    *Relay
	jobs chan func()

	mu      sync.Mutex
	set     bool              // the kinds and the account are in place
	me      string            // this phone's own number, the account's username
	account string            // the account's id in the database
	byAddr  map[string]string // an address Messaging uses -> conversation id
	synced  bool
	// The account is made by the Accounts app (pairing was started from its page),
	// so the relay waits for it instead of making one.
	external bool
}

func newBridge(r *Relay) *bridge {
	b := &bridge{r: r, jobs: make(chan func(), 256), byAddr: map[string]string{}}
	// One worker, so database work happens in the order things arrived.
	go func() {
		for job := range b.jobs {
			job()
		}
	}()
	go b.watchOutbox()
	return b
}

func (b *bridge) do(job func()) {
	select {
	case b.jobs <- job:
	default:
		b.r.log.Warn().Msg("the database queue is full; an update was dropped")
	}
}

// ---- luna-send ----

// Call a service on the system bus and return its answers (one per line).
func luna(ctx context.Context, as string, answers int, uri string, payload any) ([]map[string]any, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/luna-send", "-n", fmt.Sprint(answers), "-a", as, uri, string(data))
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err = cmd.Run(); err != nil {
		return nil, fmt.Errorf("luna-send %s: %w %s", uri, err, strings.TrimSpace(errOut.String()))
	}
	var results []map[string]any
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var answer map[string]any
		if err = json.Unmarshal([]byte(line), &answer); err != nil {
			return nil, fmt.Errorf("luna-send %s answered with something that is not JSON: %.120s", uri, line)
		}
		results = append(results, answer)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("luna-send %s gave no answer", uri)
	}
	return results, nil
}

// One call, one answer, and an error if the service said no.
func call(uri string, payload any) (map[string]any, error) {
	return callAs(webosID, uri, payload)
}

// The same, made under another id: a few records belong to the system's own
// services and can only be changed in their name.
func callAs(as, uri string, payload any) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	answers, err := luna(ctx, as, 1, uri, payload)
	if err != nil {
		return nil, err
	}
	if ok, _ := answers[0]["returnValue"].(bool); !ok {
		return answers[0], fmt.Errorf("%s refused: %v", uri, answers[0]["errorText"])
	}
	return answers[0], nil
}

func dbFind(query map[string]any) ([]map[string]any, error) {
	answer, err := call("palm://com.palm.db/find", map[string]any{"query": query})
	if err != nil {
		return nil, err
	}
	list, _ := answer["results"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if obj, ok := item.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out, nil
}

func where(prop string, val any) map[string]any {
	return map[string]any{"prop": prop, "op": "=", "val": val}
}

// ---- setting up: the kinds and the account ----

func (b *bridge) setup(me string) error {
	b.mu.Lock()
	if b.set {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()

	kinds := []map[string]any{
		{
			"id": kindMessage, "owner": webosID, "extends": []string{"com.palm.immessage:1"}, "sync": false,
			"indexes": []map[string]any{
				{"name": "outgoing", "props": []map[string]any{{"name": "folder"}, {"name": "status"}}},
				{"name": "gmId", "props": []map[string]any{{"name": "gmId"}}},
				{"name": "gmTmpId", "props": []map[string]any{{"name": "gmTmpId"}}},
				{"name": "gmConv", "props": []map[string]any{{"name": "gmConv"}}},
			},
		},
		{"id": kindCommand, "owner": webosID, "extends": []string{"com.palm.imcommand:1"}, "sync": false},
		{"id": kindLogin, "owner": webosID, "extends": []string{"com.palm.imloginstate:1"}, "sync": false},
	}
	for _, kind := range kinds {
		if _, err := call("palm://com.palm.db/putKind", kind); err != nil {
			return fmt.Errorf("the %v kind could not be registered: %w", kind["id"], err)
		}
	}
	// Messaging writes and reads these; the threader and the contact linker read and mark them.
	var perms []map[string]any
	all := map[string]string{"create": "allow", "read": "allow", "update": "allow", "delete": "allow"}
	for _, kind := range []string{kindMessage, kindCommand, kindLogin} {
		for _, caller := range []string{"com.palm.app.messaging", "com.palm.messaging.chatthreader", "com.palm.service.contacts.linker", "com.palm.*"} {
			perms = append(perms, map[string]any{"type": "db.kind", "object": kind, "caller": caller, "operations": all})
		}
	}
	if _, err := call("palm://com.palm.db/putPermissions", map[string]any{"permissions": perms}); err != nil {
		return fmt.Errorf("permissions could not be set: %w", err)
	}

	account, err := b.ensureAccount(me)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.set, b.me, b.account, b.external = true, me, account, false
	b.mu.Unlock()
	b.watchAccount(account)
	b.r.log.Info().Str("account", account).Msg("the Messaging account is in place")
	return nil
}

func (b *bridge) ensureAccount(me string) (string, error) {
	find := func() (string, error) {
		answer, err := call("palm://com.palm.service.accounts/listAccounts", map[string]any{"templateId": webosTemplate})
		if err != nil {
			return "", err
		}
		list, _ := answer["results"].([]any)
		for _, item := range list {
			if acct, ok := item.(map[string]any); ok && acct["templateId"] == webosTemplate {
				id, _ := acct["_id"].(string)
				return id, nil
			}
		}
		return "", nil
	}
	if id, err := find(); err != nil || id != "" {
		return id, err
	}
	b.mu.Lock()
	external := b.external
	b.mu.Unlock()
	if external {
		// The person is on the Accounts page, about to tap Create: give them a few minutes.
		for i := 0; i < 60; i++ {
			time.Sleep(5 * time.Second)
			if id, err := find(); err != nil || id != "" {
				return id, err
			}
		}
		return "", errors.New("the account was not created on the Accounts page")
	}
	_, err := call("palm://com.palm.service.accounts/createAccount", map[string]any{
		"templateId":          webosTemplate,
		"username":            me,
		"alias":               "Google Messages",
		"capabilityProviders": []map[string]any{{"id": webosProvider}},
		// An account whose template has a sign-in page must come with credentials; the
		// real ones are the relay's session, so this only says there are some.
		"credentials": map[string]any{"common": map[string]any{"paired": "true"}},
	})
	if err != nil {
		return "", fmt.Errorf("the account could not be made (is the account template installed?): %w", err)
	}
	id, err := find()
	if err == nil && id == "" {
		err = errors.New("the account was made but cannot be found")
	}
	return id, err
}

// Removing the account on the Accounts page is how the person says they are
// done: when it goes, unpair from the phone and clear the conversations.
func (b *bridge) watchAccount(account string) {
	go func() {
		for {
			time.Sleep(45 * time.Second)
			b.mu.Lock()
			still := b.set && b.account == account
			b.mu.Unlock()
			if !still {
				return
			}
			answer, err := call("palm://com.palm.service.accounts/listAccounts", map[string]any{"templateId": webosTemplate})
			if err != nil {
				continue // the accounts service is busy or restarting; ask again later
			}
			found := false
			list, _ := answer["results"].([]any)
			for _, item := range list {
				if acct, ok := item.(map[string]any); ok && acct["_id"] == account && acct["beingDeleted"] != true {
					found = true
				}
			}
			if !found {
				b.r.log.Info().Msg("the account was removed; unpairing")
				b.r.unpair()
				return
			}
		}
	}()
}

// The pairing is over: take this account's conversations out of Messaging.
func (b *bridge) forget() {
	b.mu.Lock()
	was, account := b.set, b.account
	b.set, b.synced, b.account, b.me = false, false, "", ""
	b.byAddr = map[string]string{}
	b.mu.Unlock()
	if !was {
		return
	}
	if _, err := call("palm://com.palm.db/del", map[string]any{"query": map[string]any{"from": kindMessage}, "purge": true}); err != nil {
		b.r.log.Warn().Err(err).Msg("the messages could not be removed")
	}
	if _, err := call("palm://com.palm.db/del", map[string]any{"query": map[string]any{"from": kindLogin}, "purge": true}); err != nil {
		b.r.log.Warn().Err(err).Msg("the sign-in state could not be removed")
	}
	// The account goes too, unless its going is what brought us here.
	if account != "" {
		_, _ = call("palm://com.palm.service.accounts/deleteAccount", map[string]any{"accountId": account})
	}
	// Conversations left with nothing in them are the threader's to clear; it does so
	// when it sees their messages gone.
}

// Tell Messaging whether this account is connected.
func (b *bridge) setOnline(online bool) {
	b.do(func() {
		b.mu.Lock()
		set, me, account := b.set, b.me, b.account
		b.mu.Unlock()
		if !set {
			return
		}
		state, availability := "offline", 4
		if online {
			state, availability = "online", 0
		}
		obj := map[string]any{
			"_kind": kindLogin, "accountId": account, "serviceName": webosService, "username": me,
			"state": state, "availability": availability,
		}
		found, err := dbFind(map[string]any{"from": kindLogin})
		if err == nil && len(found) > 0 {
			obj["_id"] = found[0]["_id"]
			_, err = call("palm://com.palm.db/merge", map[string]any{"objects": []any{obj}})
		} else if err == nil {
			_, err = call("palm://com.palm.db/put", map[string]any{"objects": []any{obj}})
		}
		if err != nil {
			b.r.log.Warn().Err(err).Msg("the sign-in state could not be written")
		}
	})
}

// ---- addresses ----

// Messaging knows a conversation by an address. A one-to-one conversation is
// the other person's number; a group has no number, so it gets a made-up one.
func convAddress(c *gmproto.Conversation) (addr, name string) {
	name = c.GetName()
	if !c.GetIsGroupChat() {
		for _, p := range c.GetParticipants() {
			if !p.GetIsMe() && p.GetID().GetNumber() != "" {
				if name == "" {
					name = p.GetFullName()
				}
				return p.GetID().GetNumber(), name
			}
		}
	}
	return "group-" + c.GetConversationID(), name
}

// The form addresses are compared in: a number down to its last ten digits.
func addrKey(addr string) string {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if strings.HasPrefix(addr, "group-") {
		return addr
	}
	digits := make([]byte, 0, len(addr))
	for i := 0; i < len(addr); i++ {
		if addr[i] >= '0' && addr[i] <= '9' {
			digits = append(digits, addr[i])
		}
	}
	if len(digits) < 5 {
		return addr // a short code or a name, not a number
	}
	if len(digits) > 10 {
		digits = digits[len(digits)-10:]
	}
	return string(digits)
}

func (b *bridge) remember(c *gmproto.Conversation) {
	addr, _ := convAddress(c)
	b.mu.Lock()
	b.byAddr[addrKey(addr)] = c.GetConversationID()
	b.mu.Unlock()
}

func myNumber(convs []*gmproto.Conversation) string {
	for _, c := range convs {
		for _, p := range c.GetParticipants() {
			if p.GetIsMe() && p.GetID().GetNumber() != "" {
				return p.GetID().GetNumber()
			}
		}
	}
	return ""
}

// ---- names ----

// The id of the system's chat threader, which owns the conversation records.
const threaderID = "com.palm.messaging.chatthreader"

// Whether a name says more than the number it stands for.
func realName(name, addr string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	for _, c := range name {
		if !strings.ContainsRune("+0123456789 ()-.", c) {
			return true
		}
	}
	return false
}

// Give conversations the names the phone has for them. The threader names a
// conversation after a contact on the tablet, or failing that after its number;
// the name that came with the messages is not used. So where there is no contact
// and the phone does know a name, it is put on the conversation here.
func (b *bridge) nameThreads(convs []*gmproto.Conversation) {
	named := 0
	for _, c := range convs {
		addr, name := convAddress(c)
		if !realName(name, addr) {
			continue
		}
		threads, err := dbFind(map[string]any{
			"from": "com.palm.chatthread:1", "where": []any{where("normalizedAddress", strings.ToLower(strings.TrimSpace(addr)))},
		})
		if err != nil {
			b.r.log.Warn().Err(err).Msg("conversations could not be searched")
			return
		}
		for _, thread := range threads {
			// One linked to a contact keeps the contact's name.
			if person, _ := thread["personId"].(string); person != "" || thread["replyService"] != webosService || thread["displayName"] == name {
				continue
			}
			_, err = callAs(threaderID, "palm://com.palm.db/merge", map[string]any{
				"objects": []any{map[string]any{"_id": thread["_id"], "displayName": name}},
			})
			if err != nil {
				b.r.log.Warn().Err(err).Msg("a conversation could not be named")
			} else {
				named++
			}
		}
	}
	if named > 0 {
		b.r.log.Info().Int("named", named).Msg("conversations given their names")
	}
}

// Name conversations once the threader has had time to make them.
func (b *bridge) nameSoon(convs []*gmproto.Conversation, waits ...time.Duration) {
	go func() {
		for _, wait := range waits {
			time.Sleep(wait)
			b.do(func() { b.nameThreads(convs) })
		}
	}()
}

// ---- messages into the database ----

// Where downloaded pictures and videos are kept on the tablet.
const mediaRoot = "/media/internal/Google Messages"

// A filename extension the Messaging app will recognise, from the mime type, or
// failing that the media's own name, or "bin". The extension is what tells the
// app to show a picture inline versus a video/audio chip.
func mediaExt(mime, name string) string {
	switch strings.ToLower(mime) {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	case "image/bmp":
		return "bmp"
	case "video/mp4":
		return "mp4"
	case "video/3gpp", "video/3gp":
		return "3gp"
	case "video/webm":
		return "webm"
	case "video/quicktime":
		return "mov"
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/ogg", "audio/opus":
		return "ogg"
	case "audio/aac", "audio/mp4", "audio/m4a", "audio/x-m4a":
		return "aac"
	case "audio/amr":
		return "amr"
	case "audio/wav", "audio/x-wav":
		return "wav"
	}
	ext := ""
	if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
		for i := dot + 1; i < len(name) && len(ext) < 5; i++ {
			c := name[i] | 0x20 // lower-case
			if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
				ext += string(c)
			}
		}
	}
	if ext != "" {
		return ext
	}
	return "bin"
}

// Keep a conversation or message id safe to use as a file name.
func safeName(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "x"
	}
	return string(out)
}

// Fetch a message's first real attachment and save it on the tablet, returning
// the file's path. "" when there is none or it could not be fetched; a failure
// here never stops the message being stored (it keeps its "[Picture]" text).
// Media bytes and keys are never logged.
func (b *bridge) mediaPath(m Message) string {
	var md Media
	for _, x := range m.Media {
		if x.ID != "" || x.ThumbID != "" || len(x.Data) > 0 {
			md = x
			break
		}
	}
	if md.ID == "" && md.ThumbID == "" && len(md.Data) == 0 {
		if len(m.Media) > 0 {
			// Usually the phone is still fetching it; a later update brings it.
			b.r.log.Info().Str("status", m.Status).Msg("an attachment has nothing to fetch yet")
		}
		return ""
	}
	dir := filepath.Join(mediaRoot, safeName(m.Conv))
	path := filepath.Join(dir, safeName(m.ID)+"."+mediaExt(md.Mime, md.Name))
	if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
		return path // already have it
	}
	// The full picture if it is there, else the thumbnail, else bytes sent inline —
	// the order the Messages web client itself uses.
	var rc io.ReadCloser
	var err error
	if md.ID != "" || md.ThumbID != "" {
		b.r.mu.Lock()
		cli := b.r.cli
		b.r.mu.Unlock()
		if cli == nil {
			return ""
		}
		if md.ID != "" {
			rc, err = cli.DownloadMedia(md.ID, md.Key)
		} else {
			rc, err = cli.DownloadMedia(md.ThumbID, md.ThumbKey)
		}
	} else {
		rc = io.NopCloser(bytes.NewReader(md.Data))
	}
	if err != nil {
		b.r.log.Warn().Err(err).Msg("a message's media could not be fetched")
		return ""
	}
	defer rc.Close()
	if err = os.MkdirAll(dir, 0o755); err != nil {
		b.r.log.Warn().Err(err).Msg("the media folder could not be made")
		return ""
	}
	f, err := os.Create(path)
	if err != nil {
		b.r.log.Warn().Err(err).Msg("the media file could not be made")
		return ""
	}
	_, err = io.Copy(f, rc)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		b.r.log.Warn().Err(err).Msg("the media could not be saved")
		return ""
	}
	return path
}

// A message as Messaging stores one. old: it is history, not news. filePath: a
// saved picture/video/audio file to show, or "" to note attachments in the text.
func (b *bridge) object(m Message, conv *gmproto.Conversation, old bool, filePath string) map[string]any {
	addr, name := convAddress(conv)
	text := m.Text
	if filePath == "" {
		for _, media := range m.Media {
			label := "[Attachment]"
			switch {
			case strings.HasPrefix(media.Mime, "image/"):
				label = "[Picture]"
			case strings.HasPrefix(media.Mime, "video/"):
				label = "[Video]"
			case strings.HasPrefix(media.Mime, "audio/"):
				label = "[Voice message]"
			}
			if text != "" {
				text += "\n"
			}
			text += label
		}
	}
	b.mu.Lock()
	me := b.me
	b.mu.Unlock()
	obj := map[string]any{
		"_kind":          kindMessage,
		"serviceName":    webosService,
		"username":       me,
		"messageText":    text,
		"localTimestamp": m.Time,
		"timestamp":      m.Time,
		"gmId":           m.ID,
		"gmConv":         m.Conv,
	}
	if filePath != "" {
		// The Messaging app shows an image inline, or a tappable chip for video
		// and audio, from this path (it adds the file:// itself).
		obj["filePath"] = filePath
	}
	if m.FromMe {
		obj["folder"] = "outbox"
		obj["status"] = "successful"
		if strings.Contains(m.Status, "FAILED") {
			obj["status"] = "failed"
		}
		obj["from"] = map[string]any{"addr": me}
		obj["to"] = []any{map[string]any{"addr": addr, "name": name}}
		obj["flags"] = map[string]any{"read": true}
	} else {
		// In a group, Messaging shows one name for the whole conversation: say who spoke.
		if conv.GetIsGroupChat() && m.Sender != "" {
			obj["messageText"] = m.Sender + ": " + text
		}
		obj["folder"] = "inbox"
		obj["status"] = "successful"
		obj["from"] = map[string]any{"addr": addr, "name": name}
		obj["to"] = []any{map[string]any{"addr": me}}
		if old {
			obj["flags"] = map[string]any{"read": true, "noNotification": true}
		}
	}
	return obj
}

// Whether a message is one to show: not an empty system note.
func showable(m Message) bool {
	return m.ID != "" && (m.Text != "" || len(m.Media) > 0) && !strings.Contains(m.Status, "TOMBSTONE")
}

func (b *bridge) conversation(id string) *gmproto.Conversation {
	b.r.mu.Lock()
	conv := b.r.convs[id]
	cli := b.r.cli
	b.r.mu.Unlock()
	if conv != nil || cli == nil {
		return conv
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conv, err := cli.GetConversation(ctx, id)
	if err != nil {
		b.r.log.Warn().Err(err).Msg("a conversation could not be read")
		return nil
	}
	b.r.mu.Lock()
	b.r.convs[id] = conv
	b.r.mu.Unlock()
	b.remember(conv)
	return conv
}

// A message arrived or changed while connected.
func (b *bridge) message(m Message) {
	b.do(func() {
		b.mu.Lock()
		set := b.set
		b.mu.Unlock()
		if !set || !showable(m) {
			return
		}
		// One written on this tablet: Messaging's own object is the record of it.
		if m.TmpID != "" {
			if mine, err := dbFind(map[string]any{"from": kindMessage, "where": []any{where("gmTmpId", m.TmpID)}, "limit": 1}); err == nil && len(mine) > 0 {
				b.merge(mine[0]["_id"], map[string]any{"gmId": m.ID, "status": b.status(m)})
				return
			}
		}
		found, err := dbFind(map[string]any{"from": kindMessage, "where": []any{where("gmId", m.ID)}, "limit": 1})
		if err != nil {
			b.r.log.Warn().Err(err).Msg("the database could not be searched")
			return
		}
		if len(found) > 0 {
			// Seen before: its delivery status can have changed, and a picture
			// the phone was still fetching (stored as "[Picture]") can have arrived.
			props := map[string]any{}
			if m.FromMe && found[0]["status"] != b.status(m) {
				props["status"] = b.status(m)
			}
			if fp, _ := found[0]["filePath"].(string); fp == "" && len(m.Media) > 0 {
				if filePath := b.mediaPath(m); filePath != "" {
					if conv := b.conversation(m.Conv); conv != nil {
						props["filePath"] = filePath
						props["messageText"] = b.object(m, conv, true, filePath)["messageText"]
					}
				}
			}
			if len(props) > 0 {
				b.merge(found[0]["_id"], props)
			}
			return
		}
		conv := b.conversation(m.Conv)
		if conv == nil {
			return
		}
		// News only if it is recent; a phone catching up sends old ones too.
		old := time.Since(time.UnixMilli(m.Time)) > 10*time.Minute
		// A live message fetches its picture or video now.
		filePath := b.mediaPath(m)
		if _, err = call("palm://com.palm.db/put", map[string]any{"objects": []any{b.object(m, conv, old, filePath)}}); err != nil {
			b.r.log.Warn().Err(err).Msg("a message could not be stored")
			return
		}
		// A first message makes a new conversation, which wants its name.
		b.nameSoon([]*gmproto.Conversation{conv}, 8*time.Second)
	})
}

func (b *bridge) status(m Message) string {
	if strings.Contains(m.Status, "FAILED") {
		return "failed"
	}
	return "successful"
}

func (b *bridge) merge(id any, props map[string]any) {
	props["_id"] = id
	if _, err := call("palm://com.palm.db/merge", map[string]any{"objects": []any{props}}); err != nil {
		b.r.log.Warn().Err(err).Msg("a message could not be updated")
	}
}

// Connected: put the account in place and bring in recent history.
func (b *bridge) connected() {
	b.do(func() {
		b.r.mu.Lock()
		cli := b.r.cli
		b.r.mu.Unlock()
		if cli == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		resp, err := cli.ListConversations(ctx, &gmproto.ListConversationsRequest{
			Count: syncConversations, Folder: gmproto.ListConversationsRequest_INBOX,
		})
		cancel()
		if err != nil {
			b.r.log.Warn().Err(err).Msg("conversations could not be listed")
			return
		}
		convs := resp.GetConversations()
		b.r.mu.Lock()
		for _, c := range convs {
			b.r.convs[c.GetConversationID()] = c
		}
		b.r.mu.Unlock()
		for _, c := range convs {
			b.remember(c)
		}
		me := myNumber(convs)
		if me == "" {
			me = "Google Messages"
		}
		b.mu.Lock()
		b.me = me
		b.mu.Unlock()
		if err = b.setup(me); err != nil {
			b.r.log.Err(err).Msg("Messaging could not be set up")
			return
		}
		b.setOnline(true)
		stored := 0
		// The conversation written to longest ago goes in first, for the same reason.
		for i := len(convs) - 1; i >= 0; i-- {
			stored += b.syncConversation(cli, convs[i])
		}
		b.mu.Lock()
		b.synced = true
		b.mu.Unlock()
		b.r.log.Info().Int("conversations", len(convs)).Int("stored", stored).Msg("history is in Messaging")
		// The threader takes about a minute over a full history.
		b.nameSoon(convs, 5*time.Second, 60*time.Second, 180*time.Second)
	})
}

// Store the recent messages of one conversation that are not stored yet.
func (b *bridge) syncConversation(cli interface {
	FetchMessages(ctx context.Context, conversationID string, count int64, cursor *gmproto.Cursor) (*gmproto.ListMessagesResponse, error)
}, c *gmproto.Conversation) int {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	resp, err := cli.FetchMessages(ctx, c.GetConversationID(), syncMessages, nil)
	cancel()
	if err != nil {
		b.r.log.Warn().Err(err).Msg("a conversation's messages could not be read")
		return 0
	}
	have := map[string]bool{}
	noFile := map[string]any{} // gmId → _id of stored messages without a saved attachment
	found, err := dbFind(map[string]any{
		"from": kindMessage, "where": []any{where("gmConv", c.GetConversationID())}, "select": []string{"_id", "gmId", "filePath"}, "limit": 500,
	})
	if err != nil {
		b.r.log.Warn().Err(err).Msg("the database could not be searched")
		return 0
	}
	for _, obj := range found {
		if id, ok := obj["gmId"].(string); ok {
			have[id] = true
			if fp, _ := obj["filePath"].(string); fp == "" {
				noFile[id] = obj["_id"]
			}
		}
	}
	// A picture stored as "[Picture]" (the phone was still fetching it, the
	// download failed, or it predates fetching all history) is fetched now.
	fetched := 0
	for _, raw := range resp.GetMessages() {
		m := b.r.plainMessage(raw)
		dbID, ok := noFile[m.ID]
		if !ok || len(m.Media) == 0 {
			continue
		}
		if filePath := b.mediaPath(m); filePath != "" {
			b.merge(dbID, map[string]any{"filePath": filePath, "messageText": b.object(m, c, true, filePath)["messageText"]})
			fetched++
		}
	}
	if fetched > 0 {
		b.r.log.Info().Int("pictures", fetched).Msg("stored messages got their attachments")
	}
	// Oldest first: the threader takes them in the order they were stored, and the
	// last one it sees becomes the conversation's summary and its place in the list.
	var fresh []Message
	for _, raw := range resp.GetMessages() {
		m := b.r.plainMessage(raw)
		if showable(m) && !have[m.ID] {
			fresh = append(fresh, m)
		}
	}
	sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].Time < fresh[j].Time })
	objects := make([]any, 0, len(fresh))
	for _, m := range fresh {
		// Every attachment in the synced history is fetched (at most syncMessages
		// per conversation, and each only once: a saved file is kept).
		filePath := b.mediaPath(m)
		objects = append(objects, b.object(m, c, true, filePath))
	}
	if len(objects) == 0 {
		return 0
	}
	if _, err = call("palm://com.palm.db/put", map[string]any{"objects": objects}); err != nil {
		b.r.log.Warn().Err(err).Msg("history could not be stored")
		return 0
	}
	return len(objects)
}

// ---- messages written in Messaging, on their way out ----

// Wait for Messaging to add something to send, forever.
func (b *bridge) watchOutbox() {
	query := map[string]any{"from": kindMessage, "where": []any{where("folder", "outbox"), where("status", "pending")}}
	for {
		b.mu.Lock()
		set := b.set
		b.mu.Unlock()
		if !set {
			time.Sleep(3 * time.Second)
			continue
		}
		b.do(b.sendPending)
		// The watch answers once at the start and again when the query has results.
		// Looked at again every few minutes regardless, in case a watch is lost.
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		_, _ = luna(ctx, webosID, 2, "palm://com.palm.db/watch", map[string]any{"query": query})
		cancel()
		time.Sleep(300 * time.Millisecond)
	}
}

func (b *bridge) sendPending() {
	pending, err := dbFind(map[string]any{"from": kindMessage, "where": []any{where("folder", "outbox"), where("status", "pending")}})
	if err != nil {
		b.r.log.Warn().Err(err).Msg("the outbox could not be read")
		return
	}
	for _, obj := range pending {
		text, _ := obj["messageText"].(string)
		addr := ""
		if to, ok := obj["to"].([]any); ok && len(to) > 0 {
			if first, ok := to[0].(map[string]any); ok {
				addr, _ = first["addr"].(string)
			}
		}
		tmpID, conv, err := b.send(addr, text)
		if err != nil {
			b.r.log.Warn().Err(err).Msg("a message could not be sent")
			b.merge(obj["_id"], map[string]any{"status": "failed", "errorCode": 1, "gmError": err.Error()})
			continue
		}
		b.merge(obj["_id"], map[string]any{"status": "successful", "gmTmpId": tmpID, "gmConv": conv})
	}
}

func (b *bridge) send(addr, text string) (tmpID, conv string, err error) {
	if addr == "" || text == "" {
		return "", "", errors.New("nothing to send, or no one to send it to")
	}
	b.r.mu.Lock()
	cli, state := b.r.cli, b.r.state
	b.r.mu.Unlock()
	if cli == nil || (state != "ready" && state != "phoneAway") {
		return "", "", errors.New("not connected to the phone")
	}
	b.mu.Lock()
	conv = b.byAddr[addrKey(addr)]
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if conv == "" {
		if strings.HasPrefix(addr, "group-") {
			return "", "", errors.New("that group is not known any more")
		}
		// Someone not written to before: the phone starts the conversation.
		resp, err := cli.GetOrCreateConversation(ctx, &gmproto.GetOrCreateConversationRequest{
			Numbers: []*gmproto.ContactNumber{{MysteriousInt: 2, Number: addr, Number2: addr}},
		})
		if err != nil {
			return "", "", fmt.Errorf("a conversation could not be started: %w", err)
		}
		c := resp.GetConversation()
		if c.GetConversationID() == "" {
			return "", "", errors.New("the phone would not start a conversation with that number")
		}
		conv = c.GetConversationID()
		b.r.mu.Lock()
		b.r.convs[conv] = c
		b.r.mu.Unlock()
		b.remember(c)
	}
	tmpID, err = b.r.sendText(ctx, cli, conv, text)
	return tmpID, conv, err
}
