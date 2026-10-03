package spec

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Proxy is what the proxy does for an application beyond routing to it.
//
// Every path in it is a path as the visitor asks for it, on the application's
// domain: with `path: /api`, the admin area is /api/admin whether or not the
// prefix is stripped on the way to the application.
type Proxy struct {
	// StripPrefix removes the application's Path from a request before the
	// application sees it.
	StripPrefix bool `json:"strip_prefix,omitempty"`
	// Headers are set on every response, replacing what the application sent
	// under the same name.
	Headers   map[string]string `json:"headers,omitempty"`
	BasicAuth []BasicAuth       `json:"basic_auth,omitempty"`
	Redirects []PathRedirect    `json:"redirects,omitempty"`
}

// BasicAuth is one account, asked for at Path and everything under it. Where
// the paths of two entries overlap, the longer path decides who may pass.
type BasicAuth struct {
	Path     string `json:"path,omitempty"` // empty: the whole application
	Username string `json:"username"`
	// Password is a secret like an env value: sealed at rest, masked in
	// responses, hashed before the proxy is told.
	Password string `json:"password"`
}

// PathRedirect answers a request for exactly From with a redirect to To: a
// path on the same host, or an https:// URL.
type PathRedirect struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Status int    `json:"status"`
}

// Bounds for `path` and the proxy block.
const (
	MaxPathBytes        = 200
	MaxProxyHeaders     = 50
	MaxHeaderValueBytes = 4096
	MaxBasicAuth        = 20
	MaxPathRedirects    = 100
	MaxRedirectToBytes  = 2048
	MaxUsernameBytes    = 255
	MinPasswordChars    = 8
	// MaxPasswordBytes is where bcrypt stops reading; a longer password
	// would be one whose tail does not count.
	MaxPasswordBytes = 72

	DefaultRedirectStatus = 308
)

const redactedValue = "********"

var (
	pathPattern = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)+$`)
	// headerNamePattern is a token as RFC 7230 defines it.
	headerNamePattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

// connectionHeaders describe one connection or the framing of one message.
// The proxy writes them itself; a fixed value would contradict what it does.
var connectionHeaders = []string{
	"connection", "content-encoding", "content-length", "keep-alive", "proxy-authenticate",
	"proxy-authorization", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade",
}

// validateURLPath checks a path of the kind `path`, `basic_auth.path` and
// `redirects.from` are. The alphabet is strict because the path becomes a
// matcher in the proxy's configuration.
func validateURLPath(p string) error {
	switch {
	case !strings.HasPrefix(p, "/"):
		return fmt.Errorf("invalid value %q: must start with /", p)
	case len(p) > MaxPathBytes:
		return fmt.Errorf("invalid value: longer than %d characters", MaxPathBytes)
	case strings.HasSuffix(p, "/"):
		return fmt.Errorf("invalid value %q: must not end with /", p)
	case !pathPattern.MatchString(p):
		return fmt.Errorf("invalid value %q: use letters, digits, dots, dashes, underscores and tildes between the slashes", p)
	}
	for _, segment := range strings.Split(p[1:], "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("invalid value %q: must not contain . or .. segments", p)
		}
	}
	return nil
}

// PathKey is the form in which two paths are compared: the proxy matches
// paths without regard to case, so /API and /api are one path to it.
func PathKey(p string) string { return strings.ToLower(p) }

// PathWithin reports whether p is prefix or lies under it, the way the proxy
// matches: /api and /api/users are within /api, /apix is not. Everything is
// within the empty prefix.
func PathWithin(p, prefix string) bool {
	p, prefix = PathKey(p), PathKey(prefix)
	return prefix == "" || p == prefix || strings.HasPrefix(p, prefix+"/")
}

// validatePath checks `path`, the prefix of its domain an application serves.
func (r raw) validatePath(verr *ValidationError, app App) string {
	p := strings.TrimSpace(r.Path)
	if p == "" {
		return ""
	}
	if app.Domain == "" {
		verr.add("path", "requires domain: a path is a part of it", "domain: example.com")
	}
	// The whole domain, which is what an application without a path serves.
	if p == "/" {
		return ""
	}
	if err := validateURLPath(p); err != nil {
		verr.add("path", err.Error(), "/api, /docs/v2, ...")
	}
	return p
}

// proxyRaw mirrors the proxy block before validation.
type proxyRaw struct {
	StripPrefix *bool             `yaml:"strip_prefix"`
	Headers     map[string]string `yaml:"headers"`
	BasicAuth   []struct {
		Path     string `yaml:"path"`
		Username string `yaml:"username"`
		Password string `yaml:"password"`
	} `yaml:"basic_auth"`
	Redirects []struct {
		From   string `yaml:"from"`
		To     string `yaml:"to"`
		Status *int   `yaml:"status"`
	} `yaml:"redirects"`
}

// validateProxy checks the `proxy` block.
func (r raw) validateProxy(verr *ValidationError, app App) *Proxy {
	node := r.Proxy
	if node.Kind == 0 || node.Tag == "!!null" {
		return nil
	}
	var pr proxyRaw
	if !decodeStrict(verr, &node, &pr) {
		return nil
	}
	if app.Domain == "" {
		verr.add("proxy", "requires domain: it says what the proxy does with the requests for it", "domain: example.com")
	}

	p := &Proxy{}
	if pr.StripPrefix != nil && *pr.StripPrefix {
		p.StripPrefix = true
		if app.Path == "" {
			verr.add("proxy.strip_prefix", "requires path: it is the prefix that is removed", "path: /api")
		}
	}
	p.Headers = validateProxyHeaders(verr, pr.Headers)
	p.BasicAuth = validateBasicAuth(verr, pr, app.Path)
	p.Redirects = validatePathRedirects(verr, pr, app.Path)

	if !p.StripPrefix && len(p.Headers) == 0 && len(p.BasicAuth) == 0 && len(p.Redirects) == 0 {
		return nil
	}
	return p
}

func validateBasicAuth(verr *ValidationError, pr proxyRaw, appPath string) []BasicAuth {
	if n := len(pr.BasicAuth); n > MaxBasicAuth {
		verr.add("proxy.basic_auth", fmt.Sprintf("too many (%d)", n), fmt.Sprintf("at most %d", MaxBasicAuth))
		return nil
	}
	var out []BasicAuth
	seen := map[string]bool{}
	for i, a := range pr.BasicAuth {
		field := fmt.Sprintf("proxy.basic_auth[%d]", i)
		entry := BasicAuth{Path: validateProxyPath(verr, field+".path", a.Path, appPath, "/admin"), Username: a.Username, Password: a.Password}
		account := PathKey(entry.Path) + "\x00" + a.Username
		switch {
		case a.Username == "":
			verr.add(field+".username", "is required", "admin")
		case len(a.Username) > MaxUsernameBytes:
			verr.add(field+".username", fmt.Sprintf("is too long (%d characters)", len(a.Username)), fmt.Sprintf("at most %d", MaxUsernameBytes))
		case strings.Contains(a.Username, ":") || hasControl(a.Username):
			verr.add(field+".username", fmt.Sprintf("invalid value %q: must not contain a colon or control characters", a.Username), "admin")
		case seen[account]:
			verr.add(field+".username", fmt.Sprintf("%q is listed twice for the same path", a.Username), "one entry for each user of a path")
		}
		seen[account] = true
		// A placeholder left for the server is judged there, once the value
		// is known.
		if !hasPlaceholder(a.Password) {
			if err := ValidatePassword(a.Password); err != nil {
				verr.add(field+".password", err.Error(), "${ADMIN_PASSWORD}, with the value in the environment, in --env-file or stored with shipwick secret set")
			}
		}
		out = append(out, entry)
	}
	return out
}

// ValidatePassword checks a basic-auth password. The message never repeats
// the value.
func ValidatePassword(password string) error {
	switch {
	case password == "":
		return errors.New("is required")
	case len([]rune(password)) < MinPasswordChars:
		return fmt.Errorf("is too short: at least %d characters", MinPasswordChars)
	case len(password) > MaxPasswordBytes:
		return fmt.Errorf("is too long: at most %d bytes", MaxPasswordBytes)
	case hasControl(password):
		return errors.New("must not contain control characters")
	}
	return nil
}

func hasPlaceholder(s string) bool {
	for _, m := range Placeholder.FindAllStringSubmatch(s, -1) {
		if m[1] == "" {
			return true
		}
	}
	return false
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(c rune) bool { return c < ' ' || c == 0x7f })
}

// validateProxyPath checks a path inside the proxy block: a path like `path`,
// and one the application serves. "/" and nothing both mean all of it.
func validateProxyPath(verr *ValidationError, field, p, appPath, example string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return ""
	}
	if err := validateURLPath(p); err != nil {
		verr.add(field, err.Error(), appPath+example)
	} else if !PathWithin(p, appPath) {
		verr.add(field, fmt.Sprintf("%q is outside path %s, which is all this application serves", p, appPath), appPath+example)
	}
	return p
}

func validateProxyHeaders(verr *ValidationError, in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	if n := len(in); n > MaxProxyHeaders {
		verr.add("proxy.headers", fmt.Sprintf("too many (%d)", n), fmt.Sprintf("at most %d", MaxProxyHeaders))
		return nil
	}
	names := make([]string, 0, len(in))
	for name := range in {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic error order
	seen := map[string]string{}
	for _, name := range names {
		field, value, lower := "proxy.headers."+name, in[name], strings.ToLower(name)
		switch {
		case !headerNamePattern.MatchString(name):
			verr.add("proxy.headers", fmt.Sprintf("invalid header name %q", name), "letters, digits and dashes, e.g. X-Frame-Options")
		case slices.Contains(connectionHeaders, lower):
			verr.add(field, "belongs to the connection, and the proxy writes it itself", "a header about the content, e.g. X-Frame-Options, Cache-Control")
		case seen[lower] != "":
			verr.add(field, fmt.Sprintf("is the same header as %s: names are compared without regard to case", seen[lower]), "each header once")
		case value == "":
			verr.add(field, "value must not be empty", "")
		case len(value) > MaxHeaderValueBytes:
			verr.add(field, fmt.Sprintf("value is too long (%d characters)", len(value)), fmt.Sprintf("at most %d", MaxHeaderValueBytes))
		case hasControl(value):
			verr.add(field, "value must not contain control characters or newlines", "")
		}
		seen[lower] = name
	}
	return in
}

func validatePathRedirects(verr *ValidationError, pr proxyRaw, appPath string) []PathRedirect {
	if n := len(pr.Redirects); n > MaxPathRedirects {
		verr.add("proxy.redirects", fmt.Sprintf("too many (%d)", n), fmt.Sprintf("at most %d", MaxPathRedirects))
		return nil
	}
	var out []PathRedirect
	// Where each path leads, for finding the redirects that lead back.
	next := map[string]string{}
	valid := true
	for i, rr := range pr.Redirects {
		field := fmt.Sprintf("proxy.redirects[%d]", i)
		red := PathRedirect{To: strings.TrimSpace(rr.To), Status: DefaultRedirectStatus}
		before := len(verr.Fields)

		if strings.TrimSpace(rr.From) == "" {
			verr.add(field+".from", "is required", appPath+"/old")
		} else if red.From = validateProxyPath(verr, field+".from", rr.From, appPath, "/old"); red.From == "" {
			verr.add(field+".from", "must be a path below /: redirecting everything would leave nothing to serve", appPath+"/old")
		} else if _, twice := next[PathKey(red.From)]; twice {
			verr.add(field+".from", fmt.Sprintf("%q is redirected twice", red.From), "one redirect for each path")
		}

		if red.To == "" {
			verr.add(field+".to", "is required", "/new, https://example.org/new")
		} else if err := validateRedirectTarget(red.To); err != nil {
			verr.add(field+".to", err.Error(), "/new, https://example.org/new")
		}

		if rr.Status != nil {
			red.Status = *rr.Status
			if !slices.Contains([]int{301, 302, 307, 308}, red.Status) {
				verr.add(field+".status", fmt.Sprintf("invalid value %d", red.Status), "301, 302, 307, 308")
			}
		}
		if len(verr.Fields) == before {
			next[PathKey(red.From)] = PathKey(targetPath(red.To))
		} else {
			valid = false
		}
		out = append(out, red)
	}
	if !valid {
		return out
	}
	for i, red := range out {
		at := PathKey(red.From)
		for steps := 0; steps <= len(out); steps++ {
			to, redirected := next[at]
			if !redirected {
				break
			}
			if to == PathKey(red.From) {
				field := fmt.Sprintf("proxy.redirects[%d].to", i)
				if steps == 0 {
					verr.add(field, fmt.Sprintf("redirects %s to itself", red.From), "another path, or an https:// URL")
				} else {
					verr.add(field, fmt.Sprintf("leads back to %s through the other redirects: a browser would follow them forever", red.From), "a path that is not redirected")
				}
				break
			}
			at = to
		}
	}
	return out
}

// validateRedirectTarget accepts a path on the same host, with or without a
// query, or an absolute https:// URL. Braces are refused in either: a URL
// carries them percent-encoded, and the proxy would read them as its own
// placeholders.
func validateRedirectTarget(to string) error {
	if len(to) > MaxRedirectToBytes {
		return fmt.Errorf("invalid value: longer than %d characters", MaxRedirectToBytes)
	}
	for _, c := range to {
		if c <= ' ' || c == 0x7f || c == '{' || c == '}' || c == '\\' {
			return fmt.Errorf("invalid value %q: must not contain whitespace, control characters, braces or backslashes", to)
		}
	}
	if strings.HasPrefix(to, "/") {
		// "//host/path" is another host to a browser.
		if strings.HasPrefix(to, "//") {
			return fmt.Errorf("invalid value %q: a path starts with one slash; for another host use https://", to)
		}
		if u, err := url.ParseRequestURI(to); err != nil || u.Host != "" || u.Scheme != "" {
			return fmt.Errorf("invalid value %q: not a valid URL path", to)
		}
		return nil
	}
	u, err := url.Parse(to)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return fmt.Errorf("invalid value %q: must be a path starting with / or an https:// URL", to)
	}
	return nil
}

// targetPath is the path a redirect to the same host leads to, without its
// query; empty for a redirect to another host.
func targetPath(to string) string {
	if !strings.HasPrefix(to, "/") {
		return ""
	}
	u, err := url.ParseRequestURI(to)
	if err != nil {
		return ""
	}
	return u.Path
}

// redacted returns a copy with every password masked.
func (p *Proxy) redacted() *Proxy {
	if p == nil || len(p.BasicAuth) == 0 {
		return p
	}
	out := *p
	out.BasicAuth = slices.Clone(p.BasicAuth)
	for i := range out.BasicAuth {
		out.BasicAuth[i].Password = redactedValue
	}
	return &out
}

// decodeStrict decodes a block that the document's decoder left as a node,
// as strictly as that decoder treats the rest: an unknown key is an error,
// and every problem is reported under its line. It reports whether out is
// usable.
func decodeStrict(verr *ValidationError, node *yaml.Node, out any) bool {
	problems := unknownKeys(node, reflect.TypeOf(out))
	usable := true
	if err := node.Decode(out); err != nil {
		usable = false
		var typeErr *yaml.TypeError
		if errors.As(err, &typeErr) {
			problems = append(problems, typeErr.Errors...)
		} else {
			problems = append(problems, strings.TrimPrefix(err.Error(), "yaml: "))
		}
	}
	if len(problems) > 0 {
		sub, _ := syntaxError(&yaml.TypeError{Errors: problems})
		verr.Fields = append(verr.Fields, sub.Fields...)
	}
	return usable
}

// unknownKeys lists, in the decoder's own wording, the keys of node that the
// type has no field for.
func unknownKeys(node *yaml.Node, t reflect.Type) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var out []string
	switch {
	case node.Kind == yaml.SequenceNode && t.Kind() == reflect.Slice:
		for _, item := range node.Content {
			out = append(out, unknownKeys(item, t.Elem())...)
		}
	case node.Kind == yaml.MappingNode && t.Kind() == reflect.Struct:
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
			fields[name] = t.Field(i).Type
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			ft, known := fields[key.Value]
			if !known {
				out = append(out, fmt.Sprintf("line %d: field %s not found in type spec.raw", key.Line, key.Value))
				continue
			}
			out = append(out, unknownKeys(value, ft)...)
		}
	}
	return out
}
