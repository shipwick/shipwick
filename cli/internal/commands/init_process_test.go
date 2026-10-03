package commands

import (
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/spec"
)

func TestInitAsksForAnInitProcessWhereItsDockerfileStartsNode(t *testing.T) {
	f := newFakeAgent(t)
	for name, files := range map[string]map[string]string{
		"a server run with node": {"package.json": `{"dependencies":{"express":"^5"},"main":"server.js"}`},
		"a start script":         {"package.json": `{"scripts":{"start":"node dist/server.js && echo done"}}`},
		"nuxt":                   {"package.json": `{"dependencies":{"nuxt":"^4"},"scripts":{"build":"nuxt build"}}`},
		"next":                   {"package.json": `{"dependencies":{"next":"^15"},"scripts":{"build":"next build"}}`},
		"sveltekit":              {"package.json": `{"dependencies":{"@sveltejs/kit":"^2","@sveltejs/adapter-node":"^5"},"scripts":{"build":"vite build"}}`},
	} {
		dir := writeTree(t, files)
		if _, _, err := f.run(dir, "init", "--name", "app"); err != nil {
			t.Fatalf("%s: init: %v", name, err)
		}
		content := readOrEmpty(t, dir, DefaultFile)
		app, err := spec.Parse([]byte(content))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !app.Init || !strings.Contains(content, "\ninit: true\n") {
			t.Errorf("%s: no init: true, though the Dockerfile init wrote starts Node as the first process:\n%s", name, readOrEmpty(t, dir, "Dockerfile"))
		}
	}
}

func TestInitLeavesTheInitProcessToImagesItDidNotWrite(t *testing.T) {
	f := newFakeAgent(t)
	for name, files := range map[string]map[string]string{
		// The developer's own Dockerfile may bring an init of its own.
		"a Node project with its own Dockerfile": {"package.json": `{"dependencies":{"express":"^5"}}`, "Dockerfile": "FROM node:24-alpine\nENTRYPOINT [\"/sbin/tini\", \"--\"]\n"},
		"a Go program":                           {"go.mod": "module example.com/api\n\ngo 1.27\n", "main.go": "package main\n"},
	} {
		dir := writeTree(t, files)
		if _, _, err := f.run(dir, "init", "--name", "app", "--port", "8080"); err != nil {
			t.Fatalf("%s: init: %v", name, err)
		}
		content := readOrEmpty(t, dir, DefaultFile)
		app, err := spec.Parse([]byte(content))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if app.Init || !strings.Contains(content, "\n# init: true\n") {
			t.Errorf("%s: init should be there as an example only:\n%s", name, content)
		}
	}

	dir := t.TempDir()
	if _, _, err := f.run(dir, "init", "--name", "app", "--image", "ghcr.io/company/app:1.0.0"); err != nil {
		t.Fatalf("init --image: %v", err)
	}
	if content := readOrEmpty(t, dir, DefaultFile); !strings.Contains(content, "\n# init: true\n") || strings.Contains(content, "\ninit: true\n") {
		t.Errorf("an image from a registry is not known to need an init process:\n%s", content)
	}
}
