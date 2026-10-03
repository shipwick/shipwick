package deploy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/agent/internal/docker"
)

func TestMissingLayersAreThoseNoImageOnTheServerStartsWith(t *testing.T) {
	// node:22-alpine with an application on top, and an unrelated image.
	have := [][]string{
		{"alpine", "node", "yarn", "workdir", "app-v1"},
		{"debian", "nginx"},
	}
	for name, tc := range map[string]struct{ layers, want []string }{
		"a new version of the application":  {[]string{"alpine", "node", "yarn", "workdir", "app-v2"}, []string{"app-v2"}},
		"the same image again":              {[]string{"alpine", "node", "yarn", "workdir", "app-v1"}, []string{}},
		"another application on that base":  {[]string{"alpine", "node", "yarn", "other"}, []string{"other"}},
		"an image the server knows none of": {[]string{"ubuntu", "python"}, []string{"ubuntu", "python"}},
		// A layer is its content on top of what is beneath it: the same
		// diff over another base is another layer to Docker.
		"a known layer on an unknown base":  {[]string{"ubuntu", "node"}, []string{"ubuntu", "node"}},
		"a known layer above a missing one": {[]string{"alpine", "curl", "yarn"}, []string{"curl", "yarn"}},
		"a shorter image than the server's": {[]string{"debian"}, []string{}},
	} {
		h := newHarness(t)
		for _, image := range have {
			h.rt.AddImageLayers(image...)
		}
		got, err := h.engine.MissingLayers(context.Background(), tc.layers)
		if err != nil || !slices.Equal(got, tc.want) || got == nil {
			t.Errorf("%s: missing = %v, %v; want %v", name, got, err, tc.want)
		}
	}
}

func TestMissingLayersOnAnEmptyServerAreAllOfThem(t *testing.T) {
	h := newHarness(t)
	got, err := h.engine.MissingLayers(context.Background(), []string{"alpine", "app"})
	if err != nil || !slices.Equal(got, []string{"alpine", "app"}) {
		t.Errorf("missing = %v, %v", got, err)
	}
}

func TestMissingLayersFailWhenTheDaemonDoesNotAnswer(t *testing.T) {
	h := newHarness(t)
	h.rt.LayersErr = errors.New("list images: daemon is not running")
	if _, err := h.engine.MissingLayers(context.Background(), []string{"alpine"}); err == nil {
		t.Error("an unknown store must not be reported as an empty one")
	}
}

func TestAnArchiveWithoutLayersTheServerLacksIsRefusedAsIncomplete(t *testing.T) {
	h := newHarness(t)
	h.rt.LoadErr = fmt.Errorf("load image: %w", docker.ErrImageIncomplete)

	_, err := h.engine.LoadImage(context.Background(), "my-api", strings.NewReader(localImage+"\n"))
	if !errors.Is(err, ErrIncompleteImage) {
		t.Fatalf("err = %v, want ErrIncompleteImage", err)
	}
	if !strings.Contains(err.Error(), "send the whole image") {
		t.Errorf("the error should say what to do: %v", err)
	}
	if ok, _ := h.rt.ImageExists(context.Background(), localImage); ok {
		t.Error("nothing of a refused archive may stay")
	}

	// Any other refusal is the daemon's own and stays what it was.
	h.rt.LoadErr = errors.New("load image: archive/tar: invalid tar header")
	if _, err := h.engine.LoadImage(context.Background(), "my-api", strings.NewReader(localImage+"\n")); err == nil || errors.Is(err, ErrIncompleteImage) {
		t.Errorf("err = %v", err)
	}
}
