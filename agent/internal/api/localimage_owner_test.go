package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestADeployWithAnotherApplicationsLocalImageIsRefused(t *testing.T) {
	f := newFixture(t)
	other := "shipwick.local/other:20260927-153000-a1b2"
	f.rt.AddLocalImage(other)

	status, body := f.do("POST", "/api/v1/applications/my-api/deploy", "name: my-api\nbuild: .\nimage: "+other+"\nport: 8080\n")
	if status != http.StatusBadRequest || decodeError(t, body).Code != api.CodeInvalidConfig {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if !strings.Contains(string(body), `"field":"image"`) || !strings.Contains(string(body), "built for other") {
		t.Errorf("the refusal should be on image and name the owner: %s", body)
	}

	status, body = f.do("POST", "/api/v1/applications/my-api/redeploy", `{"image": "`+other+`"}`)
	if status != http.StatusConflict || decodeError(t, body).Code != api.CodeNotDeployed {
		// Nothing is deployed yet, so the redeploy fails for that reason
		// first; what matters here is the image rule, below.
		if status != http.StatusBadRequest || !strings.Contains(decodeError(t, body).Message, "built for other") {
			t.Errorf("redeploy: status = %d, body = %s", status, body)
		}
	}
	if apps, _ := f.engine.Applications(context.Background()); len(apps) != 0 {
		t.Errorf("nothing may be recorded: %+v", apps)
	}
}

func TestTheSplunkTokenNeverLeavesTheServer(t *testing.T) {
	f := newFixture(t)
	config := "name: my-api\nimage: my-api:1.0\nport: 8080\nlogging:\n  driver: splunk\n  options:\n    splunk-url: https://hec.example.com:8088\n    splunk-token: 12345678-aaaa-bbbb\n"
	status, body := f.do("POST", "/api/v1/applications/my-api/deploy", config)
	if status != http.StatusAccepted {
		t.Fatalf("deploy: %d %s", status, body)
	}
	f.engine.Wait()
	for _, path := range []string{"/api/v1/applications/my-api", "/api/v1/deployments/1", "/api/v1/applications/my-api/config"} {
		status, body := f.do("GET", path, "")
		if status != http.StatusOK {
			t.Errorf("%s: %d %s", path, status, body)
		}
		if strings.Contains(string(body), "12345678-aaaa-bbbb") {
			t.Errorf("%s hands the token out: %s", path, body)
		}
		if !strings.Contains(string(body), "splunk-token") || !strings.Contains(string(body), "********") {
			t.Errorf("%s should show the option, masked: %s", path, body)
		}
	}
}
