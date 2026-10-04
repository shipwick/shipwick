package spec

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// secretsOf lists every secret value of a.
func secretsOf(a App) []string {
	var out []string
	for _, v := range a.Env {
		out = append(out, v)
	}
	if a.Proxy != nil {
		for _, account := range a.Proxy.BasicAuth {
			out = append(out, account.Password)
		}
	}
	return out
}

func TestADocumentParsesBackToTheApplicationItDescribes(t *testing.T) {
	for _, a := range everyField() {
		text, masked, err := Document(a, References{}, false)
		if err != nil {
			t.Fatalf("%s: %v", a.Name, err)
		}
		got, err := Parse([]byte(text))
		if err != nil {
			t.Errorf("%s: the document does not parse: %v\n%s", a.Name, err, text)
			continue
		}
		// Its secrets are the one thing a document does not say.
		want := a.Redacted()
		if !reflect.DeepEqual(got, want) {
			w, _ := json.MarshalIndent(want, "", "  ")
			g, _ := json.MarshalIndent(got, "", "  ")
			t.Errorf("%s came back changed:\nwant %s\ngot  %s\nfrom\n%s", a.Name, w, g, text)
		}
		if !reflect.DeepEqual(masked, MaskedFields(got)) {
			t.Errorf("%s: masked = %v, and the document holds masks at %v", a.Name, masked, MaskedFields(got))
		}
	}
}

func TestADocumentNeverHoldsASecretValue(t *testing.T) {
	for _, a := range everyField() {
		// A reference to something the application does not have is not a
		// way to a value either.
		refs := References{Env: map[string]string{"GONE": "${GONE}"}, BasicAuth: map[int]string{7: "${GONE}"}}
		for name := range a.Env {
			a.Env[name] = "hunter2-" + name
		}
		text, _, err := Document(a, refs, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range secretsOf(a) {
			if strings.Contains(text, value) {
				t.Errorf("%s: the document holds the secret value %q:\n%s", a.Name, value, text)
			}
		}
		if strings.Contains(text, "GONE") {
			t.Errorf("%s: the document holds a reference nothing has:\n%s", a.Name, text)
		}
	}
}

func TestADocumentSaysAReferenceWhereOneWasWritten(t *testing.T) {
	a := everyField()[0]
	refs := References{
		Env:       map[string]string{"DATABASE_URL": "postgres://shop:${DB_PASSWORD}@db/shop", "EMPTY": "$${NOT_ONE}", "MISSING": "${X}"},
		BasicAuth: map[int]string{1: "${ADMIN_PASSWORD}"},
	}
	text, masked, err := Document(a, refs, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse([]byte(text))
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if got.Env["DATABASE_URL"] != "postgres://shop:${DB_PASSWORD}@db/shop" || got.Proxy.BasicAuth[1].Password != "${ADMIN_PASSWORD}" {
		t.Errorf("env = %v, accounts = %+v; want the references back", got.Env, got.Proxy.BasicAuth)
	}
	// What refers to nothing is no reference: the value was a literal.
	want := []string{"env.EMPTY", "env.MULTILINE", "proxy.basic_auth[0].password"}
	if !reflect.DeepEqual(masked, want) {
		t.Errorf("masked = %v, want %v", masked, want)
	}
	if got.Env["EMPTY"] != Mask || got.Env["MULTILINE"] != Mask || got.Proxy.BasicAuth[0].Password != Mask {
		t.Errorf("env = %v, accounts = %+v; want a mask for every literal", got.Env, got.Proxy.BasicAuth)
	}
	if !strings.HasPrefix(text, "# A value shown as \"********\"") || strings.Count(text, maskedComment) != len(want) {
		t.Errorf("the masks are not explained where they stand:\n%s", text)
	}
	if !reflect.DeepEqual(ReferencesOf(got), References{Env: map[string]string{"DATABASE_URL": refs.Env["DATABASE_URL"]}, BasicAuth: refs.BasicAuth}) {
		t.Errorf("references of the parsed document = %+v", ReferencesOf(got))
	}
}

func TestADocumentWithoutMasksHasNoNotice(t *testing.T) {
	a := everyField()[1]
	a.Env = map[string]string{"TOKEN": "resolved"}
	text, masked, err := Document(a, References{Env: map[string]string{"TOKEN": "${TOKEN}"}}, false)
	if err != nil || len(masked) != 0 || strings.Contains(text, "#") {
		t.Errorf("masked = %v, err = %v\n%s", masked, err, text)
	}
}

func TestADocumentIsWrittenTheWayInitWritesOne(t *testing.T) {
	a := App{
		Name: "api", Image: "ghcr.io/company/api:1.4.2", Port: 8080, Domain: "api.example.com", Replicas: 2,
		Env:       map[string]string{"DATABASE_URL": "postgres://app:hunter2@db:5432/app", "LOG_LEVEL": "info"},
		Health:    &Health{Path: "/health", Interval: Duration(DefaultHealthInterval), Timeout: Duration(DefaultHealthTimeout), Retries: DefaultHealthRetries},
		Resources: Resources{CPU: 1, MemoryBytes: 512 << 20},
		PreDeploy: &Hook{Command: []string{"api", "migrate"}, Timeout: Duration(DefaultHookTimeout)},
		Restart:   Restart{Policy: RestartAlways}, Deploy: Deploy{Strategy: StrategyRolling},
	}
	text, _, err := Document(a, References{Env: map[string]string{"DATABASE_URL": "postgres://app:${DB_PASSWORD}@db:5432/app"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	const want = `# A value shown as "********" was given when the application was deployed and
# is not handed out. Write it again, or store it on the server with
# shipwick secret set NAME and refer to it as ${NAME}. A deployment of this
# file is refused until every such value has been replaced.

name: api

image: ghcr.io/company/api:1.4.2

port: 8080

domain: api.example.com

replicas: 2

env:
  DATABASE_URL: postgres://app:${DB_PASSWORD}@db:5432/app
  LOG_LEVEL: "********" # not handed out: write the value again, or refer to a secret as ${NAME}

health:
  path: /health
  interval: 10s
  timeout: 3s
  retries: 3

resources:
  cpu: 1
  memory: 512mb

pre_deploy:
  command: ["api", "migrate"]
  timeout: 10m

restart:
  policy: always
`
	if text != want {
		t.Errorf("document:\n%s\nwant:\n%s", text, want)
	}
}

func TestAnEscapedDocumentIsReadByTheCLIAsTheSameApplication(t *testing.T) {
	a := everyField()[1]
	a.Command = []string{"sh", "-c", "exec app --data ${DATA_DIR} --literal $${KEPT}"}
	a.Env = map[string]string{"TOKEN": "resolved"}
	refs := References{Env: map[string]string{"TOKEN": "${TOKEN}-$${NOT}"}}

	plain, _, err := Document(a, refs, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Parse([]byte(plain)); err != nil || !reflect.DeepEqual(got.Command, a.Command) {
		t.Errorf("as the agent reads it: command = %q, err = %v", got.Command, err)
	}

	escaped, _, err := Document(a, refs, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse([]byte(escaped))
	if err != nil {
		t.Fatal(err)
	}
	// What the CLI does to a value outside env: $${NAME} becomes ${NAME}.
	for i, arg := range got.Command {
		got.Command[i] = Placeholder.ReplaceAllStringFunc(arg, func(match string) string {
			m := Placeholder.FindStringSubmatch(match)
			if m[1] == "" {
				t.Errorf("the CLI would fill in %s of %q", match, arg)
			}
			return "${" + m[2] + "}"
		})
	}
	if !reflect.DeepEqual(got.Command, a.Command) {
		t.Errorf("as the CLI reads it: command = %q, want %q", got.Command, a.Command)
	}
	// A secret value is the agent's to read, and is the same in both.
	if got.Env["TOKEN"] != "${TOKEN}-$${NOT}" {
		t.Errorf("env = %v", got.Env)
	}
}

func TestMaskedFieldsNamesEveryMask(t *testing.T) {
	a := everyField()[0].Redacted()
	a.Env["REAL"] = "a value"
	want := []string{"env.DATABASE_URL", "env.EMPTY", "env.MULTILINE", "proxy.basic_auth[0].password", "proxy.basic_auth[1].password"}
	if got := MaskedFields(a); !reflect.DeepEqual(got, want) {
		t.Errorf("masked = %v, want %v", got, want)
	}
	if got := MaskedFields(everyField()[0]); got != nil {
		t.Errorf("an application with values has masks at %v", got)
	}
}

func TestBeforeInIsReplicaUnlessItSaysContainer(t *testing.T) {
	const before = "backups:\n  schedule: '0 3 * * *'\n  before: [pg_dump, app]\n"
	for in, want := range map[string]string{"": "", "  before_in: replica\n": "", "  before_in: container\n": BeforeInContainer} {
		app, err := Parse([]byte(backupsBase + before + in))
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if app.Backups.BeforeIn != want {
			t.Errorf("%q: before_in = %q, want %q", in, app.Backups.BeforeIn, want)
		}
	}
	for name, c := range map[string]struct{ block, message string }{
		"unknown": {before + "  before_in: sidecar\n", `invalid value "sidecar"`},
		"alone":   {"backups:\n  schedule: '0 3 * * *'\n  before_in: container\n", "needs backups.before"},
	} {
		_, err := Parse([]byte(backupsBase + c.block))
		if err == nil || !strings.Contains(err.Error(), "backups.before_in") || !strings.Contains(err.Error(), c.message) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestAPlainValueIsWrittenAsItIsAndReadsBackTheSame(t *testing.T) {
	a, err := Parse([]byte("name: my-api\nimage: my-api:1\n"))
	if err != nil {
		t.Fatal(err)
	}
	// As the agent stores them: among them a literal ${NAME}, and one that
	// was escaped twice.
	a.Env = map[string]string{"LOG_LEVEL": "debug", "PORT": "8080", "ON": "true", "EMPTY": "", "LINES": "one\ntwo",
		"TEMPLATE": "Hello ${NAME}", "ESCAPED": "$${NAME}", "API_KEY": "sk-1"}
	refs := References{Plain: []string{"LOG_LEVEL", "PORT", "ON", "EMPTY", "LINES", "TEMPLATE", "ESCAPED"}}

	for _, escape := range []bool{false, true} {
		text, masked, err := Document(a, refs, escape)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(masked, []string{"env.API_KEY"}) || strings.Contains(text, "sk-1") {
			t.Errorf("masked = %v:\n%s", masked, text)
		}
		got, err := Parse([]byte(text))
		if err != nil {
			t.Fatalf("the document does not parse: %v\n%s", err, text)
		}
		if len(ReferencesOf(got).Env) != 0 {
			t.Errorf("a plain value reads as a reference:\n%s", text)
		}
		// What the agent makes of the document when it is deployed again.
		for name, value := range got.Env {
			got.Env[name] = Placeholder.ReplaceAllStringFunc(value, func(m string) string { return strings.TrimPrefix(m, "$") })
		}
		want := map[string]string{"LOG_LEVEL": "debug", "PORT": "8080", "ON": "true", "EMPTY": "", "LINES": "one\ntwo",
			"TEMPLATE": "Hello ${NAME}", "ESCAPED": "$${NAME}", "API_KEY": Mask}
		if !reflect.DeepEqual(got.Env, want) {
			t.Errorf("escape=%v: env = %q, want %q\n%s", escape, got.Env, want, text)
		}
	}
}

func TestOnlyAValueOfTheApplicationThatIsNoReferenceIsPlain(t *testing.T) {
	a := App{Env: map[string]string{"DATABASE_URL": "postgres://app:hunter2@db/app", "LOG_LEVEL": "debug", "REGION": "eu"}}
	refs := References{
		Env:   map[string]string{"DATABASE_URL": "postgres://app:${DB_PASSWORD}@db/app"},
		Plain: []string{"REGION", "GONE", "DATABASE_URL", "LOG_LEVEL", "REGION"},
	}
	if got := refs.For(a).Plain; !reflect.DeepEqual(got, []string{"LOG_LEVEL", "REGION"}) {
		t.Errorf("plain = %v", got)
	}
	if got := refs.PlainFields(a); !reflect.DeepEqual(got, []string{"env.LOG_LEVEL", "env.REGION"}) {
		t.Errorf("fields = %v", got)
	}
	if (References{Plain: []string{"LOG_LEVEL"}}).Empty() {
		t.Error("a statement about plain values is something to keep")
	}
}
