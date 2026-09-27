package spec

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const jobsConfig = `
name: my-api
image: ghcr.io/company/my-api:1.4.2
pre_deploy:
  command: ["dotnet", "Migrate.dll"]
jobs:
  - name: nightly-report
    schedule: "0 3 * * *"
    command: ["node", "report.js"]
  - name: cleanup
    schedule: "  */15  *  * * *"
    command: ["sh", "-c", "rm -rf /tmp/cache/*"]
    timeout: 5m
`

func TestParseJobs(t *testing.T) {
	app, err := Parse([]byte(jobsConfig))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if app.PreDeploy == nil || app.PreDeploy.Command[1] != "Migrate.dll" || app.PreDeploy.Timeout.Std() != DefaultHookTimeout {
		t.Errorf("unexpected hook: %+v", app.PreDeploy)
	}
	if len(app.Jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(app.Jobs))
	}
	if j := app.Jobs[0]; j.Name != "nightly-report" || j.Schedule != "0 3 * * *" || j.Timeout.Std() != DefaultJobTimeout {
		t.Errorf("unexpected job: %+v", j)
	}
	if j := app.Jobs[1]; j.Schedule != "*/15 * * * *" || j.Timeout.Std() != 5*time.Minute || len(j.Command) != 3 {
		t.Errorf("unexpected job: %+v", j)
	}
}

func TestJobsValidation(t *testing.T) {
	tests := map[string]struct {
		yaml  string
		field string
		msg   string
	}{
		"hook without command":  {"pre_deploy:\n  timeout: 1m\n", "pre_deploy.command", "is required"},
		"hook timeout too long": {"pre_deploy:\n  command: [migrate]\n  timeout: 2h\n", "pre_deploy.timeout", "out of range"},
		"hook timeout garbage":  {"pre_deploy:\n  command: [migrate]\n  timeout: soon\n", "pre_deploy.timeout", `invalid value "soon"`},
		"job without name":      {"jobs:\n  - schedule: '* * * * *'\n    command: [x]\n", "jobs[0].name", "is required"},
		"job name with case":    {"jobs:\n  - name: Nightly\n    schedule: '* * * * *'\n    command: [x]\n", "jobs[0].name", `invalid value "Nightly"`},
		"job name too long":     {"jobs:\n  - name: " + strings.Repeat("a", 41) + "\n    schedule: '* * * * *'\n    command: [x]\n", "jobs[0].name", "invalid value"},
		"reserved name":         {"jobs:\n  - name: run\n    schedule: '* * * * *'\n    command: [x]\n", "jobs[0].name", `"run" is reserved`},
		"duplicate name":        {"jobs:\n  - name: a\n    schedule: '* * * * *'\n    command: [x]\n  - name: a\n    schedule: '* * * * *'\n    command: [x]\n", "jobs[1].name", "used twice"},
		"missing schedule":      {"jobs:\n  - name: a\n    command: [x]\n", "jobs[0].schedule", "is required"},
		"bad schedule":          {"jobs:\n  - name: a\n    schedule: '61 * * * *'\n    command: [x]\n", "jobs[0].schedule", "minute: value 61 out of range"},
		"six fields":            {"jobs:\n  - name: a\n    schedule: '* * * * * *'\n    command: [x]\n", "jobs[0].schedule", "expected 5 fields"},
		"missing command":       {"jobs:\n  - name: a\n    schedule: '* * * * *'\n", "jobs[0].command", "is required"},
		"empty command":         {"jobs:\n  - name: a\n    schedule: '* * * * *'\n    command: ['']\n", "jobs[0].command", "is required"},
		"job timeout too long":  {"jobs:\n  - name: a\n    schedule: '* * * * *'\n    command: [x]\n    timeout: 25h\n", "jobs[0].timeout", "out of range"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte("name: my-api\nimage: nginx:1\n" + tt.yaml))
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("err = %v, want a validation error", err)
			}
			for _, f := range verr.Fields {
				if f.Field == tt.field && strings.Contains(f.Message, tt.msg) {
					return
				}
			}
			t.Errorf("no error on %s containing %q; got %v", tt.field, tt.msg, verr.Fields)
		})
	}
}

func TestTooManyJobs(t *testing.T) {
	var b strings.Builder
	b.WriteString("name: my-api\nimage: nginx:1\njobs:\n")
	for i := 0; i <= MaxJobs; i++ {
		fmt.Fprintf(&b, "  - name: job-%d\n    schedule: '* * * * *'\n    command: [x]\n", i)
	}
	_, err := Parse([]byte(b.String()))
	var verr *ValidationError
	if !errors.As(err, &verr) || verr.Fields[0].Field != "jobs" || !strings.Contains(verr.Fields[0].Message, "too many") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateCommand(t *testing.T) {
	long := strings.Repeat("x", MaxCommandArgBytes+1)
	many := make([]string, MaxCommandArgs+1)
	for i := range many {
		many[i] = "a"
	}
	tests := []struct {
		argv []string
		want string
	}{
		{nil, "is required"},
		{[]string{" "}, "is required"},
		{[]string{"sh", "-c", "echo hi"}, ""},
		{[]string{"a", "b\x00c"}, "argument 2 must not contain NUL"},
		{[]string{"a", long}, "argument 2 is longer than"},
		{many, "too many arguments"},
	}
	for _, tt := range tests {
		err := ValidateCommand(tt.argv)
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("ValidateCommand(%q) = %v, want nil", tt.argv, err)
		case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
			t.Errorf("ValidateCommand(%d args) = %v, want %q", len(tt.argv), err, tt.want)
		}
	}
}

func TestJobsSurviveJSONRoundTrip(t *testing.T) {
	app, err := Parse([]byte(jobsConfig))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	var roundTripped App
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatal(err)
	}
	if roundTripped.PreDeploy == nil || roundTripped.PreDeploy.Timeout != app.PreDeploy.Timeout {
		t.Errorf("hook lost in JSON: %+v", roundTripped.PreDeploy)
	}
	if len(roundTripped.Jobs) != 2 || roundTripped.Jobs[1].Timeout != app.Jobs[1].Timeout || roundTripped.Jobs[0].Schedule != "0 3 * * *" {
		t.Errorf("jobs lost in JSON: %+v", roundTripped.Jobs)
	}
}
