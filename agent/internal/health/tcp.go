package health

import (
	"context"
	"net"
	"strconv"
	"time"
)

// CheckTCP performs one probe: a TCP connection to ip:port, bounded by
// timeout. Being accepted is healthy; nothing is sent and the connection is
// closed at once. For a database or a queue, a socket that accepts is as much
// as a probe from outside can know.
func CheckTCP(ctx context.Context, ip string, port int, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return describe(err, timeout)
	}
	conn.Close()
	return nil
}
