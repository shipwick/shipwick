package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Stopping waits for the replicas' grace period, which deploy.stop_timeout
// may set far beyond the server's write timeout. The answer must still arrive.
func TestStopAndDeleteAnswerAfterAGracePeriodLongerThanTheWriteTimeout(t *testing.T) {
	f := newFixture(t)
	if status, body := f.do("POST", "/api/v1/applications/my-api/deploy", validConfig); status != http.StatusAccepted {
		t.Fatalf("deploy: status = %d, body = %s", status, body)
	}
	f.engine.Wait()

	srv := httptest.NewUnstartedServer(f.api.Handler())
	srv.Config.WriteTimeout = 50 * time.Millisecond
	srv.Start()
	defer srv.Close()
	f.rt.StopDelay = 250 * time.Millisecond

	for _, tt := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/api/v1/applications/my-api/stop", http.StatusOK},
		{"DELETE", "/api/v1/applications/my-api", http.StatusNoContent},
	} {
		req, err := http.NewRequest(tt.method, srv.URL+tt.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: no answer: %v", tt.method, tt.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != tt.want {
			t.Errorf("%s %s: status = %d, want %d", tt.method, tt.path, resp.StatusCode, tt.want)
		}
		// The delete finds the replicas stopped; start them so that it has
		// something to wait for as well.
		f.do("POST", "/api/v1/applications/my-api/start", "")
	}
}
