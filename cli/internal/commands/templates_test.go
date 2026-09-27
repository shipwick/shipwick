package commands

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/spec"
)

// sampleProjects is one detected project per template, with the variations
// that change the rendered lines.
func sampleProjects() map[string]project {
	npm := nodeProject{Manifests: "package.json package-lock.json",
		Install: "npm ci", InstallProd: "npm ci --omit=dev", Prune: "npm prune --omit=dev", Run: "npm run"}
	pnpm := nodeProject{Manifests: "package.json pnpm-lock.yaml", Corepack: true,
		Install: "pnpm install --frozen-lockfile", InstallProd: "pnpm install --prod --frozen-lockfile", Prune: "pnpm prune --prod", Run: "pnpm run"}
	return map[string]project{
		"nuxt":      {Kind: kindNuxt, Port: 3000, HealthPath: "/", HealthLive: true, Node: npm},
		"nuxt pnpm": {Kind: kindNuxt, Port: 3000, HealthPath: "/", HealthLive: true, Node: pnpm},
		"next standalone": {Kind: kindNext, Port: 3000, HealthPath: "/", HealthLive: true,
			Node: nodeProject{Manifests: npm.Manifests, Install: npm.Install, InstallProd: npm.InstallProd, Run: npm.Run, Standalone: true, HasPublic: true, NextConfig: "next.config.mjs"}},
		"next": {Kind: kindNext, Port: 3000, HealthPath: "/", HealthLive: true,
			Node: nodeProject{Manifests: npm.Manifests, Install: npm.Install, InstallProd: npm.InstallProd, Run: npm.Run, NextConfig: "next.config.ts"}},
		"node": {Kind: kindNode, Port: 4000, HealthPath: "/health",
			Node: nodeProject{Manifests: npm.Manifests, Install: npm.Install, Prune: npm.Prune, Run: npm.Run, HasBuild: true, Start: `["node", "dist/server.js"]`}},
		"node plain": {Kind: kindNode, Port: 3000, HealthPath: "/health",
			Node: nodeProject{Manifests: npm.Manifests, InstallProd: npm.InstallProd, Start: `["node", "server.js"]`}},
		"dotnet web": {Kind: kindDotnet, Port: 8080, HealthPath: "/health",
			Dotnet: dotnetProject{Project: "Api.csproj", Assembly: "Api", Version: "8.0", Web: true, Runtime: "aspnet", AppUser: true}},
		"dotnet worker on .NET 6": {Kind: kindDotnet,
			Dotnet: dotnetProject{Project: "Worker.csproj", Assembly: "Worker", Version: "6.0", Runtime: "runtime"}},
		"go":                {Kind: kindGo, Port: 8080, HealthPath: "/healthz", Go: goProject{Version: "1.27", HasSum: true, Package: "./cmd/api"}},
		"go without go.sum": {Kind: kindGo, Port: 8080, HealthPath: "/healthz", Go: goProject{Version: "1.26", Package: "."}},
		"python requirements": {Kind: kindPython, Port: 8000, HealthPath: "/health",
			Python: pythonProject{Version: "3.12", Requirements: true, Command: `["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000"]`}},
		"python pyproject": {Kind: kindPython, Port: 8000, HealthPath: "/health",
			Python: pythonProject{Version: "3.13", Command: `["gunicorn", "--bind", "0.0.0.0:8000", "site.wsgi:application"]`}},
	}
}

var (
	// shellTricks are what a Dockerfile written by hand for review never
	// needs: downloading a script into a shell, evaluating text, or a
	// shell-form CMD/ENTRYPOINT that puts sh between Docker and the process.
	shellTricks   = regexp.MustCompile(`\|\s*(sh|bash)\b|\beval\b|\$\(|` + "`")
	shellFormExec = regexp.MustCompile(`(?m)^(CMD|ENTRYPOINT)\s+[^\[]`)
)

func TestDockerfileTemplates(t *testing.T) {
	for name, p := range sampleProjects() {
		t.Run(name, func(t *testing.T) {
			dockerfile, err := renderDockerfile(p)
			if err != nil {
				t.Fatal(err)
			}
			if shellTricks.MatchString(dockerfile) {
				t.Errorf("Dockerfile uses a shell trick:\n%s", dockerfile)
			}
			if shellFormExec.MatchString(dockerfile) {
				t.Errorf("CMD and ENTRYPOINT must be in exec form:\n%s", dockerfile)
			}
			if strings.Contains(dockerfile, "{{") || strings.Contains(dockerfile, "<no value>") {
				t.Errorf("template left a hole:\n%s", dockerfile)
			}
			if strings.Contains(dockerfile, "\n\n\n") {
				t.Errorf("blank lines doubled:\n%s", dockerfile)
			}
			// A Node server with nothing to build has nothing to leave behind: one stage.
			stages := 2
			if p.Kind == kindNode && !p.Node.HasBuild {
				stages = 1
			}
			if strings.Count("\n"+dockerfile, "\nFROM ") != stages || (stages == 2) != strings.Contains(dockerfile, " AS build\n") {
				t.Errorf("expected %d stages:\n%s", stages, dockerfile)
			}
			if p.Port != 0 && !strings.Contains(dockerfile, "\nEXPOSE "+strconv.Itoa(p.Port)+"\n") {
				t.Errorf("port %d is not exposed:\n%s", p.Port, dockerfile)
			}
			if p.Port == 0 && strings.Contains(dockerfile, "EXPOSE") {
				t.Errorf("nothing listens, nothing to expose:\n%s", dockerfile)
			}
			if p.Kind != kindGo && !strings.Contains(dockerfile, "\nUSER ") {
				t.Errorf("the process must not run as root:\n%s", dockerfile)
			}
			if p.Kind == kindGo && !strings.Contains(dockerfile, ":nonroot\n") {
				t.Errorf("the distroless image must be the nonroot variant:\n%s", dockerfile)
			}

			ignore, err := renderDockerignore(p)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{".git\n", ".env\n", "deploy.yaml\n", "!.env.example\n"} {
				if !strings.Contains(ignore, want) {
					t.Errorf(".dockerignore lacks %q:\n%s", want, ignore)
				}
			}
		})
	}
}

func TestDockerfileTemplateDetails(t *testing.T) {
	samples := sampleProjects()
	render := func(name string) string {
		t.Helper()
		out, err := renderDockerfile(samples[name])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	contains := func(name string, wants ...string) {
		t.Helper()
		out := render(name)
		for _, want := range wants {
			if !strings.Contains(out, want) {
				t.Errorf("%s: missing %q in\n%s", name, want, out)
			}
		}
	}
	lacks := func(name string, unwanted ...string) {
		t.Helper()
		out := render(name)
		for _, u := range unwanted {
			if strings.Contains(out, u) {
				t.Errorf("%s: has %q in\n%s", name, u, out)
			}
		}
	}

	contains("nuxt", "COPY package.json package-lock.json ./\nRUN npm ci\n", "COPY --from=build /src/.output ./.output", `CMD ["node", ".output/server/index.mjs"]`, "PORT=3000")
	lacks("nuxt", "corepack")
	contains("nuxt pnpm", "RUN corepack enable\nCOPY package.json pnpm-lock.yaml ./\nRUN pnpm install --frozen-lockfile\n", "RUN pnpm run build")
	contains("next standalone", "/src/.next/standalone ./", "/src/.next/static ./.next/static", "/src/public ./public", `CMD ["node", "server.js"]`)
	lacks("next standalone", "next.config", "--omit=dev")
	contains("next", "RUN npm ci --omit=dev", "/src/.next ./.next", "/src/next.config.ts ./", `CMD ["node_modules/.bin/next", "start"]`)
	lacks("next", "public", "standalone")
	contains("node plain", "RUN npm ci --omit=dev\nCOPY --chown=node:node . .\n", `CMD ["node", "server.js"]`)
	lacks("node plain", "AS build", "prune")
	contains("node", "RUN npm run build\nRUN npm prune --omit=dev\n", "PORT=4000", "EXPOSE 4000", `CMD ["node", "dist/server.js"]`)
	contains("dotnet web", "sdk:8.0 AS build", "COPY Api.csproj ./", "aspnet:8.0", "ASPNETCORE_URLS=http://0.0.0.0:8080", "\nUSER app\n", `ENTRYPOINT ["dotnet", "Api.dll"]`)
	lacks("dotnet web", "useradd")
	contains("dotnet worker on .NET 6", "runtime:6.0", "RUN useradd --uid 1654 --user-group --no-create-home app\nUSER app")
	lacks("dotnet worker on .NET 6", "ASPNETCORE_URLS", "EXPOSE")
	contains("go", "golang:1.27-alpine", "COPY go.mod go.sum ./", "ENV CGO_ENABLED=0", "-o /out/app ./cmd/api", "distroless/static-debian12:nonroot")
	contains("go without go.sum", "COPY go.mod ./\n", "-o /out/app .\n")
	contains("python requirements", "python:3.12-slim", "COPY requirements.txt ./\nRUN pip install --no-cache-dir -r requirements.txt", "RUN useradd --create-home app\nUSER app", `CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000"]`)
	contains("python pyproject", "python:3.13-slim", "RUN pip install --no-cache-dir .\n")
	lacks("python pyproject", "requirements.txt")
}

func TestRenderConfigForProjects(t *testing.T) {
	for name, p := range sampleProjects() {
		t.Run(name, func(t *testing.T) {
			p := p
			content := renderConfig(initAnswers{Name: "my-app", Port: p.Port, Project: &p})
			app, err := spec.Parse([]byte(content))
			if err != nil {
				t.Fatalf("deploy.yaml does not validate: %v\n%s", err, content)
			}
			if strings.Contains(content, "image:") || !strings.Contains(content, "\nbuild: .\n") {
				t.Errorf("a recognised project builds its image:\n%s", content)
			}
			if app.Image != "" {
				t.Errorf("no image, got %q", app.Image)
			}
			if p.HealthLive {
				if app.Health == nil || app.Health.Path != p.HealthPath {
					t.Errorf("a framework serving / gets a live health check, got %+v\n%s", app.Health, content)
				}
			} else {
				if app.Health != nil {
					t.Errorf("a guessed endpoint would fail every deployment; health must be commented:\n%s", content)
				}
				if p.HealthPath != "" && !strings.Contains(content, "#   path: "+p.HealthPath+"\n") {
					t.Errorf("the commented example should use %s:\n%s", p.HealthPath, content)
				}
			}
			if has := strings.Contains(content, "#   start_period: 60s"); has != (p.Kind == kindDotnet) {
				t.Errorf("start_period example: got %v for %s\n%s", has, p.Kind, content)
			}
			if p.Port != 0 && app.Port != p.Port {
				t.Errorf("port = %d, want %d", app.Port, p.Port)
			}
		})
	}
}

func TestRenderConfigForStatic(t *testing.T) {
	built := &project{Kind: kindStatic, Static: staticProject{Dir: "dist/", Build: "npm run build"}}
	content := renderConfig(initAnswers{Name: "my-site", Project: built, Domain: "my-site.example.com"})
	if _, err := spec.Parse([]byte(content)); err != nil {
		t.Fatalf("deploy.yaml does not validate: %v\n%s", err, content)
	}
	for _, want := range []string{"name: my-site\n", "static: dist/\n", "`npm run build`", "\ndomain: my-site.example.com\n"} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %q:\n%s", want, content)
		}
	}
	for _, unwanted := range []string{"image:", "build:", "port:", "replicas:", "health:", "restart:"} {
		if strings.Contains(content, unwanted) {
			t.Errorf("a static site has no %s:\n%s", unwanted, content)
		}
	}

	plain := &project{Kind: kindStatic, Static: staticProject{Dir: "."}}
	content = renderConfig(initAnswers{Name: "my-site", Project: plain, Domain: "www.example.com"})
	if !strings.Contains(content, "static: .\n") || !strings.Contains(content, "\ndomain: www.example.com\n") || strings.Contains(content, "run that before") {
		t.Errorf("unexpected static config:\n%s", content)
	}
}

func TestRenderConfigWithImageIsUnchanged(t *testing.T) {
	content := renderConfig(initAnswers{Name: "my-api", Image: "img:1", Port: 8080})
	for _, want := range []string{"image: img:1\n", "port: 8080\n", "#   tcp: 5432", "# health:\n#   path: /health\n"} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "build:") || strings.Contains(content, "start_period") {
		t.Errorf("an image project keeps the plain template:\n%s", content)
	}
}
