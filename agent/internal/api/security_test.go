package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/pkg/api"
)

const securedConfig = `name: web
image: web:1.0
port: 8080
user: "1000:1000"
security:
  read_only: true
  tmpfs:
    - /tmp
    - path: /var/cache/web
      size: 200mb
  capabilities: none
  non_root: true
`

func TestTheSecurityBlockIsAppliedAndComesBackInTheSpec(t *testing.T) {
	f := newFixture(t)
	status, body := f.do("POST", "/api/v1/applications/web/deploy", securedConfig)
	if status != http.StatusAccepted {
		t.Fatalf("deploy: status = %d, body = %s", status, body)
	}
	f.engine.Wait()

	status, body = f.do("GET", "/api/v1/applications/web", "")
	if status != http.StatusOK {
		t.Fatalf("get application: status = %d", status)
	}
	app := decode[api.ApplicationDetail](t, body)
	const want = `"security":{"read_only":true,"tmpfs":[{"path":"/tmp","size_bytes":67108864},{"path":"/var/cache/web","size_bytes":209715200}],"capabilities":[],"non_root":true}`
	if app.Spec == nil || app.Spec.Security == nil || !strings.Contains(string(body), want) {
		t.Errorf("the application answers %s\nwant its spec to hold %s", body, want)
	}

	containers := f.rt.Containers()
	if len(containers) != 1 {
		t.Fatalf("%d containers, want the one replica (status %s)", len(containers), app.Status)
	}
	got := f.rt.Spec(containers[0].ID).Security
	if got == nil || !got.ReadOnly || !got.NonRoot || !got.DropCapabilities || len(got.Capabilities) != 0 ||
		len(got.Tmpfs) != 2 || got.Tmpfs[1] != (docker.Tmpfs{Path: "/var/cache/web", SizeBytes: 200 << 20}) {
		t.Errorf("the replica was created with %+v", got)
	}
}

func TestADocumentWithoutTheBlockHasNoSecurityInItsSpec(t *testing.T) {
	f := newFixture(t)
	if status, body := f.do("POST", "/api/v1/applications/web/deploy", "name: web\nimage: web:1.0\nport: 8080\n"); status != http.StatusAccepted {
		t.Fatalf("deploy: status = %d, body = %s", status, body)
	}
	f.engine.Wait()
	if _, body := f.do("GET", "/api/v1/applications/web", ""); strings.Contains(string(body), `"security"`) {
		t.Errorf("an application without the block answers with one: %s", body)
	}
}

func TestTheAgentRefusesWhatTheSecurityBlockCannotHold(t *testing.T) {
	f := newFixture(t)
	for name, tc := range map[string]struct{ document, field string }{
		"a capability to add":   {strings.Replace(securedConfig, "capabilities: none", "capabilities: [SYS_ADMIN]", 1), "security.capabilities[0]"},
		"a key that would add":  {securedConfig + "  privileged: true\n", "security"},
		"root under non_root":   {strings.Replace(securedConfig, `user: "1000:1000"`, "user: root", 1), "user"},
		"a tmpfs that is large": {strings.Replace(securedConfig, "size: 200mb", "size: 64gb", 1), "security.tmpfs[1].size"},
	} {
		status, body := f.do("POST", "/api/v1/applications/web/deploy", tc.document)
		e := decodeError(t, body)
		if status != http.StatusBadRequest || e.Code != api.CodeInvalidConfig {
			t.Errorf("%s: status = %d, error = %+v", name, status, e)
			continue
		}
		raw, _ := json.Marshal(e.Details["fields"])
		if !strings.Contains(string(raw), `"field":"`+tc.field+`"`) {
			t.Errorf("%s: fields = %s, want %s named", name, raw, tc.field)
		}
	}
	if all := decodeList(t, f); len(all) != 0 {
		t.Errorf("refused documents made %d deployments", len(all))
	}
}

func TestADeploymentOfARootImageUnderNonRootFailsWithWhatToSet(t *testing.T) {
	f := newFixture(t)
	status, body := f.do("POST", "/api/v1/applications/web/deploy", strings.Replace(securedConfig, "user: \"1000:1000\"\n", "", 1))
	if status != http.StatusAccepted {
		t.Fatalf("deploy: status = %d, body = %s", status, body)
	}
	f.engine.Wait()

	_, body = f.do("GET", "/api/v1/deployments/1", "")
	d := decode[api.DeploymentDetail](t, body)
	const want = `security.non_root refuses image web:1.0: it names no user, and a container without one runs as root; set user in deploy.yaml to a numeric id the image can run as, e.g. user: "1000:1000", or build the image with a USER instruction`
	if d.Status != api.StatusFailed || d.Error != want || d.CompletedAt == nil {
		t.Errorf("status = %s, completed = %v, error = %q\nwant %q", d.Status, d.CompletedAt, d.Error, want)
	}
	if n := len(f.rt.Containers()); n != 0 {
		t.Errorf("%d containers were created", n)
	}
}
