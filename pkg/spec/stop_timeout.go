package spec

import "time"

// The bounds of deploy.stop_timeout. Docker counts the grace period in whole
// seconds, so less than one would be none at all; and a replica that has not
// finished after ten minutes is not going to.
const (
	MinStopTimeout = time.Second
	MaxStopTimeout = 10 * time.Minute
)
