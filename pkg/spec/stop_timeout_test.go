package spec

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStopTimeoutIsParsedAndStored(t *testing.T) {
	app, err := Parse([]byte("name: chat\nimage: chat:1\ndeploy:\n  stop_timeout: 2m\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := app.Deploy.StopTimeout.Std(); got != 2*time.Minute {
		t.Errorf("stop_timeout = %s, want 2m", got)
	}
	if app.Deploy.Strategy != StrategyRolling {
		t.Errorf("strategy = %q: stop_timeout must not disturb the default", app.Deploy.Strategy)
	}

	// The stored spec is JSON: the value must survive the round trip.
	b, err := json.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	var back App
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Deploy.StopTimeout != app.Deploy.StopTimeout {
		t.Errorf("after a JSON round trip stop_timeout = %s", back.Deploy.StopTimeout)
	}
}

func TestStopTimeoutIsLeftOutWhenNotSet(t *testing.T) {
	app, err := Parse([]byte("name: chat\nimage: chat:1\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if app.Deploy.StopTimeout != 0 {
		t.Errorf("stop_timeout = %s, want zero: the agent's default applies", app.Deploy.StopTimeout)
	}
	b, _ := json.Marshal(app)
	if strings.Contains(string(b), "stop_timeout") {
		t.Errorf("an unset stop_timeout appears in the stored spec: %s", b)
	}
}

func TestStopTimeoutBounds(t *testing.T) {
	for value, valid := range map[string]bool{
		"1s": true, "10m": true, "90s": true,
		"999ms": false, "0s": false, "10m1s": false, "1h": false, "soon": false, "30": false,
	} {
		_, err := Parse([]byte("name: chat\nimage: chat:1\ndeploy:\n  stop_timeout: " + value + "\n"))
		if valid {
			if err != nil {
				t.Errorf("stop_timeout %s: %v", value, err)
			}
			continue
		}
		var verr *ValidationError
		if !errors.As(err, &verr) || len(verr.Fields) != 1 || verr.Fields[0].Field != "deploy.stop_timeout" {
			t.Errorf("stop_timeout %s: err = %v, want one error on deploy.stop_timeout", value, err)
		}
	}
}

func TestStopTimeoutDoesNotApplyToAStaticApplication(t *testing.T) {
	_, err := Parse([]byte("name: site\nstatic: dist\ndomain: example.com\ndeploy:\n  stop_timeout: 30s\n"))
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want a validation error", err)
	}
	if len(verr.Fields) != 1 || verr.Fields[0].Field != "deploy.stop_timeout" || !strings.Contains(verr.Fields[0].Message, "static application") {
		t.Errorf("fields = %+v, want deploy.stop_timeout refused for a static application", verr.Fields)
	}
}
