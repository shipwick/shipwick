package spec

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// everyField is a set of applications that between them use every field an
// App has: one that runs containers with everything a container can have,
// one for each other kind of health check, and a static one.
func everyField() []App {
	full := App{
		Name:      "shop",
		Image:     LocalImagePrefix("shop") + "20260301-ab12",
		Port:      8080,
		Domain:    "shop.example.com",
		Aliases:   []string{"www.shop.example.com", "*.shop.example.com"},
		Redirects: []string{"shop.example.org"},
		Replicas:  1,
		Env:       map[string]string{"DATABASE_URL": "postgres://db/shop", "EMPTY": "", "MULTILINE": "a\nb: c\n"},
		Health: &Health{Path: "/health?deep=1", Interval: Duration(5 * time.Second), Timeout: Duration(500 * time.Millisecond),
			Retries: 4, StartPeriod: Duration(90 * time.Second)},
		Resources:  Resources{CPU: 0.75, MemoryBytes: 384 << 20},
		Volumes:    []Volume{{Name: "data", Path: "/var/lib/shop"}, {Name: "uploads", Path: "/srv/uploads"}},
		Publish:    []Publish{{Port: 5432, Host: 15432, Address: "10.0.0.5", Protocol: ProtocolTCP}, {Port: 9000, Host: 9000, Protocol: ProtocolUDP}},
		Entrypoint: []string{"/usr/bin/tini", "--"},
		Command:    []string{"shop", "--listen", "0.0.0.0:8080", "an argument with spaces"},
		User:       "1000:1000",
		PreDeploy:  &Hook{Command: []string{"shop", "migrate"}, Timeout: Duration(20 * time.Minute)},
		Jobs: []Job{
			{Name: "nightly-report", Schedule: "0 3 * * *", Command: []string{"shop", "report"}, Timeout: Duration(2 * time.Hour)},
			{Name: "sweep", Schedule: "*/15 * * * *", Command: []string{"shop", "sweep"}, Timeout: Duration(DefaultJobTimeout)},
		},
		Logging: &Logging{Driver: "gelf", Options: map[string]string{"gelf-address": "udp://logs.example.com:12201", "tag": "shop"}},
		Build:   &Build{Context: "services/shop", Dockerfile: "docker/Dockerfile.prod"},
		Path:    "/store",
		Proxy: &Proxy{
			StripPrefix: true,
			Headers:     map[string]string{"X-Frame-Options": "DENY", "Cache-Control": "no-store"},
			BasicAuth: []BasicAuth{
				{Username: "everyone", Password: "a password: with #yaml in it"},
				{Path: "/store/admin", Username: "admin", Password: "correct horse battery"},
			},
			Redirects: []PathRedirect{
				{From: "/store/old", To: "/store/new?from=old", Status: 301},
				{From: "/store/docs", To: "https://docs.example.org/shop", Status: DefaultRedirectStatus},
			},
		},
		Backups: &Backups{Schedule: "30 2 * * *", Keep: 14, Before: []string{"shop", "checkpoint"}, BeforeTimeout: Duration(2 * time.Hour), Stop: true, BeforeIn: BeforeInContainer},
		Init:    true,
		Restart: Restart{Policy: RestartOnFailure},
		Deploy:  Deploy{Strategy: StrategyRecreate, StopTimeout: Duration(45 * time.Second)},
		Security: &Security{
			ReadOnly:     true,
			Tmpfs:        []Tmpfs{{Path: "/tmp", SizeBytes: DefaultTmpfsBytes}, {Path: "/var/cache/shop", SizeBytes: 200 << 20}},
			Capabilities: &[]string{"CHOWN", "SETGID", "SETUID"},
			NonRoot:      true,
		},
	}
	tcp := App{
		Name: "db", Image: "postgres:17", Replicas: 2,
		Health:  &Health{TCP: 5432, Interval: Duration(DefaultHealthInterval), Timeout: Duration(DefaultHealthTimeout), Retries: DefaultHealthRetries},
		Restart: Restart{Policy: RestartAlways}, Deploy: Deploy{Strategy: StrategyRolling},
	}
	command := tcp
	command.Name = "queue"
	command.Health = &Health{Command: []string{"pg_isready", "-U", "postgres"}, Interval: Duration(time.Minute), Timeout: Duration(10 * time.Second), Retries: 1}
	static := App{
		Name: "docs", Domain: "docs.example.com", Replicas: DefaultReplicas,
		Static:  &Static{Dir: "dist", Fallback: "app/index.html"},
		Restart: Restart{Policy: RestartAlways}, Deploy: Deploy{Strategy: StrategyRolling},
	}
	return []App{full, tcp, command, static}
}

// travelled is the App as an export carries it: through its JSON form.
func travelled(t *testing.T, a App) App {
	t.Helper()
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var out App
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestValidateAcceptsWhatParseProducesAndReturnsItUnchanged(t *testing.T) {
	for _, a := range everyField() {
		got, err := Validate(travelled(t, a))
		if err != nil {
			t.Errorf("%s: %v", a.Name, err)
			continue
		}
		if !reflect.DeepEqual(got, a) {
			want, _ := json.MarshalIndent(a, "", "  ")
			have, _ := json.MarshalIndent(got, "", "  ")
			t.Errorf("%s came back changed:\nwant %s\ngot  %s", a.Name, want, have)
		}
	}
}

// A field added to App and not to documentOf would be dropped by Validate
// without a word. This fails for it until everyField uses it, and the test
// above then fails until documentOf writes it.
func TestValidateKeepsEveryField(t *testing.T) {
	used := map[string]bool{}
	for _, a := range everyField() {
		markUsed(reflect.ValueOf(a), "", used)
	}
	for _, field := range fieldsOf(reflect.TypeOf(App{}), "") {
		if !used[field] {
			t.Errorf("no application of everyField sets %s: add it there, and to documentOf", field)
		}
	}
}

func markUsed(v reflect.Value, prefix string, used map[string]bool) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			markUsed(v.Elem(), prefix, used)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			markUsed(v.Index(i), prefix, used)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			name := prefix + v.Type().Field(i).Name
			if !v.Field(i).IsZero() {
				used[name] = true
			}
			markUsed(v.Field(i), name+".", used)
		}
	}
}

func fieldsOf(t reflect.Type, prefix string) []string {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var out []string
	for i := 0; i < t.NumField(); i++ {
		name := prefix + t.Field(i).Name
		out = append(out, name)
		out = append(out, fieldsOf(t.Field(i).Type, name+".")...)
	}
	return out
}

func TestValidateHoldsAValueToTheRulesOfADocument(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(a *App)
		field  string
	}{
		"a health path that is no path":      {func(a *App) { a.Health.Path = "health\n" }, "health.path"},
		"a health check of two kinds":        {func(a *App) { a.Health.TCP = 5432 }, "health"},
		"a health check without an interval": {func(a *App) { a.Health.Interval = 0 }, "health.interval"},
		"a header that is the connection's":  {func(a *App) { a.Proxy.Headers["Transfer-Encoding"] = "chunked" }, "proxy.headers.Transfer-Encoding"},
		"an account outside the path":        {func(a *App) { a.Proxy.BasicAuth[1].Path = "/admin" }, "proxy.basic_auth[1].path"},
		"a password that is too short":       {func(a *App) { a.Proxy.BasicAuth[0].Password = "short" }, "proxy.basic_auth[0].password"},
		"a redirect with braces":             {func(a *App) { a.Proxy.Redirects[0].To = "/{http.request.uri}" }, "proxy.redirects[0].to"},
		"a redirect status":                  {func(a *App) { a.Proxy.Redirects[0].Status = 200 }, "proxy.redirects[0].status"},
		"the proxy's port":                   {func(a *App) { a.Publish[0].Host = 443 }, "publish[0].host"},
		"a published address":                {func(a *App) { a.Publish[0].Address = "0.0.0.0; rm" }, "publish[0].address"},
		"a log driver nobody checks":         {func(a *App) { a.Logging.Driver = "etwlogs" }, "logging.driver"},
		"a log option that names a file":     {func(a *App) { a.Logging.Options["syslog-tls-key"] = "/etc/shadow" }, "logging.options.syslog-tls-key"},
		"a log address that is a socket":     {func(a *App) { a.Logging.Options["gelf-address"] = "unix:///var/run/docker.sock" }, "logging.options.gelf-address"},
		"a user":                             {func(a *App) { a.User = "root; id" }, "user"},
		"a reserved name":                    {func(a *App) { a.Name = "caddy" }, "name"},
		"a volume path that climbs":          {func(a *App) { a.Volumes[0].Path = "/var/../etc" }, "volumes[0].path"},
		"two replicas next to volumes":       {func(a *App) { a.Replicas = 2 }, "replicas"},
		"a strategy it does not know":        {func(a *App) { a.Deploy.Strategy = "blue-green" }, "deploy.strategy"},
		"a backup schedule":                  {func(a *App) { a.Backups.Schedule = "every night" }, "backups.schedule"},
		"backups kept":                       {func(a *App) { a.Backups.Keep = 0 }, "backups.keep"},
		"a job's command":                    {func(a *App) { a.Jobs[0].Command = nil }, "jobs[0].command"},
		"a hook that never ends":             {func(a *App) { a.PreDeploy.Timeout = Duration(48 * time.Hour) }, "pre_deploy.timeout"},
		"an image from a registry and build": {func(a *App) { a.Image = "nginx:1.27" }, "image"},
		"a build context outside":            {func(a *App) { a.Build.Context = "../.." }, "build.context"},
		"an environment variable's name":     {func(a *App) { a.Env["A B"] = "x" }, "env.A B"},
		"a memory limit below Docker's":      {func(a *App) { a.Resources.MemoryBytes = 1024 }, "resources.memory"},
		"a capability nobody has":            {func(a *App) { *a.Security.Capabilities = append(*a.Security.Capabilities, "SYS_ADMIN") }, "security.capabilities[3]"},
		"a tmpfs the size of the server":     {func(a *App) { a.Security.Tmpfs[0].SizeBytes = 1 << 40 }, "security.tmpfs[0].size"},
		"a tmpfs path that climbs":           {func(a *App) { a.Security.Tmpfs[1].Path = "/var/../etc" }, "security.tmpfs[1].path"},
		"root where root is refused":         {func(a *App) { a.User = "0" }, "user"},
		"a path with a query":                {func(a *App) { a.Path = "/store?x=1" }, "path"},
		"a domain with a scheme":             {func(a *App) { a.Domain = "https://shop.example.com" }, "domain"},
	} {
		a := travelled(t, everyField()[0])
		tc.change(&a)
		_, err := Validate(a)
		var verr *ValidationError
		if !errors.As(err, &verr) {
			t.Errorf("%s was accepted (%v)", name, err)
			continue
		}
		named := false
		for _, f := range verr.Fields {
			named = named || f.Field == tc.field
		}
		if !named {
			t.Errorf("%s: the error names %+v, want %s", name, verr.Fields, tc.field)
		}
	}
}

func TestValidateRefusesContainerFieldsOfAStaticApplication(t *testing.T) {
	a := everyField()[3]
	a.Port, a.Replicas = 8080, 3
	_, err := Validate(a)
	if err == nil || !strings.Contains(err.Error(), "port:") || !strings.Contains(err.Error(), "replicas:") {
		t.Errorf("a static application with a port and three replicas: %v", err)
	}
}

func TestValidateReturnsTheValueAsParseWouldWriteIt(t *testing.T) {
	a := everyField()[1]
	a.Image, a.Domain, a.Port = " postgres:17 ", "DB.Example.com", 5432
	got, err := Validate(a)
	if err != nil {
		t.Fatal(err)
	}
	if got.Image != "postgres:17" || got.Domain != "db.example.com" {
		t.Errorf("image %q, domain %q: not as a document's would be", got.Image, got.Domain)
	}
}
