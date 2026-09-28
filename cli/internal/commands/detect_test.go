package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTree creates the marker files of a project in a fresh directory.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDetectProject(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  project
		// check inspects the kind-specific details worth pinning.
		check func(t *testing.T, p project)
	}{
		{
			name:  "an empty directory is not recognised",
			files: map[string]string{"README.md": "hello"},
			want:  project{},
		},
		{
			name:  "Nuxt from its dependency, npm from its lock file",
			files: map[string]string{"package.json": `{"dependencies":{"nuxt":"^4"},"scripts":{"build":"nuxt build"}}`, "package-lock.json": "{}"},
			want:  project{Kind: kindNuxt, Label: "a Nuxt application", Port: 3000, HealthPath: "/", HealthLive: true},
			check: func(t *testing.T, p project) {
				if p.Node.Install != "npm ci" || p.Node.Manifests != "package.json package-lock.json" || p.Node.Corepack {
					t.Errorf("node = %+v", p.Node)
				}
			},
		},
		{
			// PowerShell's Set-Content writes one; npm reads past it, so must init.
			name:  "a package.json with a byte-order mark",
			files: map[string]string{"package.json": "\xef\xbb\xbf" + `{"dependencies":{"nuxt":"^4"}}`},
			want:  project{Kind: kindNuxt, Label: "a Nuxt application", Port: 3000, HealthPath: "/", HealthLive: true},
		},
		{
			name:  "pnpm from its lock file",
			files: map[string]string{"package.json": `{"dependencies":{"nuxt":"^4"}}`, "pnpm-lock.yaml": ""},
			want:  project{Kind: kindNuxt, Label: "a Nuxt application", Port: 3000, HealthPath: "/", HealthLive: true},
			check: func(t *testing.T, p project) {
				if p.Node.Install != "pnpm install --frozen-lockfile" || !p.Node.Corepack || p.Node.Run != "pnpm run" {
					t.Errorf("node = %+v", p.Node)
				}
			},
		},
		{
			name: "Next with standalone output and a public folder",
			files: map[string]string{
				"package.json":       `{"dependencies":{"next":"15","react":"19"},"scripts":{"build":"next build","start":"next start"}}`,
				"next.config.mjs":    `export default { output: "standalone" }`,
				"public/favicon.ico": "",
			},
			want: project{Kind: kindNext, Label: "a Next.js application", Port: 3000, HealthPath: "/", HealthLive: true},
			check: func(t *testing.T, p project) {
				if !p.Node.Standalone || !p.Node.HasPublic || p.Node.NextConfig != "next.config.mjs" {
					t.Errorf("node = %+v", p.Node)
				}
			},
		},
		{
			name:  "Next without standalone output",
			files: map[string]string{"package.json": `{"dependencies":{"next":"15"}}`, "next.config.ts": `export default {}`},
			want:  project{Kind: kindNext, Label: "a Next.js application", Port: 3000, HealthPath: "/", HealthLive: true},
			check: func(t *testing.T, p project) {
				if p.Node.Standalone || p.Node.HasPublic || p.Node.NextConfig != "next.config.ts" {
					t.Errorf("node = %+v", p.Node)
				}
			},
		},
		{
			name:  "an Express server, its port read from the start script",
			files: map[string]string{"package.json": `{"dependencies":{"express":"^5"},"scripts":{"start":"PORT=4000 node server.js"}}`},
			want:  project{Kind: kindNode, Label: "a Node.js application", Port: 4000, HealthPath: "/health"},
			check: func(t *testing.T, p project) {
				if p.Node.Start != `["node", "server.js"]` || p.Node.HasBuild {
					t.Errorf("node = %+v", p.Node)
				}
			},
		},
		{
			name:  "a start script that needs a shell is run through npm",
			files: map[string]string{"package.json": `{"scripts":{"start":"node -r dotenv/config server.js && echo up","build":"tsc"}}`},
			want:  project{Kind: kindNode, Label: "a Node.js application", Port: 3000, HealthPath: "/health"},
			check: func(t *testing.T, p project) {
				if p.Node.Start != `["npm", "start"]` || !p.Node.HasBuild {
					t.Errorf("node = %+v", p.Node)
				}
			},
		},
		{
			name:  "a framework without a start script runs its main file",
			files: map[string]string{"package.json": `{"main":"src/index.js","dependencies":{"hono":"^4"}}`},
			want:  project{Kind: kindNode, Label: "a Node.js application", Port: 3000, HealthPath: "/health"},
			check: func(t *testing.T, p project) {
				if p.Node.Start != `["node", "src/index.js"]` {
					t.Errorf("start = %s", p.Node.Start)
				}
			},
		},
		{
			name:  "a Vite project is a static site built by npm",
			files: map[string]string{"package.json": `{"devDependencies":{"vite":"^6"},"scripts":{"build":"vite build"}}`, "package-lock.json": "{}"},
			want:  project{Kind: kindStatic, Label: "a site built by npm run build"},
			check: func(t *testing.T, p project) {
				if p.Static.Dir != "dist/" || p.Static.Build != "npm run build" {
					t.Errorf("static = %+v", p.Static)
				}
			},
		},
		{
			name:  "Vite with a server framework is a server, not a site",
			files: map[string]string{"package.json": `{"dependencies":{"vite":"^6","express":"^5"},"scripts":{"build":"vite build","start":"node server.js"}}`},
			want:  project{Kind: kindNode, Label: "a Node.js application", Port: 3000, HealthPath: "/health"},
		},
		{
			name:  "a package.json without a server or a build is not recognised",
			files: map[string]string{"package.json": `{"name":"lib","devDependencies":{"prettier":"3"}}`},
			want:  project{},
		},
		{
			name:  "a package.json project's public folder is source, not output",
			files: map[string]string{"package.json": `{"name":"lib"}`, "public/index.html": "<html>"},
			want:  project{},
		},
		{
			name:  "broken package.json is an error, not the generic questions",
			files: map[string]string{"package.json": `{"name":`},
			want:  project{},
		},
		{
			name: "ASP.NET Core from the Web SDK and TargetFramework",
			files: map[string]string{"Api.csproj": `<Project Sdk="Microsoft.NET.Sdk.Web">
  <PropertyGroup><TargetFramework>net9.0</TargetFramework><Nullable>enable</Nullable></PropertyGroup>
</Project>`},
			want: project{Kind: kindDotnet, Label: "a .NET application (Api.csproj)", Port: 8080, HealthPath: "/health"},
			check: func(t *testing.T, p project) {
				want := dotnetProject{Project: "Api.csproj", Assembly: "Api", Version: "9.0", Web: true, Runtime: "aspnet", AppUser: true}
				if p.Dotnet != want {
					t.Errorf("dotnet = %+v, want %+v", p.Dotnet, want)
				}
			},
		},
		{
			name: "a .NET worker has no port; the assembly name is honoured",
			files: map[string]string{"Worker.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFrameworks>net6.0;net8.0</TargetFrameworks><AssemblyName>Company.Worker</AssemblyName></PropertyGroup>
</Project>`},
			want: project{Kind: kindDotnet, Label: "a .NET application (Worker.csproj)", HealthPath: ""},
			check: func(t *testing.T, p project) {
				want := dotnetProject{Project: "Worker.csproj", Assembly: "Company.Worker", Version: "6.0", Runtime: "runtime"}
				if p.Dotnet != want {
					t.Errorf("dotnet = %+v, want %+v", p.Dotnet, want)
				}
			},
		},
		{
			name:  "a Go program at the module root",
			files: map[string]string{"go.mod": "module example.com/api\n\ngo 1.27.1\n", "go.sum": "", "main.go": "package main\n\nfunc main() {}\n", "main_test.go": "package main\n"},
			want:  project{Kind: kindGo, Label: "a Go program", Port: 8080, HealthPath: "/healthz"},
			check: func(t *testing.T, p project) {
				if p.Go.Version != "1.27" || !p.Go.HasSum || p.Go.Package != "." || len(p.Go.Mains) != 1 {
					t.Errorf("go = %+v", p.Go)
				}
			},
		},
		{
			name: "several Go programs under cmd are listed, none chosen",
			files: map[string]string{
				"go.mod":              "module example.com/svc\n\ngo 1.26\n",
				"internal/x/x.go":     "package x\n",
				"cmd/api/main.go":     "package main\n",
				"cmd/worker/main.go":  "package main\n",
				"cmd/tools/README.md": "",
			},
			want: project{Kind: kindGo, Label: "a Go program", Port: 8080, HealthPath: "/healthz"},
			check: func(t *testing.T, p project) {
				if strings.Join(p.Go.Mains, " ") != "./cmd/api ./cmd/worker" || p.Go.Package != "" || p.Go.HasSum {
					t.Errorf("go = %+v", p.Go)
				}
			},
		},
		{
			name:  "a Go library is not a program",
			files: map[string]string{"go.mod": "module example.com/lib\n\ngo 1.26\n", "lib.go": "package lib\n"},
			want:  project{},
		},
		{
			name:  "FastAPI runs under uvicorn, at the module the tutorial uses",
			files: map[string]string{"requirements.txt": "fastapi[standard]==0.115.0\nsqlalchemy\n", "app/main.py": "app = FastAPI()\n", ".python-version": "3.12.4\n"},
			want:  project{Kind: kindPython, Label: "a Python application", Port: 8000, HealthPath: "/health"},
			check: func(t *testing.T, p project) {
				want := pythonProject{Version: "3.12", Requirements: true, Command: `["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8000"]`}
				if p.Python != want {
					t.Errorf("python = %+v, want %+v", p.Python, want)
				}
			},
		},
		{
			name: "Django runs under gunicorn from its wsgi module; the version from pyproject",
			files: map[string]string{
				"pyproject.toml":     "[project]\nname = \"site\"\nrequires-python = \">=3.11\"\ndependencies = [\n  \"django>=5\",\n  \"gunicorn\",\n]\n",
				"manage.py":          "",
				"mysite/wsgi.py":     "",
				"mysite/__init__.py": "",
			},
			want: project{Kind: kindPython, Label: "a Python application", Port: 8000, HealthPath: "/health"},
			check: func(t *testing.T, p project) {
				want := pythonProject{Version: "3.11", Command: `["gunicorn", "--bind", "0.0.0.0:8000", "mysite.wsgi:application"]`}
				if p.Python != want {
					t.Errorf("python = %+v, want %+v", p.Python, want)
				}
			},
		},
		{
			name:  "a script without a server framework is run with python",
			files: map[string]string{"requirements.txt": "requests\n", "app.py": ""},
			want:  project{Kind: kindPython, Label: "a Python application", Port: 8000, HealthPath: "/health"},
			check: func(t *testing.T, p project) {
				if p.Python.Command != `["python", "app.py"]` || p.Python.Version != "3.13" {
					t.Errorf("python = %+v", p.Python)
				}
			},
		},
		{
			name:  "an index.html at the root is a static site",
			files: map[string]string{"index.html": "<html>", "style.css": ""},
			want:  project{Kind: kindStatic, Label: "a static site"},
			check: func(t *testing.T, p project) {
				if p.Static.Dir != "." {
					t.Errorf("static = %+v", p.Static)
				}
			},
		},
		{
			name:  "a build output folder is a static site",
			files: map[string]string{"dist/index.html": "<html>", "dist/app.js": ""},
			want:  project{Kind: kindStatic, Label: "a static site (dist/)"},
			check: func(t *testing.T, p project) {
				if p.Static.Dir != "dist/" {
					t.Errorf("static = %+v", p.Static)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeTree(t, tt.files)
			got, err := detectProject(dir)
			if strings.HasPrefix(tt.name, "broken") {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("detectProject: %v", err)
			}
			if got.Kind != tt.want.Kind || got.Label != tt.want.Label || got.Port != tt.want.Port ||
				got.HealthPath != tt.want.HealthPath || got.HealthLive != tt.want.HealthLive {
				t.Errorf("got kind=%q label=%q port=%d health=%q live=%v\nwant kind=%q label=%q port=%d health=%q live=%v",
					got.Kind, got.Label, got.Port, got.HealthPath, got.HealthLive,
					tt.want.Kind, tt.want.Label, tt.want.Port, tt.want.HealthPath, tt.want.HealthLive)
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}
