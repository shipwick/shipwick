package spec

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/distribution/reference"
	"go.yaml.in/yaml/v3"
)

// MaxConfigBytes bounds the size of a deploy.yaml document.
const MaxConfigBytes = 64 * 1024

// MaxReplicas is a sanity bound for a single server.
const MaxReplicas = 50

// MaxVolumes bounds the volumes of one application.
const MaxVolumes = 10

// raw mirrors deploy.yaml before validation. Values with their own syntax
// (sizes, durations, cpu) are decoded as strings so that a bad value yields a
// field-level error instead of a generic YAML type error.
type raw struct {
	Name      string            `yaml:"name"`
	Image     string            `yaml:"image"`
	Port      *int              `yaml:"port"`
	Domain    string            `yaml:"domain"`
	Aliases   []string          `yaml:"aliases"`
	Redirects []string          `yaml:"redirects"`
	Replicas  *int              `yaml:"replicas"`
	Env       map[string]string `yaml:"env"`
	Health    *struct {
		Path        string   `yaml:"path"`
		TCP         *int     `yaml:"tcp"`
		Command     []string `yaml:"command"`
		Interval    string   `yaml:"interval"`
		Timeout     string   `yaml:"timeout"`
		Retries     *int     `yaml:"retries"`
		StartPeriod string   `yaml:"start_period"`
	} `yaml:"health"`
	Resources struct {
		CPU    string `yaml:"cpu"`
		Memory string `yaml:"memory"`
	} `yaml:"resources"`
	Volumes []struct {
		Name string `yaml:"name"`
		Path string `yaml:"path"`
	} `yaml:"volumes"`
	Publish []struct {
		Port     *int   `yaml:"port"`
		Host     *int   `yaml:"host"`
		Address  string `yaml:"address"`
		Protocol string `yaml:"protocol"`
	} `yaml:"publish"`
	Entrypoint argv   `yaml:"entrypoint"`
	Command    argv   `yaml:"command"`
	User       string `yaml:"user"`
	PreDeploy  *struct {
		Command []string `yaml:"command"`
		Timeout string   `yaml:"timeout"`
	} `yaml:"pre_deploy"`
	Jobs []struct {
		Name     string   `yaml:"name"`
		Schedule string   `yaml:"schedule"`
		Command  []string `yaml:"command"`
		Timeout  string   `yaml:"timeout"`
	} `yaml:"jobs"`
	Logging *struct {
		Driver  string            `yaml:"driver"`
		Options map[string]string `yaml:"options"`
	} `yaml:"logging"`
	Build   buildRaw  `yaml:"build"`
	Static  staticRaw `yaml:"static"`
	Path    string    `yaml:"path"`
	Proxy   yaml.Node `yaml:"proxy"`
	Backups yaml.Node `yaml:"backups"`
	Restart struct {
		Policy string `yaml:"policy"`
	} `yaml:"restart"`
	Deploy struct {
		Strategy    string `yaml:"strategy"`
		StopTimeout string `yaml:"stop_timeout"`
	} `yaml:"deploy"`
	Init *bool `yaml:"init"`
}

// Parse decodes and validates a deploy.yaml document. JSON is accepted too,
// since it is a subset of YAML. On invalid input the returned error is a
// *ValidationError listing every problem found.
func Parse(data []byte) (App, error) {
	if len(data) > MaxConfigBytes {
		return App{}, &ValidationError{Fields: []FieldError{{
			Field:   "deploy.yaml",
			Message: fmt.Sprintf("file is too large (max %d KB)", MaxConfigBytes/1024),
		}}}
	}

	var r raw
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		verr, onlyUnknownFields := syntaxError(err)
		// A typo in a key must not hide the other mistakes. The decoder
		// fills in everything it does understand, so when unknown fields
		// are the only complaint, the rest can be validated as usual.
		// (After a type error that is not safe: the field it left empty
		// would be reported a second time, as missing.)
		if onlyUnknownFields {
			var semantic *ValidationError
			if _, err := r.validate(); errors.As(err, &semantic) {
				verr.Fields = append(verr.Fields, semantic.Fields...)
			}
		}
		return App{}, verr
	}
	if err := dec.Decode(new(yaml.Node)); !errors.Is(err, io.EOF) {
		return App{}, &ValidationError{Fields: []FieldError{{
			Field:   "deploy.yaml",
			Message: "multiple YAML documents are not supported; describe one application per file",
		}}}
	}
	return r.validate()
}

var (
	yamlLinePattern    = regexp.MustCompile(`^line (\d+): (.*)$`)
	yamlUnknownPattern = regexp.MustCompile(`^field (\S+) not found in type .*$`)
	yamlGoTypePattern  = regexp.MustCompile(` into (\S+)$`)
)

// syntaxError turns YAML decoder errors into the same report format as
// semantic validation errors, hiding Go type names from the user. It also
// reports whether unknown fields were the only problem found.
func syntaxError(err error) (verr *ValidationError, onlyUnknownFields bool) {
	verr = &ValidationError{}
	if errors.Is(err, io.EOF) {
		verr.add("deploy.yaml", "file is empty", "")
		return verr, false
	}

	var typeErr *yaml.TypeError
	if !errors.As(err, &typeErr) {
		verr.add("deploy.yaml", strings.TrimPrefix(err.Error(), "yaml: "), "")
		return verr, false
	}
	onlyUnknownFields = true
	for _, msg := range typeErr.Errors {
		field := "deploy.yaml"
		if m := yamlLinePattern.FindStringSubmatch(msg); m != nil {
			field, msg = "line "+m[1], m[2]
		}
		if m := yamlUnknownPattern.FindStringSubmatch(msg); m != nil {
			msg = fmt.Sprintf("unknown field %q", m[1])
		} else {
			onlyUnknownFields = false
			if m := yamlGoTypePattern.FindStringSubmatch(msg); m != nil {
				msg = strings.TrimSuffix(msg, m[0]) + " into " + friendlyType(m[1])
			}
		}
		verr.add(field, msg, "")
	}
	return verr, onlyUnknownFields
}

func friendlyType(goType string) string {
	switch {
	case strings.HasPrefix(goType, "int"), strings.HasPrefix(goType, "*int"):
		return "a number"
	case strings.HasPrefix(goType, "map"):
		return "a key/value map"
	case strings.HasPrefix(goType, "string"):
		return "a string"
	}
	return "the expected type"
}

var (
	// namePattern is a DNS label. Names end up in container names, Docker
	// labels, URLs and proxy config, so the alphabet is deliberately strict.
	namePattern   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	domainLabel   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// ValidateName reports whether s is a valid application name.
func ValidateName(s string) error {
	if !namePattern.MatchString(s) {
		return fmt.Errorf("invalid application name %q: use lowercase letters, digits and dashes (max 63 characters)", s)
	}
	return nil
}

func (r raw) validate() (App, error) {
	verr := &ValidationError{}
	app := App{
		Name:     r.Name,
		Image:    strings.TrimSpace(r.Image),
		Domain:   strings.ToLower(strings.TrimSpace(r.Domain)),
		Replicas: DefaultReplicas,
		Restart:  Restart{Policy: RestartAlways},
		Deploy:   Deploy{Strategy: StrategyRolling},
	}

	switch {
	case r.Name == "":
		verr.add("name", "is required", "my-api")
	case !namePattern.MatchString(r.Name):
		verr.add("name", fmt.Sprintf("invalid value %q", r.Name),
			"lowercase letters, digits and dashes, e.g. my-api (max 63 characters)")
	case slices.Contains(ReservedNames, r.Name):
		verr.add("name", fmt.Sprintf("%q is reserved for Shipwick's own services", r.Name),
			"another name, e.g. my-"+r.Name)
	}

	if app.Image == "" {
		if r.Static.Dir == "" && r.Build.Context == "" && r.Build.Dockerfile == "" {
			verr.add("image", "is required", "ghcr.io/company/my-api:1.4.2")
		}
	} else if err := ValidateImage(app.Image); err != nil {
		verr.add("image", err.Error(), "nginx:1.27, ghcr.io/company/my-api:1.4.2, ...")
	}

	if r.Port != nil {
		if *r.Port < 1 || *r.Port > 65535 {
			verr.add("port", fmt.Sprintf("invalid value %d", *r.Port), "a number between 1 and 65535")
		}
		app.Port = *r.Port
	}

	if app.Domain != "" {
		if err := ValidateHostname(app.Domain); err != nil {
			verr.add("domain", err.Error(), "api.example.com, *.example.com")
		}
		if r.Port == nil && r.Static.Dir == "" {
			verr.add("port", "is required when domain is set", "the port your application listens on, e.g. 8080")
		}
	}

	if r.Replicas != nil {
		if *r.Replicas < 1 || *r.Replicas > MaxReplicas {
			verr.add("replicas", fmt.Sprintf("invalid value %d", *r.Replicas),
				fmt.Sprintf("a number between 1 and %d", MaxReplicas))
		}
		app.Replicas = *r.Replicas
	}

	app.Env = r.validateEnv(verr)
	app.Health = r.validateHealth(verr)

	if r.Resources.CPU != "" {
		cpu, err := ParseCPU(r.Resources.CPU)
		if err != nil {
			verr.add("resources.cpu", err.Error(), "0.5, 1, 2, ...")
		}
		app.Resources.CPU = cpu
	}
	if r.Resources.Memory != "" {
		mem, err := ParseMemory(r.Resources.Memory)
		if err != nil {
			verr.add("resources.memory", err.Error(), "128mb, 512mb, 1gb, ...")
		}
		app.Resources.MemoryBytes = mem
	}

	if p := r.Restart.Policy; p != "" {
		switch p {
		case RestartAlways, RestartOnFailure, RestartNever:
			app.Restart.Policy = p
		default:
			verr.add("restart.policy", fmt.Sprintf("invalid value %q", p), "always, on-failure, never")
		}
	}

	if s := r.Deploy.Strategy; s != "" {
		switch s {
		case StrategyRolling, StrategyRecreate:
			app.Deploy.Strategy = s
		default:
			verr.add("deploy.strategy", fmt.Sprintf("invalid value %q", s), "rolling, recreate")
		}
	}
	if v := r.Deploy.StopTimeout; v != "" {
		if r.Static.Dir != "" {
			verr.add("deploy.stop_timeout", staticExclusiveMessage, "")
		} else if d, err := parseDuration(v, MinStopTimeout, MaxStopTimeout); err != nil {
			verr.add("deploy.stop_timeout", err.Error(), "10s, 30s, 5m, ... (1s to 10m)")
		} else {
			app.Deploy.StopTimeout = Duration(d)
		}
	}

	app.Aliases, app.Redirects = r.validateDomains(verr, app.Domain)
	app.Entrypoint, app.Command, app.User = r.validateProcess(verr)
	app.PreDeploy, app.Jobs = r.validateJobs(verr)
	app.Logging = r.validateLogging(verr)
	app.Build = r.validateBuild(verr)
	app.Static = r.validateStatic(verr, app)
	app.Path = r.validatePath(verr, app)
	app.Proxy = r.validateProxy(verr, app)
	app.Backups = r.validateBackups(verr, app)
	app.Volumes = r.validateVolumes(verr)
	if len(app.Volumes) > 0 {
		// Two versions writing the same files at once is how data gets lost.
		if app.Deploy.Strategy != StrategyRecreate {
			verr.add("deploy.strategy", "must be \"recreate\" for an application with volumes: two versions cannot write the same files at once",
				"deploy:\n    strategy: recreate")
		}
		if app.Replicas != 1 {
			verr.add("replicas", fmt.Sprintf("must be 1 for an application with volumes, got %d: replicas cannot share a volume", app.Replicas), "1")
		}
	}
	app.Publish = r.validatePublish(verr, app)
	app.Init = r.validateInit(verr)

	if len(verr.Fields) > 0 {
		return App{}, verr
	}
	return app, nil
}

func (r raw) validateVolumes(verr *ValidationError) []Volume {
	if len(r.Volumes) == 0 {
		return nil
	}
	if len(r.Volumes) > MaxVolumes {
		verr.add("volumes", fmt.Sprintf("too many (%d)", len(r.Volumes)), fmt.Sprintf("at most %d", MaxVolumes))
		return nil
	}
	out := make([]Volume, 0, len(r.Volumes))
	names, paths := map[string]bool{}, map[string]bool{}
	for i, v := range r.Volumes {
		field := fmt.Sprintf("volumes[%d]", i)
		switch {
		case v.Name == "":
			verr.add(field+".name", "is required", "data")
		case !namePattern.MatchString(v.Name):
			verr.add(field+".name", fmt.Sprintf("invalid value %q", v.Name), "lowercase letters, digits and dashes, e.g. data")
		case names[v.Name]:
			verr.add(field+".name", fmt.Sprintf("%q is used twice", v.Name), "a different name for each volume")
		}
		names[v.Name] = true

		p := strings.TrimSpace(v.Path)
		switch {
		case p == "":
			verr.add(field+".path", "is required", "/var/lib/postgresql/data")
		case !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\r\n") || path.Clean(p) != p || p == "/":
			verr.add(field+".path", fmt.Sprintf("invalid value %q", v.Path), "an absolute path inside the container, e.g. /var/lib/postgresql/data")
		case paths[p]:
			verr.add(field+".path", fmt.Sprintf("%q is mounted twice", p), "a different path for each volume")
		}
		paths[p] = true
		out = append(out, Volume{Name: v.Name, Path: p})
	}
	return out
}

func (r raw) validateEnv(verr *ValidationError) map[string]string {
	if len(r.Env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(r.Env))
	for k := range r.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic error order
	for _, k := range keys {
		if !envKeyPattern.MatchString(k) {
			verr.add("env."+k, "invalid variable name", "letters, digits and underscores, e.g. DATABASE_URL")
			continue
		}
		// Never echo the value: it may be a secret.
		if strings.ContainsRune(r.Env[k], 0) {
			verr.add("env."+k, "value must not contain NUL bytes", "")
		}
	}
	return r.Env
}

func (r raw) validateHealth(verr *ValidationError) *Health {
	if r.Health == nil {
		return nil
	}
	h := &Health{
		Path:     r.Health.Path,
		Interval: Duration(DefaultHealthInterval),
		Timeout:  Duration(DefaultHealthTimeout),
		Retries:  DefaultHealthRetries,
	}
	if r.Health.TCP != nil {
		h.TCP = *r.Health.TCP
	}

	// The three kinds are alternatives: a check is a GET, a connect or a
	// command, and a file that names two leaves the agent to guess which.
	var kinds []string
	if r.Health.Path != "" {
		kinds = append(kinds, "path")
	}
	if r.Health.TCP != nil {
		kinds = append(kinds, "tcp")
	}
	if r.Health.Command != nil {
		kinds = append(kinds, "command")
	}
	switch {
	case len(kinds) == 0:
		verr.add("health", "one of path, tcp or command is required", "path: /health")
	case len(kinds) > 1:
		verr.add("health", fmt.Sprintf("%s are set; a health check is one of path, tcp or command", strings.Join(kinds, " and ")),
			"path: /health for an HTTP application, tcp: 5432 for a database")
	case kinds[0] == "path":
		if err := validateHealthPath(h.Path); err != nil {
			verr.add("health.path", err.Error(), "/health, /healthz, /api/ping, ...")
		}
		if r.Port == nil {
			verr.add("port", "is required when health is set", "the port your application listens on, e.g. 8080")
		}
	case kinds[0] == "tcp":
		if h.TCP < 1 || h.TCP > 65535 {
			verr.add("health.tcp", fmt.Sprintf("invalid value %d", h.TCP), "the port your application listens on, e.g. 5432")
		}
	case kinds[0] == "command":
		h.Command = validateHealthCommand(r.Health.Command, verr)
	}

	if v := r.Health.Interval; v != "" {
		d, err := parseDuration(v, time.Second, 5*time.Minute)
		if err != nil {
			verr.add("health.interval", err.Error(), "5s, 10s, 1m, ... (1s to 5m)")
		}
		h.Interval = Duration(d)
	}
	if v := r.Health.Timeout; v != "" {
		d, err := parseDuration(v, 100*time.Millisecond, time.Minute)
		if err != nil {
			verr.add("health.timeout", err.Error(), "500ms, 3s, 10s, ... (100ms to 1m)")
		}
		h.Timeout = Duration(d)
	}
	if r.Health.Retries != nil {
		if *r.Health.Retries < 1 || *r.Health.Retries > 100 {
			verr.add("health.retries", fmt.Sprintf("invalid value %d", *r.Health.Retries), "a number between 1 and 100")
		}
		h.Retries = *r.Health.Retries
	}
	if v := r.Health.StartPeriod; v != "" {
		d, err := parseDuration(v, 0, 30*time.Minute)
		if err != nil {
			verr.add("health.start_period", err.Error(), "30s, 1m, 5m, ... (up to 30m)")
		}
		h.StartPeriod = Duration(d)
	}
	return h
}

func parseDuration(s string, min, max time.Duration) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", s)
	}
	if d < min || d > max {
		return 0, fmt.Errorf("invalid value %q: out of range", s)
	}
	return d, nil
}

// ValidateImage reports whether image is a well-formed image reference.
func ValidateImage(image string) error {
	if strings.ContainsAny(image, " \t\r\n") {
		return fmt.Errorf("invalid value %q: must not contain whitespace", image)
	}
	if _, err := reference.ParseNormalizedNamed(image); err != nil {
		return fmt.Errorf("invalid value %q: not a valid image reference", image)
	}
	return nil
}

// ValidateDomain reports whether domain is a plain hostname. Domains end up
// in the reverse proxy's configuration, so nothing else is accepted: no
// scheme, port, path or wildcard.
func ValidateDomain(domain string) error {
	if len(domain) > 253 {
		return errors.New("invalid value: longer than 253 characters")
	}
	for _, label := range strings.Split(domain, ".") {
		if !domainLabel.MatchString(label) {
			return fmt.Errorf("invalid value %q: not a valid hostname", domain)
		}
	}
	return nil
}

func validateHealthPath(p string) error {
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("invalid value %q: must start with /", p)
	}
	if len(p) > 2048 {
		return errors.New("invalid value: longer than 2048 characters")
	}
	for _, c := range p {
		if c <= ' ' || c == 0x7f {
			return fmt.Errorf("invalid value %q: must not contain whitespace or control characters", p)
		}
	}
	u, err := url.ParseRequestURI(p)
	if err != nil || u.Host != "" || u.Scheme != "" {
		return fmt.Errorf("invalid value %q: not a valid URL path", p)
	}
	return nil
}

// MaxHealthCommandArgs bounds the argv of a command health check.
const MaxHealthCommandArgs = 64

// validateHealthCommand checks the argv of a command health check. It is
// handed to the container runtime as it is, never to a shell, so the only
// things to refuse are what an argv cannot carry.
func validateHealthCommand(cmd []string, verr *ValidationError) []string {
	const example = `["pg_isready", "-U", "postgres"]`
	if len(cmd) == 0 {
		verr.add("health.command", "must not be empty", example)
		return nil
	}
	if len(cmd) > MaxHealthCommandArgs {
		verr.add("health.command", fmt.Sprintf("too many arguments (%d)", len(cmd)), fmt.Sprintf("at most %d", MaxHealthCommandArgs))
		return nil
	}
	for i, arg := range cmd {
		switch {
		case arg == "":
			verr.add(fmt.Sprintf("health.command[%d]", i), "must not be empty", example)
		case strings.ContainsRune(arg, 0):
			verr.add(fmt.Sprintf("health.command[%d]", i), "must not contain NUL bytes", example)
		}
	}
	return cmd
}

// Version derives the human-facing deployment version from the image
// reference: its tag, a shortened digest, or "latest".
func (a App) Version() string {
	named, err := reference.ParseNormalizedNamed(a.Image)
	if err != nil {
		return "unknown"
	}
	if tagged, ok := named.(reference.Tagged); ok {
		return tagged.Tag()
	}
	if digested, ok := named.(reference.Digested); ok {
		d := digested.Digest().String()
		if len(d) > 19 {
			d = d[:19] // "sha256:" + 12 hex chars
		}
		return d
	}
	return "latest"
}
