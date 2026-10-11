//go:build linux

package main

import (
	"errors"
	"net"
	"os"
	"sync"
	"syscall"
)

// Go accepts connections with the accept4 system call, which the TouchPad's
// 2.6.35 kernel does not have on ARM (it arrived in 2.6.36). This listener
// uses the plain accept call every Linux has.
type plainListener struct {
	fd     int
	addr   net.Addr
	once   sync.Once
	closed chan struct{}
}

func listenTCP(address string) (net.Listener, error) {
	tcp, err := net.ResolveTCPAddr("tcp4", address)
	if err != nil {
		return nil, err
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	sa := &syscall.SockaddrInet4{Port: tcp.Port}
	if ip := tcp.IP.To4(); ip != nil {
		copy(sa.Addr[:], ip)
	}
	if err = syscall.Bind(fd, sa); err == nil {
		err = syscall.Listen(fd, 16)
	}
	if err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	return &plainListener{fd: fd, addr: tcp, closed: make(chan struct{})}, nil
}

func (l *plainListener) Accept() (net.Conn, error) {
	for {
		// The caller's address is not needed, so none is asked for.
		nfd, _, errno := syscall.Syscall(syscall.SYS_ACCEPT, uintptr(l.fd), 0, 0)
		select {
		case <-l.closed:
			if errno == 0 {
				_ = syscall.Close(int(nfd))
			}
			return nil, net.ErrClosed
		default:
		}
		if errno == syscall.EINTR || errno == syscall.ECONNABORTED {
			continue
		}
		if errno != 0 {
			return nil, &net.OpError{Op: "accept", Net: "tcp", Addr: l.addr, Err: errno}
		}
		syscall.CloseOnExec(int(nfd))
		file := os.NewFile(nfd, "connection")
		if file == nil {
			return nil, errors.New("accept: bad descriptor")
		}
		// FileConn works on its own copy of the descriptor.
		conn, err := net.FileConn(file)
		_ = file.Close()
		if err != nil {
			continue
		}
		return conn, nil
	}
}

func (l *plainListener) Close() error {
	l.once.Do(func() {
		close(l.closed)
		// shutdown wakes the accept that is waiting; close alone does not.
		_ = syscall.Shutdown(l.fd, syscall.SHUT_RDWR)
		_ = syscall.Close(l.fd)
	})
	return nil
}

func (l *plainListener) Addr() net.Addr { return l.addr }
