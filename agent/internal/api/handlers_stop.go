package api

import (
	"net/http"
	"time"
)

// afterGracePeriod gives a response its time to be written once an operation
// that waited for replicas to stop has returned. Stopping and deleting wait
// for the application's deploy.stop_timeout, which may be longer than the
// server's write timeout; the answer must still reach whoever waited for it.
func afterGracePeriod(w http.ResponseWriter) {
	http.NewResponseController(w).SetWriteDeadline(time.Now().Add(archiveIdle))
}
