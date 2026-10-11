//go:build !linux

package main

import "net"

// Off the TouchPad (a PC, for trying things out) Go's own listener is fine.
func listenTCP(address string) (net.Listener, error) {
	return net.Listen("tcp4", address)
}
