package spec

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInitIsOffUnlessAskedFor(t *testing.T) {
	app, err := Parse([]byte("name: api\nimage: api:1\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if app.Init {
		t.Error("init is on by default: an image that brings its own init would run two")
	}
	if b, _ := json.Marshal(app); strings.Contains(string(b), `"init"`) {
		t.Errorf("an unset init appears in the stored spec: %s", b)
	}
}

func TestInitIsParsedAndStored(t *testing.T) {
	app, err := Parse([]byte("name: api\nimage: api:1\ninit: true\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !app.Init {
		t.Fatal("init: true was not kept")
	}
	b, err := json.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	var back App
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Init {
		t.Errorf("init was lost in the stored spec: %s", b)
	}

	if app, err := Parse([]byte("name: api\nimage: api:1\ninit: false\n")); err != nil || app.Init {
		t.Errorf("init: false: %+v, %v", app.Init, err)
	}
}

func TestInitMustBeABoolean(t *testing.T) {
	_, err := Parse([]byte("name: api\nimage: api:1\ninit: tini\n"))
	if err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Errorf("init: tini: %v, want an error that names the line", err)
	}
}

func TestInitIsRefusedForAStaticApplication(t *testing.T) {
	for _, value := range []string{"true", "false"} {
		_, err := Parse([]byte("name: site\ndomain: example.com\nstatic: dist\ninit: " + value + "\n"))
		if err == nil || !strings.Contains(err.Error(), "init") || !strings.Contains(err.Error(), "does not apply to a static application") {
			t.Errorf("init: %s next to static: %v", value, err)
		}
	}
}
