package health

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func listener(t *testing.T) (net.Listener, string, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return ln, host, port
}

func TestCheckTCPPassesWhenAcceptedAndSendsNothing(t *testing.T) {
	ln, ip, port := listener(t)
	defer ln.Close()

	received := make(chan int, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			received <- -1
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _ := conn.Read(make([]byte, 16)) // EOF once the probe hangs up
		received <- n
	}()

	if err := CheckTCP(context.Background(), ip, port, time.Second); err != nil {
		t.Fatalf("a listening port is healthy: %v", err)
	}
	if n := <-received; n != 0 {
		t.Errorf("the probe wrote %d bytes; a database would log a protocol error", n)
	}
}

func TestCheckTCPFailsWhenNothingListens(t *testing.T) {
	ln, ip, port := listener(t)
	ln.Close() // nothing listens there any more

	err := CheckTCP(context.Background(), ip, port, time.Second)
	if err == nil {
		t.Fatal("expected an error")
	}
	// Short enough for a status line: no "dial tcp 127.0.0.1:54321:" chains.
	if strings.Contains(err.Error(), "dial tcp") || len(err.Error()) > 120 {
		t.Errorf("error is not user-friendly: %q", err)
	}
}

func TestCheckTCPCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// 192.0.2.1 (TEST-NET-1) is never routed, so only the context can end this.
	if err := CheckTCP(ctx, "192.0.2.1", 5432, 10*time.Second); err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled so callers can tell shutdown from failure", err)
	}
}
