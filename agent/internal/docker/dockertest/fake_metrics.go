package dockertest

import "time"

// now is the clock Stats reads with: the test's, when it set one, so that
// readings can be spaced out without waiting.
func (f *Fake) now() time.Time {
	if f.Clock != nil {
		return f.Clock()
	}
	return time.Now()
}
