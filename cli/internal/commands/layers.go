package commands

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"

	dockerclient "github.com/moby/moby/client"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// An image built here shares most of itself with what the server has: the
// base image's layers, and usually everything but the application's own.
// Sending those again is the whole cost of a deployment, so they are left
// out of the archive.
//
// What `docker save` writes, from both of Docker's image stores, is an OCI
// layout — blobs/sha256/<digest> — with a manifest.json that lists the
// image's layer files, base layer first; and `docker load`, in both stores,
// does not open the file of a layer it already has. A layer is known by its
// diff ID (the RootFS of `docker image inspect`), which is what the agent is
// asked about; manifest.json, the last entry of the archive, says which file
// holds which. So the archive is read twice from the local daemon: once to
// its end, for that list, and once more to send, without the files the
// server said it has.
//
// None of this may cost a deployment. An agent that does not know the
// question, an archive that is not laid out as described, a server that
// loses a layer between the answer and the upload: each ends in the whole
// archive being sent, as it always was.

// sendImage saves the image and sends it to the server for the application,
// without the layers the server has when that can be found out.
func (c *cli) sendImage(ctx context.Context, cl *client.Client, tools buildTools, app, image string) (api.LoadedImage, error) {
	loaded, whole, err := c.sendMissingLayers(ctx, cl, tools, app, image)
	c.ui.Done()
	if err == nil {
		c.ui.Success("Sent image to the server (%s; the server had the rest of %s)", spec.FormatMemory(loaded.SizeBytes), spec.FormatMemory(whole))
		return loaded, nil
	}
	if ctx.Err() != nil {
		return api.LoadedImage{}, ctx.Err()
	}

	archive, err := tools.save(ctx, image)
	if err != nil {
		return api.LoadedImage{}, err
	}
	defer archive.Close()
	loaded, err = cl.PushImage(ctx, app, c.sendProgress(archive), 0)
	c.ui.Done()
	if err != nil {
		return api.LoadedImage{}, err
	}
	c.ui.Success("Sent image to the server (%s)", spec.FormatMemory(loaded.SizeBytes))
	return loaded, nil
}

func (c *cli) sendProgress(r io.Reader) io.Reader {
	return &progressReader{r: r, report: func(n int64) {
		c.ui.Progress("Sending image to the server (%s)", spec.FormatMemory(n))
	}}
}

// errSendWhole is every reason to send the whole archive that is not worth
// a sentence: there is nothing the user should do about any of them.
var errSendWhole = errors.New("the image is sent whole")

// sendMissingLayers sends the image without the layers the server has, and
// returns what the agent loaded and how long the whole archive is. Any error
// means nothing was loaded and the whole archive is to be sent.
func (c *cli) sendMissingLayers(ctx context.Context, cl *client.Client, tools buildTools, app, image string) (loaded api.LoadedImage, whole int64, err error) {
	layers, err := tools.layers(ctx, image)
	if err != nil {
		return loaded, 0, err
	}
	if len(layers) == 0 {
		return loaded, 0, errSendWhole
	}
	missing, err := cl.MissingLayers(ctx, app, layers)
	if err != nil {
		return loaded, 0, err
	}
	if len(missing) >= len(layers) {
		return loaded, 0, errSendWhole // nothing to leave out; one pass is enough
	}

	c.ui.Progress("Comparing the image with what the server has")
	survey, err := tools.save(ctx, image)
	if err != nil {
		return loaded, 0, err
	}
	files, whole, err := surveyArchive(survey)
	survey.Close()
	if err != nil {
		return loaded, 0, err
	}
	omit, ok := omittable(files, layers, missing)
	if !ok {
		return loaded, 0, errSendWhole
	}

	archive, err := tools.save(ctx, image)
	if err != nil {
		return loaded, 0, err
	}
	defer archive.Close()
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		pw.CloseWithError(reduceArchive(pw, archive, omit))
	}()
	loaded, err = cl.PushImage(ctx, app, c.sendProgress(pr), 0)
	// An upload that ended early leaves the copy blocked on the pipe.
	pr.Close()
	<-done
	return loaded, whole, err
}

// dockerImageLayers asks the local Docker daemon for an image's layers: the
// diff IDs, base layer first.
func dockerImageLayers(ctx context.Context, image string) ([]string, error) {
	cli, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("connect to Docker: %w", err)
	}
	defer cli.Close()
	inspect, err := cli.ImageInspect(ctx, image)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", image, err)
	}
	return inspect.RootFS.Layers, nil
}

// noImageLayers stands in where the image store is not Docker's: with
// nothing known about the layers, the image is sent whole.
func noImageLayers(context.Context, string) ([]string, error) {
	return nil, errSendWhole
}

// maxManifestBytes bounds manifest.json, which lists one image's layers and
// is a few kilobytes.
const maxManifestBytes = 1 << 20

// layerFile is where a layer lives in the archives this code understands.
var layerFile = regexp.MustCompile(`^blobs/sha256/[0-9a-f]{64}$`)

// surveyArchive reads an archive written by `docker save` to its end and
// returns the files that hold its image's layers, base layer first, and the
// archive's length. An archive it does not understand — more than one image,
// layers kept anywhere but blobs/sha256, a layer file that is not there — is
// an error.
func surveyArchive(r io.Reader) (layers []string, size int64, err error) {
	counted := &countReader{r: r}
	tr := tar.NewReader(counted)
	files := map[string]bool{}
	var manifest []struct {
		Layers []string `json:"Layers"`
	}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("read the saved image: %w", err)
		}
		if h.Typeflag == tar.TypeReg {
			files[h.Name] = true
		}
		if h.Name != "manifest.json" {
			continue
		}
		if manifest != nil || h.Size > maxManifestBytes {
			return nil, 0, errSendWhole
		}
		if err := json.NewDecoder(tr).Decode(&manifest); err != nil {
			return nil, 0, fmt.Errorf("read the saved image's manifest.json: %w", err)
		}
	}
	// The archive ends in padding the tar reader does not ask for.
	if _, err := io.Copy(io.Discard, counted); err != nil {
		return nil, 0, fmt.Errorf("read the saved image: %w", err)
	}

	if len(manifest) != 1 || len(manifest[0].Layers) == 0 {
		return nil, 0, errSendWhole
	}
	for _, file := range manifest[0].Layers {
		if !layerFile.MatchString(file) || !files[file] {
			return nil, 0, errSendWhole
		}
	}
	return manifest[0].Layers, counted.n, nil
}

// omittable returns the archive's files that hold only layers the server
// has. files and layers describe the same image, base layer first: the file
// of each layer and its diff ID. It reports false when they do not match,
// when the answer names a layer the image does not have, or when there is
// nothing to leave out.
func omittable(files, layers, missing []string) (map[string]bool, bool) {
	if len(files) != len(layers) {
		return nil, false
	}
	known := map[string]bool{}
	for _, layer := range layers {
		known[layer] = true
	}
	needed := map[string]bool{}
	for _, layer := range missing {
		if !known[layer] {
			return nil, false
		}
		needed[layer] = true
	}

	omit := map[string]bool{}
	for i, file := range files {
		if !needed[layers[i]] {
			omit[file] = true
		}
	}
	// One file can hold two layers of an image; it stays if either is needed.
	for i, file := range files {
		if needed[layers[i]] {
			delete(omit, file)
		}
	}
	return omit, len(omit) > 0
}

// reduceArchive copies a tar archive from r to w without the named files.
// It holds one entry's header at a time and nothing else.
func reduceArchive(w io.Writer, r io.Reader, omit map[string]bool) error {
	tr := tar.NewReader(r)
	tw := tar.NewWriter(w)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read the saved image: %w", err)
		}
		if h.Typeflag == tar.TypeReg && omit[h.Name] {
			continue
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}
	return tw.Close()
}

// countReader counts the bytes read through it.
type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
