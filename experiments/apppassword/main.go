// An experiment: can a Google app password stand in for a browser's sign-in
// cookies? Google Messages pairing needs web cookies (SID and friends). An app
// password cannot sign in to a web page, but it has been able to sign in the
// way an Android device does; this tries that, then the exchange Chrome uses
// to turn an account token into web cookies.
//
//	apppassword <file>
//
// The file's first line is the Google account's email address, the second an
// app password. Nothing is printed but which step worked and the names (never
// the values) of the cookies obtained.
package main

import (
	"bufio"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

const authURL = "https://android.clients.google.com/auth"
const clientSig = "38918a453d07199354f8b19af05ec6562ced5788"

func androidAuth(client *http.Client, form url.Values) (map[string]string, error) {
	req, _ := http.NewRequest(http.MethodPost, authURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "GoogleAuth/1.4 (generic JOP24G)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	out := map[string]string{"_status": resp.Status}
	for _, line := range strings.Split(string(body), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			out[key] = value
		}
	}
	return out, nil
}

// Google's public key for sign-ins from Android devices, as Android carries it:
// a length, the modulus, a length, the exponent.
const androidKey = "AAAAgMom/1a/v0lblO2Ubrt60J2gcuXSljGFQXgcyZWveWLEwo6prwgi3iJIZdodyhKZQrNWp5nKJ3srRXcUW+F1BD3baEVGcmEgqaLZUNBjm057pKRI16kB0YppeGx5qIQ5QjKzsR8ETQbKLNWgRY0QRNVz34kMJR3P/LgHax/6rmf5AAAAAwEAAQ=="

// The email and password sealed the way an Android device sends them.
func sealed(email, password string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(androidKey)
	if err != nil || len(key) < 8 {
		return "", fmt.Errorf("the key constant is damaged")
	}
	n := int(binary.BigEndian.Uint32(key[:4]))
	if len(key) < 4+n+4 {
		return "", fmt.Errorf("the key constant is damaged")
	}
	modulus := new(big.Int).SetBytes(key[4 : 4+n])
	e := int(binary.BigEndian.Uint32(key[4+n : 8+n]))
	exponent := new(big.Int).SetBytes(key[8+n : 8+n+e])
	pub := &rsa.PublicKey{N: modulus, E: int(exponent.Int64())}
	cipher, err := rsa.EncryptOAEP(sha1.New(), rand.Reader, pub, []byte(email+"\x00"+password), nil)
	if err != nil {
		return "", err
	}
	digest := sha1.Sum(key)
	out := append([]byte{0}, digest[:4]...)
	out = append(out, cipher...)
	return base64.URLEncoding.EncodeToString(out), nil
}

func fail(step, why string) {
	fmt.Printf("STEP %s: FAILED: %s\n", step, why)
	os.Exit(1)
}

func main() {
	if len(os.Args) != 2 {
		fmt.Println("usage: apppassword <file with the email on line 1 and an app password on line 2>")
		os.Exit(2)
	}
	file, err := os.Open(os.Args[1])
	if err != nil {
		fail("0 read the file", err.Error())
	}
	scan := bufio.NewScanner(file)
	var lines []string
	for scan.Scan() {
		if line := strings.TrimSpace(scan.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	file.Close()
	if len(lines) < 2 {
		fail("0 read the file", "two lines are needed: the email address, then the app password")
	}
	email, password := lines[0], strings.ReplaceAll(lines[1], " ", "")

	idBytes := make([]byte, 8)
	_, _ = rand.Read(idBytes)
	androidID := hex.EncodeToString(idBytes)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 30 * time.Second, Jar: jar}

	common := func() url.Values {
		return url.Values{
			"accountType":     {"HOSTED_OR_GOOGLE"},
			"Email":           {email},
			"has_permission":  {"1"},
			"source":          {"android"},
			"androidId":       {androidID},
			"device_country":  {"us"},
			"operatorCountry": {"us"},
			"lang":            {"en"},
			"sdk_version":     {"17"},
			"client_sig":      {clientSig},
			"callerSig":       {clientSig},
		}
	}

	// 1. Sign in as an Android device would, with the app password. Google is
	// particular about how this is asked, so it is tried a few ways.
	old := &http.Client{Timeout: 30 * time.Second, Jar: jar, Transport: &http.Transport{
		// As older Android does: no TLS 1.3, no HTTP/2.
		TLSClientConfig: &tls.Config{MaxVersion: tls.VersionTLS12},
		TLSNextProto:    map[string]func(string, *tls.Conn) http.RoundTripper{},
	}}
	phone := &http.Client{Timeout: 40 * time.Second, Jar: jar, Transport: androidTransport{}}
	master := ""
	var answer map[string]string
	for _, try := range []struct {
		name   string
		client *http.Client
		seal   bool
	}{{"plain, modern connection", client, false}, {"sealed, modern connection", client, true},
		{"plain, older connection", old, false}, {"sealed, older connection", old, true},
		{"sealed, a phone's connection", phone, true}, {"plain, a phone's connection", phone, false}} {
		form := common()
		form.Set("add_account", "1")
		form.Set("service", "ac2dm")
		form.Set("droidguard_results", "dummy123")
		if try.seal {
			enc, err := sealed(email, password)
			if err != nil {
				fail("1 device sign-in", err.Error())
			}
			form.Set("EncryptedPasswd", enc)
		} else {
			form.Set("Passwd", password)
		}
		answer, err = androidAuth(try.client, form)
		if err != nil {
			fmt.Printf("  %s: %s\n", try.name, err)
			continue
		}
		fmt.Printf("  %s: %s Error=%s %s\n", try.name, answer["_status"], answer["Error"], answer["ErrorDetail"])
		if answer["Token"] != "" {
			master = answer["Token"]
			client = try.client
			break
		}
	}
	if master == "" {
		fail("1 device sign-in", "Google refused every way it was asked")
	}
	fmt.Println("STEP 1 device sign-in: ok (Google issued a device token)")

	// 2. A token Chrome's sign-in exchange accepts.
	form := common()
	form.Set("EncryptedPasswd", master)
	form.Set("service", "oauth2:https://www.google.com/accounts/OAuthLogin")
	form.Set("app", "com.google.android.gms")
	answer, err = androidAuth(client, form)
	if err != nil {
		fail("2 account token", err.Error())
	}
	token := answer["Auth"]
	if token == "" {
		fail("2 account token", fmt.Sprintf("%s, Error=%s %s", answer["_status"], answer["Error"], answer["ErrorDetail"]))
	}
	fmt.Println("STEP 2 account token: ok")

	// 3. Trade it for a one-time web sign-in ticket.
	req, _ := http.NewRequest(http.MethodGet, "https://accounts.google.com/OAuthLogin?source=ChromiumBrowser&issueuberauth=1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		fail("3 web ticket", err.Error())
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	ticket := strings.TrimSpace(string(body))
	if resp.StatusCode != http.StatusOK || ticket == "" || strings.ContainsAny(ticket, " <\n") {
		fail("3 web ticket", fmt.Sprintf("%s (%d bytes back)", resp.Status, len(body)))
	}
	fmt.Println("STEP 3 web ticket: ok")

	// 4. Spend the ticket: this is what sets the browser cookies.
	merge := "https://accounts.google.com/MergeSession?service=mail&source=ChromiumBrowser&continue=" +
		url.QueryEscape("https://messages.google.com/web/config") + "&uberauth=" + url.QueryEscape(ticket)
	resp, err = client.Get(merge)
	if err != nil {
		fail("4 web sign-in", err.Error())
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	fmt.Println("STEP 4 web sign-in: answered", resp.Status, "at", resp.Request.URL.Host+resp.Request.URL.Path)

	names := map[string]bool{}
	for _, site := range []string{"https://www.google.com/", "https://accounts.google.com/", "https://messages.google.com/web/"} {
		u, _ := url.Parse(site)
		for _, c := range jar.Cookies(u) {
			names[c.Name] = true
		}
	}
	var have, missing []string
	for name := range names {
		have = append(have, name)
	}
	sort.Strings(have)
	for _, name := range []string{"SID", "HSID", "OSID", "SSID", "APISID", "SAPISID"} {
		if !names[name] {
			missing = append(missing, name)
		}
	}
	fmt.Println("cookies obtained:", strings.Join(have, " "))
	if len(missing) == 0 {
		fmt.Println("RESULT: all six cookies pairing needs were obtained. An app password can work.")
	} else {
		fmt.Println("RESULT: missing", strings.Join(missing, " "), "- not enough to pair.")
	}
}
