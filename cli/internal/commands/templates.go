package commands

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"
)

// templateFS holds one Dockerfile and one .dockerignore per kind of project.
// They are the files a careful engineer writes by hand: a build stage, a
// small runtime image, a non-root user, the port exposed, the process in
// exec form so that it is PID 1 and receives Docker's signals.
//
//go:embed templates/*
var templateFS embed.FS

var templates = template.Must(template.ParseFS(templateFS, "templates/*"))

// renderDockerfile writes the Dockerfile for a recognised project.
func renderDockerfile(p project) (string, error) {
	var b bytes.Buffer
	if err := templates.ExecuteTemplate(&b, string(p.Kind)+".Dockerfile", p); err != nil {
		return "", fmt.Errorf("Dockerfile template for %s: %w", p.Kind, err)
	}
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}

// renderDockerignore writes the .dockerignore that goes with the Dockerfile.
// Nuxt, Next and plain Node share one: their output folders differ, and
// excluding every one of them costs nothing.
func renderDockerignore(p project) (string, error) {
	name := string(p.Kind)
	switch p.Kind {
	case kindNuxt, kindNext:
		name = string(kindNode)
	}
	var b bytes.Buffer
	if err := templates.ExecuteTemplate(&b, name+".dockerignore", p); err != nil {
		return "", fmt.Errorf(".dockerignore template for %s: %w", p.Kind, err)
	}
	return b.String(), nil
}
