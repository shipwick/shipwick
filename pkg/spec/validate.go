package spec

import "strconv"

// Validate checks an App that Parse did not produce: one read out of an
// export, which arrives over the API like a deploy.yaml and ends up in the
// same container names, URLs and proxy configuration. There is one set of
// rules, the one a document is held to, so the value is written back as the
// document that says it and that document is validated. The App returned is
// what Parse would have made of it; for a value Parse did produce, it is the
// same value.
//
// On invalid input the error is a *ValidationError, as from Parse.
func Validate(a App) (App, error) {
	return documentOf(a).validate()
}

// documentOf is the inverse of raw.validate: the deploy.yaml an App came from,
// as far as the App remembers it. A field of App that is not written here
// would be lost by Validate, which TestValidateKeepsEveryField notices.
func documentOf(a App) raw {
	r := raw{
		Name:      a.Name,
		Image:     a.Image,
		Domain:    a.Domain,
		Aliases:   a.Aliases,
		Redirects: a.Redirects,
		Env:       a.Env,
		User:      a.User,
		Path:      a.Path,
	}
	if a.Port != 0 {
		r.Port = &a.Port
	}
	// A static application has no replicas to count; Parse gives it the
	// default and refuses any other.
	if a.Static == nil || a.Replicas != DefaultReplicas {
		r.Replicas = &a.Replicas
	}

	if h := a.Health; h != nil {
		out := alloc(&r.Health)
		out.Path = h.Path
		if h.TCP != 0 {
			out.TCP = &h.TCP
		}
		if len(h.Command) > 0 {
			out.Command = h.Command
		}
		out.Interval, out.Timeout, out.Retries = h.Interval.String(), h.Timeout.String(), &h.Retries
		if h.StartPeriod != 0 {
			out.StartPeriod = h.StartPeriod.String()
		}
	}

	if a.Resources.CPU != 0 {
		r.Resources.CPU = strconv.FormatFloat(a.Resources.CPU, 'f', -1, 64)
	}
	if a.Resources.MemoryBytes != 0 {
		r.Resources.Memory = strconv.FormatInt(a.Resources.MemoryBytes, 10)
	}

	for i := range sized(&r.Volumes, len(a.Volumes)) {
		r.Volumes[i].Name, r.Volumes[i].Path = a.Volumes[i].Name, a.Volumes[i].Path
	}
	for i := range sized(&r.Publish, len(a.Publish)) {
		p := &a.Publish[i]
		r.Publish[i].Port, r.Publish[i].Host, r.Publish[i].Address, r.Publish[i].Protocol = &p.Port, &p.Host, p.Address, p.Protocol
	}

	// An empty list is what the App has when the key was not given.
	if len(a.Entrypoint) > 0 {
		r.Entrypoint = a.Entrypoint
	}
	if len(a.Command) > 0 {
		r.Command = a.Command
	}
	if h := a.PreDeploy; h != nil {
		out := alloc(&r.PreDeploy)
		out.Command, out.Timeout = h.Command, h.Timeout.String()
	}
	for i := range sized(&r.Jobs, len(a.Jobs)) {
		j := a.Jobs[i]
		r.Jobs[i].Name, r.Jobs[i].Schedule, r.Jobs[i].Command, r.Jobs[i].Timeout = j.Name, j.Schedule, j.Command, j.Timeout.String()
	}
	if l := a.Logging; l != nil {
		out := alloc(&r.Logging)
		out.Driver, out.Options = l.Driver, l.Options
	}
	if b := a.Build; b != nil {
		r.Build = buildRaw{Context: b.Context, Dockerfile: b.Dockerfile}
	}
	if s := a.Static; s != nil {
		r.Static = staticRaw{Dir: s.Dir, Fallback: s.Fallback}
	}

	// The two blocks a document's decoder leaves as nodes. Encode fails for
	// values YAML cannot say, which these are not.
	if p := a.Proxy; p != nil {
		pr := proxyRaw{Headers: p.Headers}
		if p.StripPrefix {
			pr.StripPrefix = &p.StripPrefix
		}
		for i := range sized(&pr.BasicAuth, len(p.BasicAuth)) {
			b := p.BasicAuth[i]
			pr.BasicAuth[i].Path, pr.BasicAuth[i].Username, pr.BasicAuth[i].Password = b.Path, b.Username, b.Password
		}
		for i := range sized(&pr.Redirects, len(p.Redirects)) {
			red := &p.Redirects[i]
			pr.Redirects[i].From, pr.Redirects[i].To, pr.Redirects[i].Status = red.From, red.To, &red.Status
		}
		r.Proxy.Encode(pr)
	}
	if b := a.Backups; b != nil {
		// Not backupsRaw: without `before` it would say an empty one, which is
		// a command that is missing.
		var beforeTimeout string
		if b.BeforeTimeout != 0 {
			beforeTimeout = b.BeforeTimeout.String()
		}
		r.Backups.Encode(struct {
			Schedule      string   `yaml:"schedule"`
			Keep          int      `yaml:"keep"`
			Before        []string `yaml:"before,omitempty"`
			BeforeTimeout string   `yaml:"before_timeout,omitempty"`
			Stop          bool     `yaml:"stop,omitempty"`
			BeforeIn      string   `yaml:"before_in,omitempty"`
		}{b.Schedule, b.Keep, b.Before, beforeTimeout, b.Stop, b.BeforeIn})
	}
	if a.Init {
		on := true
		r.Init = &on
	}

	r.Restart.Policy = a.Restart.Policy
	r.Deploy.Strategy = a.Deploy.Strategy
	if a.Deploy.StopTimeout != 0 {
		r.Deploy.StopTimeout = a.Deploy.StopTimeout.String()
	}
	return r
}

// alloc points *p at a new value and returns it, and sized makes *s a list of
// n: the blocks of raw are structs without a name.
func alloc[T any](p **T) *T {
	*p = new(T)
	return *p
}

func sized[T any](s *[]T, n int) []T {
	if n > 0 {
		*s = make([]T, n)
	}
	return *s
}
