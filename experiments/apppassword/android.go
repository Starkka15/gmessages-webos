package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// A connection that greets the server the way an Android phone's own network
// library does. Google's device sign-in turns away connections that do not
// look like a phone's, whatever the password.
type androidTransport struct{}

func (androidTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return nil, fmt.Errorf("https only")
	}
	host := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	raw, err := dialer.DialContext(req.Context(), "tcp4", net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	conn := utls.UClient(raw, &utls.Config{ServerName: host}, utls.HelloAndroid_11_OkHttp)
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err = conn.HandshakeContext(req.Context()); err != nil {
		raw.Close()
		return nil, err
	}
	// The phone offers HTTP/2; speak whichever the server chose.
	if conn.ConnectionState().NegotiatedProtocol == "h2" {
		t := &http2.Transport{}
		cc, err := t.NewClientConn(conn)
		if err != nil {
			conn.Close()
			return nil, err
		}
		return cc.RoundTrip(req)
	}
	if err = req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return resp, nil
}

var _ = context.Background
