package spec

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Mask is what stands in the place of a secret value in everything the agent
// hands out. It is never a value: a document that carries it where a value
// belongs is refused (MaskedFields), so that what was copied out of an answer
// cannot be deployed as if it were the configuration.
const Mask = redactedValue

// References are the secret values of a document that referred to secrets
// stored on the server, as they were written: the text of the value with its
// ${NAME} placeholders in place. The agent keeps them next to the deployment
// whose values it filled in, so that the document can be given back with the
// references where the values are (Document).
type References struct {
	// Env is by variable name.
	Env map[string]string `json:"env,omitempty"`
	// BasicAuth is by position in proxy.basic_auth, for the password.
	BasicAuth map[int]string `json:"basic_auth,omitempty"`
}

// Empty reports whether nothing was referred to.
func (r References) Empty() bool { return len(r.Env) == 0 && len(r.BasicAuth) == 0 }

// ReferencesOf collects the references of a document that has not had them
// filled in yet: every env value and basic-auth password with a placeholder
// in it.
func ReferencesOf(a App) References {
	var refs References
	for name, value := range a.Env {
		if hasPlaceholder(value) {
			if refs.Env == nil {
				refs.Env = map[string]string{}
			}
			refs.Env[name] = value
		}
	}
	if a.Proxy != nil {
		for i, account := range a.Proxy.BasicAuth {
			if hasPlaceholder(account.Password) {
				if refs.BasicAuth == nil {
					refs.BasicAuth = map[int]string{}
				}
				refs.BasicAuth[i] = account.Password
			}
		}
	}
	return refs
}

// For returns the references that still apply to a: those of a variable or an
// account it has. What is kept for one deployment may be handed to another
// whose spec is not the same, and a reference to what is gone says nothing.
// A text without a placeholder is not a reference and is dropped as well.
func (r References) For(a App) References {
	var out References
	for name, text := range r.Env {
		if _, ok := a.Env[name]; ok && hasPlaceholder(text) {
			if out.Env == nil {
				out.Env = map[string]string{}
			}
			out.Env[name] = text
		}
	}
	accounts := 0
	if a.Proxy != nil {
		accounts = len(a.Proxy.BasicAuth)
	}
	for i, text := range r.BasicAuth {
		if i >= 0 && i < accounts && hasPlaceholder(text) {
			if out.BasicAuth == nil {
				out.BasicAuth = map[int]string{}
			}
			out.BasicAuth[i] = text
		}
	}
	return out
}

// MaskedFields lists the fields of a whose value is the mask, in the order a
// report names them.
func MaskedFields(a App) []string {
	var fields []string
	for name, value := range a.Env {
		if value == Mask {
			fields = append(fields, "env."+name)
		}
	}
	sort.Strings(fields)
	if a.Proxy != nil {
		for i, account := range a.Proxy.BasicAuth {
			if account.Password == Mask {
				fields = append(fields, fmt.Sprintf("proxy.basic_auth[%d].password", i))
			}
		}
	}
	return fields
}

// Document writes a as the deploy.yaml that describes it, in the order and
// style `shipwick init` writes one. Parsed again, it is a.
//
// The secret values of a are never written. One that came from a reference
// is written as that reference, which the server fills in again when the
// document is deployed; any other is written as the mask, and masked names
// those fields: a deployment of the document is refused until each has been
// given a value or a reference.
//
// With escape, a literal ${NAME} outside the secret values is written
// $${NAME}, which is how a file read by the CLI says it: the CLI fills in
// placeholders everywhere, the agent only in the secret values.
func Document(a App, refs References, escape bool) (text string, masked []string, err error) {
	w := documentWriter{escape: escape}
	refs = refs.For(a)

	w.block(pair("name", w.str(a.Name)))

	var source []*yaml.Node
	switch {
	case a.Static != nil && a.Static.Fallback == "":
		source = pair("static", w.str(a.Static.Dir))
	case a.Static != nil:
		source = pair("static", mapping(pair("dir", w.str(a.Static.Dir)), pair("fallback", w.str(a.Static.Fallback))))
	case a.Build != nil && a.Build.Dockerfile == DefaultDockerfile:
		source = pair("build", w.str(a.Build.Context))
	case a.Build != nil:
		source = pair("build", mapping(pair("context", w.str(a.Build.Context)), pair("dockerfile", w.str(a.Build.Dockerfile))))
	}
	if a.Image != "" {
		source = append(source, pair("image", w.str(a.Image))...)
	}
	w.block(source)

	var process []*yaml.Node
	if len(a.Entrypoint) > 0 {
		process = append(process, pair("entrypoint", w.argv(a.Entrypoint))...)
	}
	if len(a.Command) > 0 {
		process = append(process, pair("command", w.argv(a.Command))...)
	}
	if a.User != "" {
		process = append(process, pair("user", w.str(a.User))...)
	}
	w.block(process)

	if a.Init {
		w.block(pair("init", boolean(true)))
	}
	if a.Port != 0 {
		w.block(pair("port", number(a.Port)))
	}

	var hostnames []*yaml.Node
	if a.Domain != "" {
		hostnames = append(hostnames, pair("domain", w.str(a.Domain))...)
	}
	if a.Path != "" {
		hostnames = append(hostnames, pair("path", w.str(a.Path))...)
	}
	if len(a.Aliases) > 0 {
		hostnames = append(hostnames, pair("aliases", w.list(a.Aliases))...)
	}
	if len(a.Redirects) > 0 {
		hostnames = append(hostnames, pair("redirects", w.list(a.Redirects))...)
	}
	w.block(hostnames)

	// A static application has no replicas to count and nothing to restart;
	// Parse gives it the defaults, and the file init writes for one names
	// neither.
	if a.Static == nil || a.Replicas != DefaultReplicas {
		w.block(pair("replicas", number(a.Replicas)))
	}

	if len(a.Env) > 0 {
		env := &yaml.Node{Kind: yaml.MappingNode}
		for _, name := range sortedKeys(a.Env) {
			value, isMasked := secret(refs.Env[name])
			if isMasked {
				masked = append(masked, "env."+name)
			}
			env.Content = append(env.Content, pair(name, value)...)
		}
		w.block(pair("env", env))
	}

	if h := a.Health; h != nil {
		var check []*yaml.Node
		switch h.Kind() {
		case HealthCommand:
			check = pair("command", w.argv(h.Command))
		case HealthTCP:
			check = pair("tcp", number(h.TCP))
		default:
			check = pair("path", w.str(h.Path))
		}
		check = append(check, pair("interval", duration(h.Interval))...)
		check = append(check, pair("timeout", duration(h.Timeout))...)
		check = append(check, pair("retries", number(h.Retries))...)
		if h.StartPeriod != 0 {
			check = append(check, pair("start_period", duration(h.StartPeriod))...)
		}
		w.block(pair("health", mapping(check)))
	}

	if a.Resources.CPU != 0 || a.Resources.MemoryBytes != 0 {
		var limits []*yaml.Node
		if a.Resources.CPU != 0 {
			limits = append(limits, pair("cpu", plain(strconv.FormatFloat(a.Resources.CPU, 'f', -1, 64)))...)
		}
		if a.Resources.MemoryBytes != 0 {
			limits = append(limits, pair("memory", plain(memoryText(a.Resources.MemoryBytes)))...)
		}
		w.block(pair("resources", mapping(limits)))
	}

	var storage []*yaml.Node
	if len(a.Volumes) > 0 {
		volumes := &yaml.Node{Kind: yaml.SequenceNode}
		for _, v := range a.Volumes {
			volumes.Content = append(volumes.Content, mapping(pair("name", w.str(v.Name)), pair("path", w.str(v.Path))))
		}
		storage = append(storage, pair("volumes", volumes)...)
	}
	if len(a.Publish) > 0 {
		publish := &yaml.Node{Kind: yaml.SequenceNode}
		for _, p := range a.Publish {
			entry := append(pair("port", number(p.Port)), pair("host", number(p.Host))...)
			if p.Address != "" {
				entry = append(entry, pair("address", w.str(p.Address))...)
			}
			entry = append(entry, pair("protocol", w.str(p.Protocol))...)
			publish.Content = append(publish.Content, mapping(entry))
		}
		storage = append(storage, pair("publish", publish)...)
	}
	if a.Deploy.Strategy != StrategyRolling || a.Deploy.StopTimeout != 0 {
		rollout := pair("strategy", w.str(a.Deploy.Strategy))
		if a.Deploy.StopTimeout != 0 {
			rollout = append(rollout, pair("stop_timeout", duration(a.Deploy.StopTimeout))...)
		}
		storage = append(storage, pair("deploy", mapping(rollout))...)
	}
	w.block(storage)

	if h := a.PreDeploy; h != nil {
		w.block(pair("pre_deploy", mapping(pair("command", w.argv(h.Command)), pair("timeout", duration(h.Timeout)))))
	}
	if len(a.Jobs) > 0 {
		jobs := &yaml.Node{Kind: yaml.SequenceNode}
		for _, j := range a.Jobs {
			jobs.Content = append(jobs.Content, mapping(pair("name", w.str(j.Name)), pair("schedule", quoted(j.Schedule)),
				pair("command", w.argv(j.Command)), pair("timeout", duration(j.Timeout))))
		}
		w.block(pair("jobs", jobs))
	}

	if l := a.Logging; l != nil {
		logging := pair("driver", w.str(l.Driver))
		if len(l.Options) > 0 {
			logging = append(logging, pair("options", w.strings(l.Options))...)
		}
		w.block(pair("logging", mapping(logging)))
	}

	if p := a.Proxy; p != nil {
		var proxy []*yaml.Node
		if p.StripPrefix {
			proxy = append(proxy, pair("strip_prefix", boolean(true))...)
		}
		if len(p.Headers) > 0 {
			proxy = append(proxy, pair("headers", w.strings(p.Headers))...)
		}
		if len(p.BasicAuth) > 0 {
			accounts := &yaml.Node{Kind: yaml.SequenceNode}
			for i, account := range p.BasicAuth {
				var entry []*yaml.Node
				if account.Path != "" {
					entry = append(entry, pair("path", w.str(account.Path))...)
				}
				entry = append(entry, pair("username", w.str(account.Username))...)
				password, isMasked := secret(refs.BasicAuth[i])
				if isMasked {
					masked = append(masked, fmt.Sprintf("proxy.basic_auth[%d].password", i))
				}
				entry = append(entry, pair("password", password)...)
				accounts.Content = append(accounts.Content, mapping(entry))
			}
			proxy = append(proxy, pair("basic_auth", accounts)...)
		}
		if len(p.Redirects) > 0 {
			redirects := &yaml.Node{Kind: yaml.SequenceNode}
			for _, red := range p.Redirects {
				redirects.Content = append(redirects.Content, mapping(pair("from", w.str(red.From)), pair("to", w.str(red.To)), pair("status", number(red.Status))))
			}
			proxy = append(proxy, pair("redirects", redirects)...)
		}
		w.block(pair("proxy", mapping(proxy)))
	}

	if b := a.Backups; b != nil {
		backups := append(pair("schedule", quoted(b.Schedule)), pair("keep", number(b.Keep))...)
		if len(b.Before) > 0 {
			backups = append(backups, pair("before", w.argv(b.Before))...)
			if b.BeforeTimeout != 0 {
				backups = append(backups, pair("before_timeout", duration(b.BeforeTimeout))...)
			}
			if b.BeforeIn != "" {
				backups = append(backups, pair("before_in", w.str(b.BeforeIn))...)
			}
		}
		if b.Stop {
			backups = append(backups, pair("stop", boolean(true))...)
		}
		w.block(pair("backups", mapping(backups)))
	}

	if a.Static == nil || a.Restart.Policy != RestartAlways {
		w.block(pair("restart", mapping(pair("policy", w.str(a.Restart.Policy)))))
	}

	if w.err != nil {
		return "", nil, w.err
	}
	if len(masked) > 0 {
		text = maskedNotice
	}
	return text + strings.Join(w.blocks, "\n"), masked, nil
}

// maskedNotice opens a document that holds masks.
const maskedNotice = `# A value shown as "` + Mask + `" was given when the application was deployed and
# is not handed out. Write it again, or store it on the server with
# shipwick secret set NAME and refer to it as ${NAME}. A deployment of this
# file is refused until every such value has been replaced.

`

// maskedComment stands next to every masked value.
const maskedComment = "# not handed out: write the value again, or refer to a secret as ${NAME}"

// secret is the node of a secret value: its reference, or the mask.
func secret(reference string) (node *yaml.Node, masked bool) {
	if reference != "" {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: reference}, false
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: Mask, Style: yaml.DoubleQuotedStyle, LineComment: maskedComment}, true
}

// documentWriter collects the blocks of a document: groups of top-level keys
// with an empty line between them, as init writes them.
type documentWriter struct {
	escape bool
	blocks []string
	err    error
}

// block writes one group of top-level keys; pairs are keys and values in turn.
func (w *documentWriter) block(pairs []*yaml.Node) {
	if len(pairs) == 0 || w.err != nil {
		return
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.MappingNode, Content: pairs}); err != nil {
		w.err = err
		return
	}
	if err := enc.Close(); err != nil {
		w.err = err
		return
	}
	w.blocks = append(w.blocks, buf.String())
}

// str is a string that is not a secret. The tag makes the encoder quote what
// would otherwise read as a number or a boolean.
func (w *documentWriter) str(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: w.literal(v)}
}

// literal is v as the document says it: see Document on escape.
func (w *documentWriter) literal(v string) string {
	if !w.escape {
		return v
	}
	return Placeholder.ReplaceAllStringFunc(v, func(match string) string { return "$" + match })
}

// argv is a command line, on one line: ["pg_isready", "-U", "postgres"].
func (w *documentWriter) argv(args []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, arg := range args {
		n.Content = append(n.Content, quoted(w.literal(arg)))
	}
	return n
}

func (w *documentWriter) list(values []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode}
	for _, v := range values {
		n.Content = append(n.Content, w.str(v))
	}
	return n
}

// strings is a map of strings, its keys in order.
func (w *documentWriter) strings(m map[string]string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode}
	for _, k := range sortedKeys(m) {
		n.Content = append(n.Content, pair(k, w.str(m[k]))...)
	}
	return n
}

func pair(key string, value *yaml.Node) []*yaml.Node {
	return []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value}
}

func mapping(pairs ...[]*yaml.Node) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode}
	for _, p := range pairs {
		n.Content = append(n.Content, p...)
	}
	return n
}

// plain is a value written as it is: a number, a size, a duration.
func plain(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: v} }

func quoted(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: yaml.DoubleQuotedStyle}
}

func number(n int) *yaml.Node { return plain(strconv.Itoa(n)) }

func boolean(b bool) *yaml.Node { return plain(strconv.FormatBool(b)) }

// duration writes 10s, 5m, 1h30m: what time.Duration prints, without the
// zero units it ends in.
func duration(d Duration) *yaml.Node {
	s := time.Duration(d).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return plain(s)
}

// memoryText is a size in the largest unit that says it exactly.
func memoryText(bytes int64) string {
	for _, unit := range []struct {
		name string
		size int64
	}{{"gb", 1 << 30}, {"mb", 1 << 20}, {"kb", 1 << 10}} {
		if bytes%unit.size == 0 {
			return strconv.FormatInt(bytes/unit.size, 10) + unit.name
		}
	}
	return strconv.FormatInt(bytes, 10)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// DocumentName reads the name of the application out of a deploy.yaml
// without judging the rest of it: what an endpoint that takes a document has
// to know before the document is read in earnest. ok is false for a document
// whose name Parse would not accept either.
func DocumentName(data []byte) (name string, ok bool) {
	var doc struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil || !namePattern.MatchString(doc.Name) {
		return "", false
	}
	return doc.Name, true
}
