package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// projectKind is what `shipwick init` recognised in a directory. It picks the
// Dockerfile template and the shape of the deploy.yaml.
type projectKind string

const (
	kindNuxt   projectKind = "nuxt"
	kindNext   projectKind = "next"
	kindSvelte projectKind = "sveltekit"
	kindNode   projectKind = "node"
	kindDotnet projectKind = "dotnet"
	kindGo     projectKind = "go"
	kindPython projectKind = "python"
	kindStatic projectKind = "static"
)

// project is what detectProject found: enough for the templates to write a
// Dockerfile that works as it is, and for the message that says what was
// recognised. The zero value means nothing was recognised.
type project struct {
	Kind projectKind
	// Label completes "Recognised ...": "a Nuxt application".
	Label string
	// Port the program listens on inside the container; 0 for none.
	Port int
	// HealthPath is the conventional health endpoint of the kind. Live
	// (uncommented) in deploy.yaml only when the framework is known to
	// answer it: a framework serving `/`.
	HealthPath string
	HealthLive bool

	Node   nodeProject
	Dotnet dotnetProject
	Go     goProject
	Python pythonProject
	Static staticProject
}

// nodeProject is a package.json project. The install lines follow the lock
// file found, so that the image is built from the same dependency tree the
// developer runs.
type nodeProject struct {
	Manifests   string // files copied before installing: "package.json package-lock.json"
	Corepack    bool   // pnpm and yarn come through corepack, npm is in the image
	Install     string // "npm ci"
	InstallProd string // production dependencies only, for the runtime stage
	Prune       string // drop development dependencies after a build
	Run         string // "npm run"
	HasBuild    bool   // a build script exists (TypeScript, bundlers)
	HasPublic   bool   // a public/ folder to copy next to the build output
	NextConfig  string // the next.config file, copied into the runtime image
	Standalone  bool   // Next: output: "standalone" is set
	Start       string // the CMD, as a JSON array
	StaticDir   string // Vite, Astro: the folder the build script produces
	NoLock      bool   // no lock file: the dependency tree is resolved anew on every build
	Host        bool   // the server listens on localhost unless HOST says otherwise
}

type dotnetProject struct {
	Project  string // the .csproj file name
	Assembly string // the .dll dotnet runs
	Version  string // SDK and runtime image tag: "8.0"
	Web      bool   // Microsoft.NET.Sdk.Web: listens on a port
	Runtime  string // "aspnet" or "runtime"
	AppUser  bool   // the images ship a non-root `app` user since 8.0
}

type goProject struct {
	Version string   // "1.27": the golang image tag
	HasSum  bool     // go.sum exists (a module without dependencies has none)
	Package string   // the main package to build: "." or "./cmd/api"
	Mains   []string // every main package found; the caller chooses when several
}

type pythonProject struct {
	Version      string // "3.13": the python image tag
	Requirements bool   // requirements.txt exists; otherwise pyproject.toml is installed
	Uv           bool   // uv.lock exists: the build installs exactly what it pins
	Command      string // the CMD, as a JSON array
}

type staticProject struct {
	Dir   string // the folder to serve, relative to deploy.yaml
	Build string // the script that produces it, when it is a build output: "npm run build"
	// Fallback is the page that answers the paths naming no file, for a
	// single-page application whose router reads the path in the browser.
	Fallback string
}

// detectProject looks at dir and reports what kind of application it holds.
// A zero project means nothing was recognised; an error means a marker file
// exists but could not be read, which the user should hear about rather than
// be asked the generic questions.
func detectProject(dir string) (project, error) {
	if exists(dir, "package.json") {
		p, recognised, err := detectNode(dir)
		if err != nil || recognised {
			return p, err
		}
	}
	if csproj := firstMatch(dir, "*.csproj"); csproj != "" {
		return detectDotnet(dir, csproj)
	}
	if exists(dir, "go.mod") {
		p, recognised, err := detectGo(dir)
		if err != nil || recognised {
			return p, err
		}
	}
	if exists(dir, "pyproject.toml") || exists(dir, "requirements.txt") {
		return detectPython(dir)
	}
	return detectStatic(dir), nil
}

func exists(dir string, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func firstMatch(dir, pattern string) string {
	matches, _ := filepath.Glob(filepath.Join(dir, pattern))
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return filepath.Base(matches[0])
}

// packageJSON is the part of package.json that decides the kind of project.
type packageJSON struct {
	Main            string            `json:"main"`
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func (pkg packageJSON) depends(name string) bool {
	_, dep := pkg.Dependencies[name]
	_, dev := pkg.DevDependencies[name]
	return dep || dev
}

var (
	nodeServerFrameworks = []string{"express", "fastify", "koa", "hono"}
	scriptPortPattern    = regexp.MustCompile(`\bPORT=(\d{1,5})\b`)
	// shellPattern marks a start script that only a shell can run; the
	// Dockerfile then goes through the package manager instead of guessing.
	shellPattern = regexp.MustCompile("[&|;<>$`\"'\\\\*?()]")
	// nextExport is `output: "export"` in a next.config file: the build
	// writes plain files to out/ and there is no server to run.
	nextExport = regexp.MustCompile("\\boutput\\s*:\\s*[\"'`]export[\"'`]")
)

func detectNode(dir string) (project, bool, error) {
	data, err := readProjectFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return project{}, false, err
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return project{}, false, fmt.Errorf("package.json: %w", err)
	}

	p := project{Port: 3000}
	if m := scriptPortPattern.FindStringSubmatch(pkg.Scripts["start"]); m != nil {
		fmt.Sscanf(m[1], "%d", &p.Port)
	}
	p.Node = nodeInstall(dir)
	_, p.Node.HasBuild = pkg.Scripts["build"]
	p.Node.HasPublic = exists(dir, "public")

	server := false
	for _, f := range nodeServerFrameworks {
		server = server || pkg.depends(f)
	}
	switch {
	case pkg.depends("nuxt"):
		p.Kind, p.Label = kindNuxt, "a Nuxt application"
		p.HealthPath, p.HealthLive = "/", true
	case pkg.depends("next"):
		p.Kind, p.Label = kindNext, "a Next.js application"
		p.HealthPath, p.HealthLive = "/", true
		p.Node.NextConfig = firstMatch(dir, "next.config.*")
		if p.Node.NextConfig != "" {
			config, _ := readProjectFile(filepath.Join(dir, p.Node.NextConfig))
			p.Node.Standalone = strings.Contains(string(config), "standalone")
			if nextExport.Match(config) {
				p = builtSite(p, "a Next.js site exported by "+p.Node.Run+" build", "out/")
			}
		}
	case pkg.depends("@sveltejs/kit"):
		switch {
		case pkg.depends("@sveltejs/adapter-node"):
			p.Kind, p.Label = kindSvelte, "a SvelteKit application"
			p.HealthPath, p.HealthLive = "/", true
		case pkg.depends("@sveltejs/adapter-static"):
			p = builtSite(p, "a SvelteKit site built by "+p.Node.Run+" build", "build/")
		default:
			// adapter-auto builds for the platforms it knows, and a server
			// of one's own is none of them.
			return project{}, false, errors.New("this SvelteKit project has no adapter that Shipwick can deploy\n\nInstall @sveltejs/adapter-node (a server) or @sveltejs/adapter-static (files the proxy serves), set it in svelte.config.js, and run shipwick init again")
		}
	case pkg.depends("@remix-run/node") || pkg.depends("@remix-run/serve"):
		p.Kind, p.Label = kindNode, "a Remix application"
		p.HealthPath, p.HealthLive = "/", true
		p.Node.Start = remixStart(pkg)
	case pkg.depends("astro") && pkg.depends("@astrojs/node") && p.Node.HasBuild:
		// The Node adapter's standalone server; without the adapter Astro
		// builds to files, which is the next case.
		p.Kind, p.Label = kindNode, "an Astro application"
		p.HealthPath, p.HealthLive = "/", true
		p.Node.Host = true
		p.Node.Start = jsonArray("node", "./dist/server/entry.mjs")
		if !scriptPortPattern.MatchString(pkg.Scripts["start"]) {
			p.Port = 4321
		}
	case (pkg.depends("vite") || pkg.depends("astro")) && p.Node.HasBuild && !server:
		// A frontend that builds to files: the proxy serves those, no container.
		p = builtSite(p, "a site built by "+p.Node.Run+" build", "dist/")
		if !pkg.depends("astro") {
			// Vite alone builds one page and routes in the browser; Astro
			// builds a file for every page.
			p.Static.Fallback = "index.html"
		}
	case server || pkg.Scripts["start"] != "":
		p.Kind, p.Label = kindNode, "a Node.js application"
		p.HealthPath = "/health"
		p.Node.Start = nodeStart(pkg)
	default:
		return project{}, false, nil
	}
	return p, true, nil
}

// builtSite turns a package.json project into the folder its build script
// writes.
func builtSite(p project, label, dir string) project {
	p.Kind, p.Label, p.Port = kindStatic, label, 0
	p.HealthPath, p.HealthLive = "", false
	p.Static = staticProject{Dir: dir, Build: p.Node.Run + " build"}
	return p
}

// remixStart is the CMD of a Remix application: remix-serve with the
// arguments of the start script, run directly so that it is PID 1. A start
// script of another shape — a server of one's own — is treated like any Node
// project's.
func remixStart(pkg packageJSON) string {
	fields := strings.Fields(pkg.Scripts["start"])
	switch {
	case len(fields) == 0 && pkg.depends("@remix-run/serve"):
		return jsonArray("node_modules/.bin/remix-serve", "./build/server/index.js")
	case len(fields) > 0 && fields[0] == "remix-serve" && !shellPattern.MatchString(strings.Join(fields, " ")):
		return jsonArray(append([]string{"node_modules/.bin/remix-serve"}, fields[1:]...)...)
	}
	return nodeStart(pkg)
}

// nodeInstall picks the package manager from the lock file that is present.
func nodeInstall(dir string) nodeProject {
	switch {
	case exists(dir, "pnpm-lock.yaml"):
		return nodeProject{
			Manifests: "package.json pnpm-lock.yaml", Corepack: true,
			Install: "pnpm install --frozen-lockfile", InstallProd: "pnpm install --prod --frozen-lockfile",
			Prune: "pnpm prune --prod", Run: "pnpm run",
		}
	case exists(dir, "yarn.lock"):
		return nodeProject{
			Manifests: "package.json yarn.lock", Corepack: true,
			Install: "yarn install --frozen-lockfile", InstallProd: "yarn install --production --frozen-lockfile",
			Prune: "yarn install --production --frozen-lockfile", Run: "yarn",
		}
	case exists(dir, "package-lock.json"):
		return nodeProject{
			Manifests: "package.json package-lock.json",
			Install:   "npm ci", InstallProd: "npm ci --omit=dev", Prune: "npm prune --omit=dev", Run: "npm run",
		}
	}
	// No lock file: the build cannot be reproducible, but it can work.
	return nodeProject{
		Manifests: "package.json", NoLock: true,
		Install: "npm install", InstallProd: "npm install --omit=dev", Prune: "npm prune --omit=dev", Run: "npm run",
	}
}

// nodeStart turns the start script into an exec-form CMD, so that the
// process is PID 1 and receives the signals Docker sends. A script that
// needs a shell is left to `npm start`.
func nodeStart(pkg packageJSON) string {
	script := strings.TrimSpace(pkg.Scripts["start"])
	if script == "" {
		main := pkg.Main
		if main == "" {
			main = "index.js"
		}
		return jsonArray("node", main)
	}
	fields := strings.Fields(script)
	// A leading PORT=3000 was read into the port already.
	for len(fields) > 0 && scriptPortPattern.MatchString(fields[0]) {
		fields = fields[1:]
	}
	if len(fields) == 0 || fields[0] != "node" || shellPattern.MatchString(strings.Join(fields, " ")) {
		return jsonArray("npm", "start")
	}
	return jsonArray(fields...)
}

// jsonArray formats an exec-form CMD the way the Dockerfiles are written by hand.
func jsonArray(args ...string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		out, _ := json.Marshal(arg)
		quoted[i] = string(out)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

var (
	csprojTag     = regexp.MustCompile(`<(TargetFramework|TargetFrameworks|AssemblyName)>([^<]*)</`)
	csprojSDK     = regexp.MustCompile(`Sdk="([^"]*)"`)
	frameworkPatt = regexp.MustCompile(`^net(coreapp)?(\d+\.\d+)$`)
)

func detectDotnet(dir, csproj string) (project, error) {
	data, err := readProjectFile(filepath.Join(dir, csproj))
	if err != nil {
		return project{}, err
	}
	d := dotnetProject{
		Project:  csproj,
		Assembly: strings.TrimSuffix(csproj, ".csproj"),
		Version:  "8.0",
		Runtime:  "runtime",
	}
	for _, m := range csprojTag.FindAllStringSubmatch(string(data), -1) {
		value := strings.TrimSpace(strings.Split(m[2], ";")[0])
		switch m[1] {
		case "TargetFramework", "TargetFrameworks":
			if f := frameworkPatt.FindStringSubmatch(value); f != nil {
				d.Version = f[2]
			}
		case "AssemblyName":
			if value != "" {
				d.Assembly = value
			}
		}
	}
	if m := csprojSDK.FindStringSubmatch(string(data)); m != nil && strings.HasSuffix(m[1], ".Web") {
		d.Web, d.Runtime = true, "aspnet"
	}
	var major int
	fmt.Sscanf(d.Version, "%d", &major)
	d.AppUser = major >= 8

	p := project{Kind: kindDotnet, Label: "a .NET application (" + csproj + ")", Dotnet: d}
	if d.Web {
		p.Port, p.HealthPath = 8080, "/health"
	}
	return p, nil
}

var goDirective = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+)`)

func detectGo(dir string) (project, bool, error) {
	data, err := readProjectFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return project{}, false, err
	}
	g := goProject{Version: "1.25", HasSum: exists(dir, "go.sum")}
	if m := goDirective.FindStringSubmatch(string(data)); m != nil {
		g.Version = m[1]
	}

	if isMainPackage(dir) {
		g.Mains = append(g.Mains, ".")
	}
	cmds, _ := filepath.Glob(filepath.Join(dir, "cmd", "*"))
	sort.Strings(cmds)
	for _, cmd := range cmds {
		if isMainPackage(cmd) {
			g.Mains = append(g.Mains, "./cmd/"+filepath.Base(cmd))
		}
	}
	if len(g.Mains) == 0 {
		return project{}, false, nil // a library: nothing to run
	}
	if len(g.Mains) == 1 {
		g.Package = g.Mains[0]
	}
	p := project{Kind: kindGo, Port: 8080, HealthPath: "/healthz", Go: g}
	p.Label = goLabel(g.Package)
	return p, true, nil
}

func goLabel(pkg string) string {
	if pkg == "" || pkg == "." {
		return "a Go program"
	}
	return "a Go program (" + strings.TrimPrefix(pkg, "./") + ")"
}

var packageMain = regexp.MustCompile(`(?m)^package\s+main\b`)

// isMainPackage reports whether the .go files directly in dir declare package
// main. Test files are skipped: a package's tests may be `package main` too.
func isMainPackage(dir string) bool {
	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := readProjectFile(f)
		if err == nil && packageMain.Match(data) {
			return true
		}
	}
	return false
}

var (
	requiresPython = regexp.MustCompile(`requires-python\s*=\s*"[^\d]*(\d+\.\d+)`)
	pythonVersion  = regexp.MustCompile(`^(\d+\.\d+)`)
)

func detectPython(dir string) (project, error) {
	var deps strings.Builder
	py := pythonProject{Version: "3.13", Uv: exists(dir, "uv.lock") && exists(dir, "pyproject.toml")}
	for _, name := range []string{"requirements.txt", "pyproject.toml"} {
		data, err := readProjectFile(filepath.Join(dir, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return project{}, err
		}
		deps.Write(data)
		deps.WriteByte('\n')
		if name == "requirements.txt" {
			py.Requirements = true
		} else if m := requiresPython.FindStringSubmatch(string(data)); m != nil {
			py.Version = m[1]
		}
	}
	if data, err := readProjectFile(filepath.Join(dir, ".python-version")); err == nil {
		if m := pythonVersion.FindStringSubmatch(strings.TrimSpace(string(data))); m != nil {
			py.Version = m[1]
		}
	}

	text := strings.ToLower(deps.String())
	switch {
	case mentionsPackage(text, "uvicorn") || mentionsPackage(text, "fastapi"):
		py.Command = jsonArray("uvicorn", pythonModule(dir)+":app", "--host", "0.0.0.0", "--port", "8000")
	case mentionsPackage(text, "gunicorn") || mentionsPackage(text, "flask") || mentionsPackage(text, "django"):
		app := pythonModule(dir) + ":app"
		// Django's startproject puts wsgi.py in the package named after the project.
		if matches, _ := filepath.Glob(filepath.Join(dir, "*", "wsgi.py")); len(matches) > 0 {
			sort.Strings(matches)
			app = filepath.Base(filepath.Dir(matches[0])) + ".wsgi:application"
		}
		py.Command = jsonArray("gunicorn", "--bind", "0.0.0.0:8000", app)
	default:
		py.Command = jsonArray("python", pythonModule(dir)+".py")
	}
	return project{Kind: kindPython, Label: "a Python application", Port: 8000, HealthPath: "/health", Python: py}, nil
}

// mentionsPackage reports whether a dependency list (requirements.txt lines,
// or pyproject's quoted entries) names pkg, as opposed to a package whose
// name merely starts with it.
func mentionsPackage(deps, pkg string) bool {
	return regexp.MustCompile(`(?m)^[\s"']*` + pkg + `([^a-z0-9_-]|$)`).MatchString(deps)
}

// pythonModule finds the module that holds the application object, in the
// places the frameworks' own tutorials put it.
func pythonModule(dir string) string {
	for _, candidate := range []string{"main.py", "app.py", "app/main.py", "src/main.py"} {
		if exists(dir, candidate) {
			return strings.ReplaceAll(strings.TrimSuffix(candidate, ".py"), "/", ".")
		}
	}
	return "main"
}

// detectStatic finds files to serve as they are: an index.html at the root,
// or a build output folder holding one. public/ is skipped for a package.json
// project, where it holds sources, not output.
func detectStatic(dir string) project {
	if exists(dir, "index.html") {
		return project{Kind: kindStatic, Label: "a static site", Static: staticProject{Dir: "."}}
	}
	folders := []string{"dist", "build", "out", "public"}
	if exists(dir, "package.json") {
		folders = folders[:3]
	}
	for _, folder := range folders {
		if exists(dir, filepath.Join(folder, "index.html")) {
			return project{Kind: kindStatic, Label: "a static site (" + folder + "/)", Static: staticProject{Dir: folder + "/"}}
		}
	}
	return project{}
}

// readProjectFile reads a file the way its own tools do: without the UTF-8
// byte-order mark that Windows editors and PowerShell's Set-Content put at
// the start of a file, which json and xml would otherwise refuse.
func readProjectFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), nil
}

// startsNode reports whether the Dockerfile written for the project runs Node
// as the container's first process — directly, or as the program behind
// `next start`, `remix-serve` or `npm start`. Such a process gets no SIGTERM
// it did not install a handler for, and the frameworks that do install one
// lose nothing by an init process in front of them.
func startsNode(p *project) bool {
	if p == nil {
		return false
	}
	switch p.Kind {
	case kindNuxt, kindNext, kindSvelte, kindNode:
		return true
	}
	return false
}
