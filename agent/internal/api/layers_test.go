package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/pkg/api"
)

func layer(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func layersBody(layers ...string) string {
	return `{"layers": ["` + strings.Join(layers, `", "`) + `"]}`
}

func TestMissingLayersAnswersWhatTheServerLacks(t *testing.T) {
	f := newFixture(t)
	f.rt.AddImageLayers(layer("a"), layer("b"), layer("c"))

	status, body := f.do("POST", "/api/v1/applications/my-api/images/missing", layersBody(layer("a"), layer("b"), layer("d")))
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if got := decode[api.MissingLayers](t, body); !slices.Equal(got.Missing, []string{layer("d")}) {
		t.Errorf("missing = %v, want the one new layer", got.Missing)
	}

	// An image that is all there: an empty list, not null.
	status, body = f.do("POST", "/api/v1/applications/my-api/images/missing", layersBody(layer("a"), layer("b")))
	if status != http.StatusOK || !strings.Contains(string(body), `"missing":[]`) {
		t.Errorf("status = %d, body = %s", status, body)
	}
}

func TestMissingLayersValidatesTheQuestion(t *testing.T) {
	f := newFixture(t)
	many := make([]string, maxImageLayers+1)
	for i := range many {
		many[i] = layer("a")
	}
	for name, body := range map[string]string{
		"no body":             ``,
		"not JSON":            `layers`,
		"no layers":           `{"layers": []}`,
		"an unknown field":    `{"layers": ["` + layer("a") + `"], "image": "x"}`,
		"a tag, not a digest": `{"layers": ["node:22-alpine"]}`,
		"uppercase hex":       layersBody("sha256:" + strings.Repeat("A", 64)),
		"a short digest":      layersBody("sha256:abc"),
		"too many layers":     layersBody(many...),
	} {
		status, resp := f.do("POST", "/api/v1/applications/my-api/images/missing", body)
		if status != http.StatusBadRequest || decodeError(t, resp).Code != api.CodeInvalidRequest {
			t.Errorf("%s: status = %d, body = %s", name, status, resp)
		}
	}

	status, resp := f.do("POST", "/api/v1/applications/My_Api/images/missing", layersBody(layer("a")))
	if status != http.StatusBadRequest {
		t.Errorf("an invalid application name: status = %d, body = %s", status, resp)
	}
}

func TestMissingLayersNeedsTheDeployRole(t *testing.T) {
	f := newFixture(t)
	viewer := "Bearer " + f.createToken("viewer", api.RoleRead).Token
	status, body := f.doWithAuth("POST", "/api/v1/applications/my-api/images/missing", layersBody(layer("a")), viewer)
	if status != http.StatusForbidden || decodeError(t, body).Code != api.CodeForbidden {
		t.Errorf("a read token learns nothing about the server's images: status = %d, body = %s", status, body)
	}

	ci := "Bearer " + f.createToken("ci", api.RoleDeploy).Token
	if status, body := f.doWithAuth("POST", "/api/v1/applications/my-api/images/missing", layersBody(layer("a")), ci); status != http.StatusOK {
		t.Errorf("a deploy token sends images, so it may ask: status = %d, body = %s", status, body)
	}
}

func TestAnIncompleteImageArchiveIsAConflictWithItsOwnCode(t *testing.T) {
	f := newFixture(t)
	f.rt.LoadErr = fmt.Errorf("load image: %w", docker.ErrImageIncomplete)

	status, body := f.postArchive("/api/v1/applications/my-api/images", "application/x-tar", []byte(localImage+"\n"))
	e := decodeError(t, body)
	if status != http.StatusConflict || e.Code != api.CodeImageIncomplete {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if !strings.Contains(e.Message, "send the whole image") {
		t.Errorf("the message should say what to do next: %q", e.Message)
	}
}
