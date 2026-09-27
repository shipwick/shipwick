package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

const localImage = "shipwick.local/my-api:20260927-153000-a1b2"

// postArchive sends an image upload with the given content type.
func (f *fixture) postArchive(path, contentType string, body []byte) (int, []byte) {
	f.t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.srv.URL+path, bytes.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func TestLoadImageThenDeployIt(t *testing.T) {
	f := newFixture(t)

	// The fake runtime reads the archive as the list of references it carries.
	status, body := f.postArchive("/api/v1/applications/my-api/images", "application/x-tar", []byte(localImage+"\n"))
	if status != http.StatusCreated {
		t.Fatalf("status = %d: %s", status, body)
	}
	loaded := decode[api.LoadedImage](t, body)
	if loaded.Image != localImage || loaded.SizeBytes != int64(len(localImage)+1) {
		t.Errorf("loaded = %+v", loaded)
	}
	if !strings.Contains(string(body), `"size_bytes"`) {
		t.Errorf("the wire name is size_bytes: %s", body)
	}

	config := "name: my-api\nbuild: .\nimage: " + localImage + "\nport: 8080\n"
	status, body = f.do("POST", "/api/v1/applications/my-api/deploy", config)
	if status != http.StatusAccepted {
		t.Fatalf("deploy: %d %s", status, body)
	}
	f.engine.Wait()
	d, err := f.store.GetDeployment(context.Background(), decode[api.Deployment](t, body).ID)
	if err != nil || d.Status != api.StatusActive {
		t.Errorf("deployment = %+v, %v", d, err)
	}
	if pulled := f.rt.Pulled(); len(pulled) != 0 {
		t.Errorf("pulled %v", pulled)
	}
}

func TestLoadImageRejectsWhatIsNotTheApplicationsArchive(t *testing.T) {
	f := newFixture(t)

	status, body := f.postArchive("/api/v1/applications/my-api/images", "application/gzip", []byte(localImage))
	if status != http.StatusBadRequest || decodeError(t, body).Code != api.CodeInvalidRequest {
		t.Errorf("wrong content type: status = %d, body = %s", status, body)
	}

	status, body = f.postArchive("/api/v1/applications/my-api/images", "application/x-tar", []byte("shipwick.local/other:20260927-153000-a1b2\n"))
	if status != http.StatusBadRequest || decodeError(t, body).Code != api.CodeInvalidRequest {
		t.Errorf("another application's image: status = %d, body = %s", status, body)
	}
	if !strings.Contains(decodeError(t, body).Message, "shipwick.local/my-api:") {
		t.Errorf("the message should say how the image must be tagged: %s", body)
	}
	if ok, _ := f.rt.ImageExists(context.Background(), "shipwick.local/other:20260927-153000-a1b2"); ok {
		t.Error("a refused image must not stay")
	}

	status, body = f.postArchive("/api/v1/applications/Not_Valid/images", "application/x-tar", []byte(localImage))
	if status != http.StatusBadRequest {
		t.Errorf("invalid name: status = %d, body = %s", status, body)
	}
}

func TestADeployWithBuildAndNoImageIsRefused(t *testing.T) {
	f := newFixture(t)
	status, body := f.do("POST", "/api/v1/applications/my-api/deploy", "name: my-api\nbuild: .\nport: 8080\n")
	if status != http.StatusBadRequest || decodeError(t, body).Code != api.CodeInvalidRequest {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if !strings.Contains(decodeError(t, body).Message, "never builds") {
		t.Errorf("the message should say the agent never builds: %s", body)
	}
}
