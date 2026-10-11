package main

// The Atlas browser's Google sign-in, read from its cookie database, so the
// relay can pair from the TouchPad alone. Atlas is a current web engine and
// can sign in to Google; the cookies pairing needs live in its cookies.db.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Where Atlas keeps its cookies: a stable symlink into the app's data dir.
const atlasCookies = "/var/atlas252/webkit-data/cookies.db"

// The cookies that sign a pairing in. OSID and __Secure-1PSIDTS are carried
// along when Atlas has them, but are never required.
var atlasWant = []string{"SID", "HSID", "SSID", "APISID", "SAPISID"}

// Name and value are joined by this when sqlite3 prints a row. A literal string
// works on any sqlite version (the device's is too old for char()); it does not
// appear in a cookie name or a Google cookie value.
const cookieSep = "~|~"

// The Google cookies Atlas holds, as name -> value. The five in atlasWant
// must all be there; the error says what the person has to do, not what the
// database said. Cookie values are never logged.
func atlasCookiesRead() (map[string]string, error) {
	cookies, err := readCookieDB(atlasCookies)
	if err != nil {
		// The database can be locked by a running Atlas: try a copy instead.
		cookies, err = readCookieCopy()
		if err != nil {
			return nil, errors.New("the Atlas browser's cookies could not be read on this TouchPad")
		}
	}
	for _, name := range atlasWant {
		if cookies[name] == "" {
			return nil, errors.New("sign in to your Google account in the Atlas browser first")
		}
	}
	return cookies, nil
}

// Ask the device's sqlite3 for the Google cookies. The name and the value are
// read as two columns, joined by a tab: values hold "=", ";" and "/", so they
// must not be parsed as a cookie header.
func readCookieDB(path string) (map[string]string, error) {
	// The SID family signs in; modern Google web auth also wants the __Secure-*PSID
	// session cookies, so take those too. messages.google.com rejects the config
	// fetch (401) with only the legacy SID. OSID is taken when present but the
	// account need not have it.
	query := "SELECT name || '" + cookieSep + "' || value FROM moz_cookies WHERE host LIKE '%google.com' AND name IN " +
		"('SID','HSID','SSID','APISID','SAPISID','OSID'," +
		"'__Secure-1PSID','__Secure-3PSID','__Secure-1PAPISID','__Secure-3PAPISID'," +
		"'__Secure-1PSIDCC','__Secure-3PSIDCC','__Secure-1PSIDTS','__Secure-3PSIDTS');"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// This device's sqlite3 does not accept "file:" URIs, so the path is passed
	// plainly. A SELECT takes only a shared lock and writes nothing back; if a
	// writing Atlas is holding the file, the caller reads a copy instead.
	cmd := exec.CommandContext(ctx, "/usr/bin/sqlite3", path, query)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("sqlite3: %w %s", err, strings.TrimSpace(errOut.String()))
	}
	return parseCookieRows(out.String())
}

// The rows sqlite3 gave, as name -> value.
func parseCookieRows(out string) (map[string]string, error) {
	cookies := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		sep := strings.Index(line, cookieSep)
		if sep < 0 {
			return nil, errors.New("the cookie database gave an unexpected answer")
		}
		cookies[line[:sep]] = line[sep+len(cookieSep):]
	}
	return cookies, nil
}

// Copy the database (and its WAL side files, when there are any) to /tmp and
// read the copy, so a locked database does not stop the pairing. The copy is
// removed afterwards, whatever happened.
func readCookieCopy() (map[string]string, error) {
	tmp := fmt.Sprintf("/tmp/cookies-%d.db", time.Now().UnixNano())
	if err := copyFile(atlasCookies, tmp); err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	for _, side := range []string{"-journal", "-wal", "-shm"} {
		if _, err := os.Stat(atlasCookies + side); err == nil {
			if err = copyFile(atlasCookies+side, tmp+side); err != nil {
				return nil, err
			}
			defer os.Remove(tmp + side)
		}
	}
	return readCookieDB(tmp)
}

func copyFile(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(to)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	if closeErr := dst.Close(); err == nil {
		err = closeErr
	}
	return err
}
