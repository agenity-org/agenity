package main

import "net"

// tcpZeroListener wraps a :0-bound TCP listener so the test helpers
// in p0_491_hub_test.go don't have to import "net" directly.
type tcpZeroListener struct {
	l *net.TCPListener
}

func newTCPZeroListener() (freePortListener, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	return &tcpZeroListener{l: l.(*net.TCPListener)}, nil
}

func (z *tcpZeroListener) port() int   { return z.l.Addr().(*net.TCPAddr).Port }
func (z *tcpZeroListener) Close() error { return z.l.Close() }
