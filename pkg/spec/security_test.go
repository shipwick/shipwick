package spec

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const securityBase = "name: api\nimage: api:1\n"

func TestSecurityIsAbsentUnlessAskedFor(t *testing.T) {
	for name, block := range map[string]string{
		"no block":              "",
		"an empty block":        "security: {}\n",
		"a block of two falses": "security:\n  read_only: false\n  non_root: false\n",
	} {
		app, err := Parse([]byte(securityBase + block))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if app.Security != nil {
			t.Errorf("%s: security = %+v, want none", name, app.Security)
		}
		if b, _ := json.Marshal(app); strings.Contains(string(b), `"security"`) {
			t.Errorf("%s: an unset security appears in the stored spec: %s", name, b)
		}
	}
}

func TestSecurityIsParsedAndStored(t *testing.T) {
	app, err := Parse([]byte(securityBase + `user: "1000:1000"
security:
  read_only: true
  tmpfs:
    - /tmp
    - path: /var/cache/api
      size: 200mb
  capabilities: [setuid, CAP_CHOWN, SETGID]
  non_root: true
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := &Security{
		ReadOnly:     true,
		Tmpfs:        []Tmpfs{{Path: "/tmp", SizeBytes: 64 << 20}, {Path: "/var/cache/api", SizeBytes: 200 << 20}},
		Capabilities: &[]string{"CHOWN", "SETGID", "SETUID"},
		NonRoot:      true,
	}
	if !reflect.DeepEqual(app.Security, want) {
		t.Fatalf("security = %+v, want %+v", app.Security, want)
	}
	b, err := json.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	var back App
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Security, want) {
		t.Errorf("security was changed by the stored spec: %s", b)
	}
	if got := app.Redacted().Security; !reflect.DeepEqual(got, want) {
		t.Errorf("the redacted spec says %+v: nothing of security is a secret", got)
	}
}

// The two cannot be told apart by a list alone: one keeps Docker's default
// set, the other keeps nothing.
func TestCapabilitiesNoneIsNotCapabilitiesAbsent(t *testing.T) {
	none, err := Parse([]byte(securityBase + "security:\n  capabilities: none\n"))
	if err != nil {
		t.Fatalf("capabilities: none: %v", err)
	}
	if none.Security == nil || none.Security.Capabilities == nil || len(*none.Security.Capabilities) != 0 {
		t.Fatalf("capabilities: none: %+v, want a list that keeps nothing", none.Security)
	}
	b, _ := json.Marshal(none)
	if !strings.Contains(string(b), `"capabilities":[]`) {
		t.Errorf("the stored spec does not say that nothing is kept: %s", b)
	}
	var back App
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Security == nil || back.Security.Capabilities == nil {
		t.Errorf("capabilities: none came back from the stored spec as Docker's default set: %s", b)
	}
	if again, err := Validate(back); err != nil || again.Security == nil || again.Security.Capabilities == nil || len(*again.Security.Capabilities) != 0 {
		t.Errorf("Validate made %+v of it (%v)", again.Security, err)
	}

	absent, err := Parse([]byte(securityBase + "security:\n  read_only: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if absent.Security.Capabilities != nil {
		t.Errorf("no capabilities key: %v, want Docker's default set left alone", *absent.Security.Capabilities)
	}
}

func TestSecurityRefusesWhatItCannotHold(t *testing.T) {
	for name, tc := range map[string]struct {
		doc   string
		field string
		says  string
	}{
		"a capability outside Docker's default set": {"security:\n  capabilities: [SYS_ADMIN]\n", "security.capabilities[0]", "nothing is added"},
		"every capability":                          {"security:\n  capabilities: ALL\n", "security.capabilities", "invalid value"},
		"an empty list of capabilities":             {"security:\n  capabilities: []\n", "security.capabilities", "write none"},
		"a capability twice":                        {"security:\n  capabilities: [CHOWN, cap_chown]\n", "security.capabilities[1]", "twice"},
		"capabilities as a block":                   {"security:\n  capabilities:\n    keep: CHOWN\n", "security", "none or a list"},
		"a relative tmpfs path":                     {"security:\n  tmpfs: [tmp]\n", "security.tmpfs[0].path", "invalid value"},
		"a tmpfs path that climbs":                  {"security:\n  tmpfs: [/tmp/../etc]\n", "security.tmpfs[0].path", "invalid value"},
		"the root as tmpfs":                         {"security:\n  tmpfs: [/]\n", "security.tmpfs[0].path", "invalid value"},
		"a tmpfs path twice":                        {"security:\n  tmpfs: [/tmp, /tmp]\n", "security.tmpfs[1].path", "already mounted"},
		"a tmpfs over a volume": {"deploy:\n  strategy: recreate\nvolumes:\n  - name: data\n    path: /data\nsecurity:\n  tmpfs: [/data]\n",
			"security.tmpfs[0].path", "volume data"},
		"a tmpfs larger than the bound":  {"security:\n  tmpfs:\n    - path: /tmp\n      size: 2gb\n", "security.tmpfs[0].size", "out of range"},
		"a tmpfs smaller than the bound": {"security:\n  tmpfs:\n    - path: /tmp\n      size: 4kb\n", "security.tmpfs[0].size", "out of range"},
		"a tmpfs size that is no size":   {"security:\n  tmpfs:\n    - path: /tmp\n      size: plenty\n", "security.tmpfs[0].size", "invalid value"},
		"a misspelt tmpfs key":           {"security:\n  tmpfs:\n    - path: /tmp\n      sise: 1gb\n", "security", `unknown field "sise"`},
		"too many tmpfs":                 {"security:\n  tmpfs: [/a, /b, /c, /d, /e, /f, /g, /h, /i, /j, /k]\n", "security.tmpfs", "too many"},
		"a misspelt key":                 {"security:\n  readonly: true\n", "security", `unknown field "readonly"`},
		"a knob that would add":          {"security:\n  privileged: true\n", "security", `unknown field "privileged"`},
		"a list":                         {"security: [read_only]\n", "security", "must be a block"},
		"root next to non_root":          {"user: root\nsecurity:\n  non_root: true\n", "user", "it is root"},
		"id 0 next to non_root":          {"user: \"0:0\"\nsecurity:\n  non_root: true\n", "user", "id 0 is root"},
		"a named user next to non_root":  {"user: app\nsecurity:\n  non_root: true\n", "user", "/etc/passwd"},
	} {
		_, err := Parse([]byte(securityBase + tc.doc))
		verr, ok := err.(*ValidationError)
		if !ok {
			t.Errorf("%s was accepted (%v)", name, err)
			continue
		}
		found := false
		for _, f := range verr.Fields {
			found = found || (f.Field == tc.field && strings.Contains(f.Message, tc.says))
		}
		if !found {
			t.Errorf("%s: %+v, want %s to say %q", name, verr.Fields, tc.field, tc.says)
		}
	}
}

func TestNonRootAcceptsANumericUserAndLeavesTheImagesToTheAgent(t *testing.T) {
	for _, user := range []string{"", "user: \"1000\"\n", "user: \"1000:0\"\n"} {
		if _, err := Parse([]byte(securityBase + user + "security:\n  non_root: true\n")); err != nil {
			t.Errorf("%q next to non_root: %v", user, err)
		}
	}
}

func TestSecurityIsRefusedForAStaticApplication(t *testing.T) {
	_, err := Parse([]byte("name: site\ndomain: example.com\nstatic: dist\nsecurity:\n  read_only: true\n"))
	if err == nil || !strings.Contains(err.Error(), "security") || !strings.Contains(err.Error(), "does not apply to a static application") {
		t.Errorf("security next to static: %v", err)
	}
}

func TestRootUser(t *testing.T) {
	for user, root := range map[string]bool{
		"": true, "0": true, "0:0": true, "000": true, "0:1000": true, "root": true, "root:root": true,
		"app": true, "node": true, "nginx:nginx": true,
		"1000": false, "1000:1000": false, "101:0": false, "65534": false, "1000:root": false,
	} {
		if got := RootUser(user) != ""; got != root {
			t.Errorf("RootUser(%q) = %q, want root = %v", user, RootUser(user), root)
		}
	}
}

func TestADocumentWritesSecurityAsItIsWritten(t *testing.T) {
	a, err := Parse([]byte(securityBase + "security:\n  read_only: true\n  tmpfs:\n    - /tmp\n    - path: /scratch\n      size: 16mb\n  capabilities: none\n"))
	if err != nil {
		t.Fatal(err)
	}
	text, _, err := Document(a, References{}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "security:\n  read_only: true\n  tmpfs:\n    - /tmp\n    - path: /scratch\n      size: 16mb\n  capabilities: none\n"
	if !strings.Contains(text, want) {
		t.Errorf("the document says\n%s\nwant it to hold\n%s", text, want)
	}
	a.Security.Capabilities = &[]string{"CHOWN", "SETUID"}
	if text, _, _ = Document(a, References{}, false); !strings.Contains(text, "  capabilities: [CHOWN, SETUID]\n") {
		t.Errorf("a list of capabilities is written as\n%s", text)
	}
}
