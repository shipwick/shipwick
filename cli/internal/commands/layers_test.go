package commands

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// digest is a readable stand-in for a sha256 digest: the character, 64 times.
func digest(c string) string { return strings.Repeat(c, 64) }

func blob(c string) string   { return "blobs/sha256/" + digest(c) }
func diffID(c string) string { return "sha256:" + digest(c) }

type tarEntry struct{ name, content string }

func imageArchive(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o444, Size: int64(len(e.content))}
		if strings.HasSuffix(e.name, "/") {
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		io.WriteString(tw, e.content)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func entriesOf(t *testing.T, archive []byte) []tarEntry {
	t.Helper()
	var entries []tarEntry
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return entries
		}
		if err != nil {
			t.Fatalf("the archive must stay a tar archive: %v", err)
		}
		content, _ := io.ReadAll(tr)
		entries = append(entries, tarEntry{h.Name, string(content)})
	}
}

func manifestJSON(layerFiles ...string) string {
	b, _ := json.Marshal([]map[string]any{{"Config": blob("c"), "RepoTags": []string{"shipwick.local/my-api:1"}, "Layers": layerFiles}})
	return string(b)
}

// The image of these tests has three layers, with diff IDs 1, 2 and 3: two of
// a base image and the application's own.
var threeLayers = []string{diffID("1"), diffID("2"), diffID("3")}

// containerdArchive is what `docker save` writes from the containerd image
// store: an OCI layout whose layer blobs are compressed, so that their names
// (a, b, d) are not the layers' diff IDs, with the manifests and an
// attestation next to them and manifest.json at the end.
func containerdArchive(t *testing.T) []byte {
	return imageArchive(t,
		tarEntry{"blobs/", ""},
		tarEntry{"blobs/sha256/", ""},
		tarEntry{blob("a"), "base layer, gzipped"},
		tarEntry{blob("9"), `{"schemaVersion":2,"manifests":[]}`},
		tarEntry{blob("b"), "second base layer, gzipped"},
		tarEntry{blob("c"), `{"rootfs":{"type":"layers","diff_ids":[]}}`},
		tarEntry{blob("d"), "the application's layer, gzipped"},
		tarEntry{blob("e"), `{"_type":"https://in-toto.io/Statement/v1"}`},
		tarEntry{"index.json", `{"schemaVersion":2}`},
		tarEntry{"manifest.json", manifestJSON(blob("a"), blob("b"), blob("d"))},
		tarEntry{"oci-layout", `{"imageLayoutVersion":"1.0.0"}`},
	)
}

// classicArchive is what `docker save` writes from the classic image store:
// the same layout with uncompressed layers, each named by its diff ID.
func classicArchive(t *testing.T) []byte {
	return imageArchive(t,
		tarEntry{"blobs/", ""},
		tarEntry{"blobs/sha256/", ""},
		tarEntry{blob("1"), "base layer"},
		tarEntry{blob("3"), "the application's layer"},
		tarEntry{blob("c"), `{"rootfs":{"type":"layers","diff_ids":[]}}`},
		tarEntry{blob("2"), "second base layer"},
		tarEntry{blob("9"), `{"schemaVersion":2}`},
		tarEntry{"index.json", `{"schemaVersion":2}`},
		tarEntry{"manifest.json", manifestJSON(blob("1"), blob("2"), blob("3"))},
		tarEntry{"oci-layout", `{"imageLayoutVersion":"1.0.0"}`},
		tarEntry{"repositories", `{}`},
	)
}

func TestSurveyFindsTheLayerFilesOfBothImageStores(t *testing.T) {
	for name, tc := range map[string]struct {
		archive []byte
		want    []string
	}{
		"containerd": {containerdArchive(t), []string{blob("a"), blob("b"), blob("d")}},
		"classic":    {classicArchive(t), []string{blob("1"), blob("2"), blob("3")}},
	} {
		files, size, err := surveyArchive(bytes.NewReader(tc.archive))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Equal(files, tc.want) {
			t.Errorf("%s: layer files = %v, want %v", name, files, tc.want)
		}
		if size != int64(len(tc.archive)) {
			t.Errorf("%s: size = %d, want the whole archive's %d, padding included", name, size, len(tc.archive))
		}
	}
}

func TestSurveyRefusesAnArchiveItDoesNotUnderstand(t *testing.T) {
	twoImages, _ := json.Marshal([]map[string]any{{"Layers": []string{blob("a")}}, {"Layers": []string{blob("b")}}})
	for name, archive := range map[string][]byte{
		// What Docker wrote before 25: a directory per layer.
		"layers outside blobs/sha256": imageArchive(t, tarEntry{"abc/layer.tar", "x"}, tarEntry{"manifest.json", manifestJSON("abc/layer.tar")}),
		"two images":                  imageArchive(t, tarEntry{blob("a"), "x"}, tarEntry{blob("b"), "y"}, tarEntry{"manifest.json", string(twoImages)}),
		"no manifest.json":            imageArchive(t, tarEntry{blob("a"), "x"}, tarEntry{"index.json", "{}"}),
		"a layer file that is absent": imageArchive(t, tarEntry{blob("a"), "x"}, tarEntry{"manifest.json", manifestJSON(blob("a"), blob("b"))}),
		"an image without layers":     imageArchive(t, tarEntry{"manifest.json", manifestJSON()}),
		"a manifest that is not JSON": imageArchive(t, tarEntry{blob("a"), "x"}, tarEntry{"manifest.json", "not json"}),
		"not a tar archive":           []byte(strings.Repeat("shipwick.local/my-api:1\n", 100)),
		"a truncated archive":         containerdArchive(t)[:700],
	} {
		if files, _, err := surveyArchive(bytes.NewReader(archive)); err == nil {
			t.Errorf("%s: understood as %v, want an error", name, files)
		}
	}
}

func TestOmittableLeavesOutOnlyWhatTheServerHas(t *testing.T) {
	files := []string{blob("a"), blob("b"), blob("d")}
	for name, tc := range map[string]struct {
		files, layers, missing []string
		want                   []string // nil: nothing can be left out
	}{
		"the server has the base layers":     {files, threeLayers, []string{diffID("3")}, []string{blob("a"), blob("b")}},
		"the server has the first layer":     {files, threeLayers, []string{diffID("2"), diffID("3")}, []string{blob("a")}},
		"the server has all of it":           {files, threeLayers, nil, []string{blob("a"), blob("b"), blob("d")}},
		"the server has none of it":          {files, threeLayers, threeLayers, nil},
		"more layers than files":             {files[:2], threeLayers, []string{diffID("3")}, nil},
		"an answer about another image":      {files, threeLayers, []string{diffID("f")}, nil},
		"a file needed by a missing layer":   {[]string{blob("a"), blob("b"), blob("a")}, []string{diffID("1"), diffID("2"), diffID("1")}, []string{diffID("1")}, []string{blob("b")}},
		"one file for a had and a new layer": {[]string{blob("a"), blob("a")}, []string{diffID("1"), diffID("2")}, []string{diffID("2")}, nil},
	} {
		omit, ok := omittable(tc.files, tc.layers, tc.missing)
		var got []string
		for file := range omit {
			got = append(got, file)
		}
		slices.Sort(got)
		if ok != (tc.want != nil) || (ok && !slices.Equal(got, tc.want)) {
			t.Errorf("%s: omit %v (%v), want %v", name, got, ok, tc.want)
		}
	}
}

func TestReduceArchiveDropsTheNamedFilesAndKeepsTheRestAsItWas(t *testing.T) {
	for name, tc := range map[string]struct {
		archive []byte
		omit    []string
	}{
		"containerd": {containerdArchive(t), []string{blob("a"), blob("b")}},
		"classic":    {classicArchive(t), []string{blob("1"), blob("2")}},
	} {
		omit := map[string]bool{}
		for _, file := range tc.omit {
			omit[file] = true
		}
		var want []tarEntry
		for _, e := range entriesOf(t, tc.archive) {
			if !omit[e.name] {
				want = append(want, e)
			}
		}

		var out bytes.Buffer
		if err := reduceArchive(&out, bytes.NewReader(tc.archive), omit); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := entriesOf(t, out.Bytes()); !slices.Equal(got, want) {
			t.Errorf("%s: entries = %v, want %v", name, got, want)
		}
		if len(want) != len(entriesOf(t, tc.archive))-2 {
			t.Errorf("%s: exactly the two base layers should go", name)
		}
	}
}

func TestReduceArchiveFailsOnABrokenArchive(t *testing.T) {
	if err := reduceArchive(io.Discard, bytes.NewReader(containerdArchive(t)[:700]), map[string]bool{blob("a"): true}); err == nil {
		t.Error("a truncated archive must not be passed on as a whole one")
	}
}

// layerAgent answers the two image endpoints the way the agent does, for a
// server that has the layers in has.
type layerAgent struct {
	mu       sync.Mutex
	has      []string // the diff IDs of an image on the server
	noAnswer bool     // an agent older than the question
	refuse   int      // how many uploads the daemon refuses as incomplete
	asked    [][]string
	uploads  [][]byte
}

func (a *layerAgent) client(t *testing.T) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/applications/{name}/images/missing", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.noAnswer {
			respondError(w, 404, api.Error{Code: api.CodeEndpointNotFound, Message: "no such endpoint: POST " + r.URL.Path})
			return
		}
		var req api.MissingLayersRequest
		json.NewDecoder(r.Body).Decode(&req)
		a.asked = append(a.asked, req.Layers)
		n := 0
		for n < len(req.Layers) && n < len(a.has) && req.Layers[n] == a.has[n] {
			n++
		}
		respond(w, 200, api.MissingLayers{Missing: req.Layers[n:]})
	})
	mux.HandleFunc("POST /api/v1/applications/{name}/images", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		defer a.mu.Unlock()
		a.uploads = append(a.uploads, body)
		if a.refuse > 0 {
			a.refuse--
			respondError(w, 409, api.Error{Code: api.CodeImageIncomplete, Message: "image: the archive does not hold every layer of the image"})
			return
		}
		respond(w, 201, api.LoadedImage{Image: "shipwick.local/my-api:1", SizeBytes: int64(len(body))})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cl, err := client.New(srv.URL, testToken)
	if err != nil {
		t.Fatal(err)
	}
	return cl
}

// savedImage is a local Docker with one image: its layers and its archive.
type savedImage struct {
	layers    []string
	layersErr error
	archive   []byte
	saves     int
}

func (s *savedImage) tools() buildTools {
	return buildTools{
		save: func(context.Context, string) (io.ReadCloser, error) {
			s.saves++
			return io.NopCloser(bytes.NewReader(s.archive)), nil
		},
		layers: func(context.Context, string) ([]string, error) {
			return s.layers, s.layersErr
		},
	}
}

func sendTestImage(t *testing.T, a *layerAgent, s *savedImage) (api.LoadedImage, string, error) {
	t.Helper()
	var out bytes.Buffer
	c, _ := newRoot(Options{In: strings.NewReader(""), Out: &out, Err: io.Discard, Getenv: func(string) string { return "" }})
	loaded, err := c.sendImage(context.Background(), a.client(t), s.tools(), "my-api", "shipwick.local/my-api:1")
	return loaded, out.String(), err
}

func names(entries []tarEntry) []string {
	var all []string
	for _, e := range entries {
		all = append(all, e.name)
	}
	return all
}

func TestSendImageLeavesOutTheLayersTheServerHas(t *testing.T) {
	for name, archive := range map[string][]byte{"containerd": containerdArchive(t), "classic": classicArchive(t)} {
		a := &layerAgent{has: []string{diffID("1"), diffID("2"), diffID("7")}}
		s := &savedImage{layers: threeLayers, archive: archive}

		loaded, out, err := sendTestImage(t, a, s)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(a.asked) != 1 || !slices.Equal(a.asked[0], threeLayers) {
			t.Errorf("%s: asked %v, want the image's diff IDs once", name, a.asked)
		}
		if len(a.uploads) != 1 {
			t.Fatalf("%s: %d uploads, want one", name, len(a.uploads))
		}
		sent := names(entriesOf(t, a.uploads[0]))
		all := names(entriesOf(t, archive))
		if len(sent) != len(all)-2 || slices.Contains(sent, blob("a")) || slices.Contains(sent, blob("1")) || slices.Contains(sent, blob("b")) || slices.Contains(sent, blob("2")) {
			t.Errorf("%s: sent %v, want everything but the two base layers", name, sent)
		}
		if !slices.Contains(sent, "manifest.json") || !slices.Contains(sent, blob("c")) {
			t.Errorf("%s: the manifest and the config must travel: %v", name, sent)
		}
		if loaded.SizeBytes != int64(len(a.uploads[0])) {
			t.Errorf("%s: loaded = %+v", name, loaded)
		}
		// The numbers are the upload's and the archive's own.
		want := fmt.Sprintf("✓ Sent image to the server (%s; the server had the rest of %s)\n", formatBytes(len(a.uploads[0])), formatBytes(len(archive)))
		if out != want {
			t.Errorf("%s: output = %q, want %q", name, out, want)
		}
	}
}

func formatBytes(n int) string { return spec.FormatMemory(int64(n)) }

func TestSendImageSendsTheWholeArchive(t *testing.T) {
	whole := containerdArchive(t)
	for name, tc := range map[string]struct {
		agent     *layerAgent
		image     *savedImage
		wantSaves int // how often the image is read from the local daemon
	}{
		"to an agent older than the question":       {&layerAgent{noAnswer: true}, &savedImage{layers: threeLayers, archive: whole}, 1},
		"to a server that has none of its layers":   {&layerAgent{}, &savedImage{layers: threeLayers, archive: whole}, 1},
		"when the local Docker does not say":        {&layerAgent{has: threeLayers}, &savedImage{layersErr: errors.New("no such image"), archive: whole}, 1},
		"when the archive is not laid out as known": {&layerAgent{has: threeLayers[:2]}, &savedImage{layers: threeLayers, archive: imageArchive(t, tarEntry{"abc/layer.tar", "x"}, tarEntry{"manifest.json", manifestJSON("abc/layer.tar")})}, 2},
		"when its layers and its files differ":      {&layerAgent{has: []string{diffID("0")}}, &savedImage{layers: append([]string{diffID("0")}, threeLayers...), archive: whole}, 2},
	} {
		loaded, out, err := sendTestImage(t, tc.agent, tc.image)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(tc.agent.uploads) != 1 || !bytes.Equal(tc.agent.uploads[0], tc.image.archive) {
			t.Errorf("%s: the archive must arrive once, as it was saved (%d uploads)", name, len(tc.agent.uploads))
		}
		if loaded.SizeBytes != int64(len(tc.image.archive)) || tc.image.saves != tc.wantSaves {
			t.Errorf("%s: loaded %+v after %d saves, want %d", name, loaded, tc.image.saves, tc.wantSaves)
		}
		if want := fmt.Sprintf("✓ Sent image to the server (%s)\n", formatBytes(len(tc.image.archive))); out != want {
			t.Errorf("%s: output = %q, want %q and nothing about what was tried", name, out, want)
		}
	}
}

func TestSendImageSendsTheWholeArchiveWhenTheServerRefusesTheReducedOne(t *testing.T) {
	whole := classicArchive(t)
	a := &layerAgent{has: threeLayers[:2], refuse: 1}
	s := &savedImage{layers: threeLayers, archive: whole}

	loaded, out, err := sendTestImage(t, a, s)
	if err != nil {
		t.Fatalf("a deployment must not fail because layers were left out: %v", err)
	}
	if len(a.uploads) != 2 || len(a.uploads[0]) >= len(whole) || !bytes.Equal(a.uploads[1], whole) {
		t.Fatalf("want the reduced archive, then the whole one; got %d uploads", len(a.uploads))
	}
	if loaded.SizeBytes != int64(len(whole)) {
		t.Errorf("loaded = %+v", loaded)
	}
	if want := fmt.Sprintf("✓ Sent image to the server (%s)\n", formatBytes(len(whole))); out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
}

func TestSendImageReportsTheRefusalOfTheWholeArchive(t *testing.T) {
	a := &layerAgent{has: threeLayers[:2], refuse: 2}
	s := &savedImage{layers: threeLayers, archive: classicArchive(t)}

	_, out, err := sendTestImage(t, a, s)
	if !client.IsCode(err, api.CodeImageIncomplete) {
		t.Errorf("err = %v, want the agent's", err)
	}
	if strings.Contains(out, "Sent image") {
		t.Errorf("nothing was sent:\n%s", out)
	}
	if !strings.Contains(Render(err), "shipwick deploy") {
		t.Errorf("the message should say what to do next: %s", Render(err))
	}
}

func TestBuildToolsAskDockerForLayersOnlyWhenDockerSavesTheImage(t *testing.T) {
	substituted := buildTools{save: func(context.Context, string) (io.ReadCloser, error) { return nil, nil }}.withDefaults()
	if layers, err := substituted.layers(context.Background(), "x"); err == nil || layers != nil {
		t.Errorf("a substituted image store has no layers to report: %v, %v", layers, err)
	}
}
