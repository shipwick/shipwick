package deploy

import (
	"encoding/json"
	"math"
	"net"
	"strings"
	"time"
)

// accessEntry is one request as the proxy logged it.
type accessEntry struct {
	Time     time.Time
	Host     string // lowercase, without a port
	Method   string
	Path     string // without a query string
	Status   int
	Duration time.Duration
	Bytes    int64
	Client   string
}

// accessLogger is the logger Caddy writes the server's access log with: see
// proxy.Build, which names it.
const accessLogger = "http.log.access.shipwick"

// maxLoggedPath bounds what one request can make the agent remember.
const maxLoggedPath = 512

// caddyAccess is the part of Caddy's access log entry the proxy configuration
// leaves in.
type caddyAccess struct {
	TS      float64 `json:"ts"`
	Logger  string  `json:"logger"`
	Request struct {
		ClientIP string `json:"client_ip"`
		Method   string `json:"method"`
		Host     string `json:"host"`
		URI      string `json:"uri"`
	} `json:"request"`
	Duration float64 `json:"duration"`
	Size     int64   `json:"size"`
	Status   int     `json:"status"`
}

// parseAccessLine reads one line of the proxy's standard output. Anything
// that is not an access log entry — there should be nothing else there, but
// the stream is not the agent's to define — is reported as not ok.
func parseAccessLine(line []byte) (accessEntry, bool) {
	var raw caddyAccess
	if err := json.Unmarshal(line, &raw); err != nil || raw.Logger != accessLogger || raw.TS <= 0 {
		return accessEntry{}, false
	}
	sec, frac := math.Modf(raw.TS)
	e := accessEntry{
		// Microseconds: a float of seconds since 1970 carries no more.
		Time:     time.Unix(int64(sec), int64(frac*1e6)*1000).UTC(),
		Host:     hostOnly(raw.Request.Host),
		Method:   raw.Request.Method,
		Path:     raw.Request.URI,
		Status:   raw.Status,
		Duration: time.Duration(raw.Duration * float64(time.Second)),
		Bytes:    raw.Size,
		Client:   raw.Request.ClientIP,
	}
	// The proxy is told to cut the query string off; a configuration loaded
	// by an older agent may still be running when this one starts reading.
	if i := strings.IndexByte(e.Path, '?'); i >= 0 {
		e.Path = e.Path[:i]
	}
	if len(e.Path) > maxLoggedPath {
		e.Path = e.Path[:maxLoggedPath]
	}
	return e, true
}

// hostOnly is the hostname of a Host header: lowercase, without the port.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}
