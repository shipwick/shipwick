package spec

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/shipwick/shipwick/pkg/cron"
)

// Defaults and bounds of the pre-deploy hook and the scheduled jobs.
const (
	DefaultHookTimeout = 10 * time.Minute
	DefaultJobTimeout  = time.Hour
	MaxHookTimeout     = time.Hour
	MaxJobTimeout      = 24 * time.Hour
	MaxJobs            = 20
	MaxJobNameLength   = 40
	// MaxCommandArgs and MaxCommandArgBytes bound a command: it ends up in a
	// container's argv, and Docker has limits of its own.
	MaxCommandArgs     = 256
	MaxCommandArgBytes = 4096
)

// ReservedJobNames are the job names Shipwick uses for runs that have no job
// in deploy.yaml: the pre-deploy hook and `shipwick run`.
var ReservedJobNames = []string{"pre-deploy", "run"}

// jobNamePattern is a DNS label with a shorter cap: the name is part of the
// job container's name.
var jobNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// ValidateJobName reports whether s can name a job.
func ValidateJobName(s string) error {
	if !jobNamePattern.MatchString(s) {
		return fmt.Errorf("invalid job name %q: use lowercase letters, digits and dashes (max %d characters)", s, MaxJobNameLength)
	}
	return nil
}

// ValidateCommand reports whether argv can be handed to a container as its
// command. The agent runs it for `shipwick run` too, where it comes from an
// API request rather than from deploy.yaml.
func ValidateCommand(argv []string) error {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return fmt.Errorf("is required")
	}
	if len(argv) > MaxCommandArgs {
		return fmt.Errorf("too many arguments (%d)", len(argv))
	}
	for i, arg := range argv {
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("argument %d must not contain NUL bytes", i+1)
		}
		if len(arg) > MaxCommandArgBytes {
			return fmt.Errorf("argument %d is longer than %d bytes", i+1, MaxCommandArgBytes)
		}
	}
	return nil
}

// validateJobs checks the pre-deploy hook and the scheduled jobs.
func (r raw) validateJobs(verr *ValidationError) (*Hook, []Job) {
	var hook *Hook
	if r.PreDeploy != nil {
		hook = &Hook{Command: r.PreDeploy.Command, Timeout: Duration(DefaultHookTimeout)}
		if err := ValidateCommand(hook.Command); err != nil {
			verr.add("pre_deploy.command", err.Error(), `["dotnet", "Migrate.dll"]`)
		}
		if v := r.PreDeploy.Timeout; v != "" {
			d, err := parseDuration(v, time.Second, MaxHookTimeout)
			if err != nil {
				verr.add("pre_deploy.timeout", err.Error(), "30s, 10m, 1h, ... (1s to 1h)")
			}
			hook.Timeout = Duration(d)
		}
	}

	if len(r.Jobs) == 0 {
		return hook, nil
	}
	if len(r.Jobs) > MaxJobs {
		verr.add("jobs", fmt.Sprintf("too many (%d)", len(r.Jobs)), fmt.Sprintf("at most %d", MaxJobs))
		return hook, nil
	}
	jobs := make([]Job, 0, len(r.Jobs))
	names := map[string]bool{}
	for i, j := range r.Jobs {
		field := fmt.Sprintf("jobs[%d]", i)
		job := Job{Name: j.Name, Schedule: strings.Join(strings.Fields(j.Schedule), " "), Command: j.Command, Timeout: Duration(DefaultJobTimeout)}
		switch {
		case j.Name == "":
			verr.add(field+".name", "is required", "nightly-report")
		case ValidateJobName(j.Name) != nil:
			verr.add(field+".name", fmt.Sprintf("invalid value %q", j.Name),
				fmt.Sprintf("lowercase letters, digits and dashes, e.g. nightly-report (max %d characters)", MaxJobNameLength))
		case slices.Contains(ReservedJobNames, j.Name):
			verr.add(field+".name", fmt.Sprintf("%q is reserved", j.Name), "another name, e.g. nightly-report")
		case names[j.Name]:
			verr.add(field+".name", fmt.Sprintf("%q is used twice", j.Name), "a different name for each job")
		}
		names[j.Name] = true

		if job.Schedule == "" {
			verr.add(field+".schedule", "is required", `"0 3 * * *" (minute hour day-of-month month day-of-week, in UTC)`)
		} else if _, err := cron.Parse(job.Schedule); err != nil {
			verr.add(field+".schedule", fmt.Sprintf("invalid value %q: %v", j.Schedule, err),
				`five cron fields in UTC, e.g. "0 3 * * *" (every day at 03:00), "*/15 * * * *" (every 15 minutes)`)
		}
		if err := ValidateCommand(job.Command); err != nil {
			verr.add(field+".command", err.Error(), `["node", "report.js"]`)
		}
		if v := j.Timeout; v != "" {
			d, err := parseDuration(v, time.Second, MaxJobTimeout)
			if err != nil {
				verr.add(field+".timeout", err.Error(), "5m, 1h, 12h, ... (1s to 24h)")
			}
			job.Timeout = Duration(d)
		}
		jobs = append(jobs, job)
	}
	return hook, jobs
}
