package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// heartbeatInterval is how long a followed log may carry nothing before it
// carries a heartbeat line. Cloudflare's proxy closes a response that has
// carried no bytes for 100 seconds, and was seen to close one after 60; the
// log of a quiet application carries none for hours. Twenty seconds is far
// enough under both and costs a quiet stream three lines of 60 bytes a
// minute; a busy one carries nothing extra.
const heartbeatInterval = 20 * time.Second

// streamTimer is what a stream waits on for its idle interval: a time.Timer,
// or in tests one the test fires. Reset starts the interval over after a
// line was sent; Stop ends it with the stream.
type streamTimer struct {
	C     <-chan time.Time
	Reset func(time.Duration)
	Stop  func()
}

func (s *Server) idleTimer(d time.Duration) streamTimer {
	if s.newTimer != nil {
		return s.newTimer(d)
	}
	t := time.NewTimer(d)
	return streamTimer{C: t.C, Reset: func(d time.Duration) { t.Reset(d) }, Stop: func() { t.Stop() }}
}

// streamLines writes lines to w as they come, one JSON object per line,
// flushed per line while the stream is idle and per batch while it is busy,
// and a heartbeat whenever heartbeatInterval passes without a line — only
// then: every line sent starts the interval over. It returns when lines is
// closed or the client is gone; the heartbeat stops with it. A stream the
// agent ends — the containers are gone, or the agent is shutting down —
// ends with a line that says which, so that a client reconnects after a
// restart and stops after a deployment.
func (s *Server) streamLines(w http.ResponseWriter, rc *http.ResponseController, lines <-chan api.LogLine) {
	enc := json.NewEncoder(w)
	idle := s.idleTimer(heartbeatInterval)
	defer idle.Stop()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				end := api.LogEnd{End: api.LogEndContainers}
				select {
				case <-s.closing:
					end.End = api.LogEndShutdown
				default:
				}
				if enc.Encode(end) == nil {
					rc.Flush()
				}
				return
			}
			if err := enc.Encode(line); err != nil {
				return
			}
			if len(lines) == 0 {
				rc.Flush()
			}
		case <-idle.C:
			if err := enc.Encode(api.LogHeartbeat{Heartbeat: true, Time: s.now().UTC()}); err != nil {
				return
			}
			rc.Flush()
		}
		idle.Reset(heartbeatInterval)
	}
}
