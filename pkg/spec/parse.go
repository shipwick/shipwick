package spec

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
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

// raw mirrors deploy.yaml before validation. Values with their own syntax
// (sizes, durations, cpu) are decoded as strings so that a bad value yields a
// field-level error instead of a generic YAML type error.
type raw struct {
	Name     string            `yaml:"name"`
	Image    string            `yaml:"image"`
	Port     *int              `yaml:"port"`
	Domain   string            `yaml:"domain"`
	Replicas *int              `yaml:"replicas"`
	Env      map[string]string `yaml:"env"`
	Health   *struct {
		Path     string `yaml:"path"`
		Interval string `yaml:"interval"`
		Timeout  string `yaml:"timeout"`
		Retries  *int   `yaml:"retries"`
	} `yaml:"health"`
	Resources struct {
		CPU    string `yaml:"cpu"`
		Memory string `yaml:"memory"`
	} `yaml:"resources"`
	Restart struct {
		Policy string `yaml:"policy"`
	} `yaml:"restart"`
	Deploy struct {
		Strategy string `yaml:"strategy"`
	} `yaml:"deploy"`
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
	}

	if app.Image == "" {
		verr.add("image", "is required", "ghcr.io/company/my-api:1.4.2")
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
		if err := ValidateDomain(app.Domain); err != nil {
			verr.add("domain", err.Error(), "api.example.com")
		}
		if r.Port == nil {
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
		case StrategyRolling:
			app.Deploy.Strategy = s
		default:
			verr.add("deploy.strategy", fmt.Sprintf("invalid value %q", s), "rolling")
		}
	}

	if len(verr.Fields) > 0 {
		return App{}, verr
	}
	return app, nil
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

	if h.Path == "" {
		verr.add("health.path", "is required when health is set", "/health")
	} else if err := validateHealthPath(h.Path); err != nil {
		verr.add("health.path", err.Error(), "/health, /healthz, /api/ping, ...")
	}
	if r.Port == nil {
		verr.add("port", "is required when health is set", "the port your application listens on, e.g. 8080")
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
