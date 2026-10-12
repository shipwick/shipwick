package spec

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const splunkConfig = "name: api\nimage: app:1\nenv:\n  A: b\nlogging:\n  driver: splunk\n  options:\n    splunk-url: https://hec.example.com:8088\n    splunk-token: 12345678-aaaa-bbbb-cccc-dddddddddddd\n"

func TestRedactedMasksTheSplunkToken(t *testing.T) {
	app, err := Parse([]byte(splunkConfig))
	if err != nil {
		t.Fatal(err)
	}
	red := app.Redacted()
	if red.Logging.Options["splunk-token"] != Mask || red.Logging.Options["splunk-url"] != "https://hec.example.com:8088" || red.Logging.Driver != "splunk" {
		t.Errorf("redacted logging = %+v", red.Logging)
	}
	if out, _ := json.Marshal(red); strings.Contains(string(out), "12345678-aaaa") {
		t.Errorf("the token is in the redacted JSON: %s", out)
	}
	if app.Logging.Options["splunk-token"] != "12345678-aaaa-bbbb-cccc-dddddddddddd" {
		t.Error("Redacted changed the spec it was called on")
	}
	if got := app.SecretLogOptions(); !reflect.DeepEqual(got, []string{"splunk-token"}) {
		t.Errorf("SecretLogOptions = %v", got)
	}
	plain := App{Logging: &Logging{Driver: "gelf", Options: map[string]string{"gelf-address": "udp://logs.example.com:12201"}}}
	if got := plain.Redacted().Logging.Options["gelf-address"]; got != "udp://logs.example.com:12201" {
		t.Errorf("an option that is no credential was masked: %q", got)
	}
}

func TestTheDocumentMasksTheSplunkTokenAndSaysItsReference(t *testing.T) {
	app, err := Parse([]byte(splunkConfig))
	if err != nil {
		t.Fatal(err)
	}
	app.Env = nil

	// A literal token is not handed out: the mask stands in its place, the
	// document names the field, and parsed again it is a mask Parse accepts
	// and MaskedFields names in the same order.
	text, masked, err := Document(app, References{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(masked, []string{"logging.options.splunk-token"}) || strings.Contains(text, "12345678-aaaa") || !strings.Contains(text, "splunk-url: https://hec.example.com:8088") {
		t.Errorf("masked = %v:\n%s", masked, text)
	}
	got, err := Parse([]byte(text))
	if err != nil {
		t.Fatalf("the document does not parse: %v\n%s", err, text)
	}
	if !reflect.DeepEqual(MaskedFields(got), masked) {
		t.Errorf("MaskedFields = %v, want %v", MaskedFields(got), masked)
	}

	// Written as a reference, the reference is what comes back.
	app.Logging.Options["splunk-token"] = "${SPLUNK_TOKEN}"
	refs := ReferencesOf(app)
	if !reflect.DeepEqual(refs.Logging, map[string]string{"splunk-token": "${SPLUNK_TOKEN}"}) || refs.Empty() {
		t.Errorf("references = %+v", refs)
	}
	app.Logging.Options["splunk-token"] = "the-value-filled-in"
	text, masked, err = Document(app, refs, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(masked) != 0 || !strings.Contains(text, "splunk-token: ${SPLUNK_TOKEN}") || strings.Contains(text, "the-value-filled-in") {
		t.Errorf("masked = %v:\n%s", masked, text)
	}
	if kept := refs.For(App{Logging: &Logging{Driver: "gelf"}}); len(kept.Logging) != 0 {
		t.Errorf("a reference to an option the application has not got was kept: %+v", kept)
	}
	if kept := refs.For(App{}); !kept.Empty() {
		t.Errorf("references of nothing: %+v", kept)
	}
}

func TestMaskedFieldsNamesTheSplunkTokenBetweenEnvAndProxy(t *testing.T) {
	a := App{
		Env:     map[string]string{"B": Mask, "A": Mask},
		Logging: &Logging{Driver: "splunk", Options: map[string]string{"splunk-token": Mask, "splunk-url": "https://hec.example.com:8088"}},
		Proxy:   &Proxy{BasicAuth: []BasicAuth{{Username: "u", Password: Mask}}},
	}
	want := []string{"env.A", "env.B", "logging.options.splunk-token", "proxy.basic_auth[0].password"}
	if got := MaskedFields(a); !reflect.DeepEqual(got, want) {
		t.Errorf("masked = %v, want %v", got, want)
	}
}
