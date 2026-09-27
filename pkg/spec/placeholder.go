package spec

import "regexp"

// Placeholder matches ${NAME}: a value deploy.yaml refers to but must not
// contain. The CLI fills placeholders in from its environment and --env-file
// before the file is sent; the ones in env values that it cannot fill in are
// left for the agent, which fills them in from the secrets stored on the
// server. Both sides recognize the same form, this one: a bare $NAME is left
// alone, and $${NAME} stands for a literal ${NAME}.
//
// The first group is "$" for the escaped form, the second is the name.
var Placeholder = regexp.MustCompile(`\$(\$?)\{([A-Za-z_][A-Za-z0-9_]*)\}`)
