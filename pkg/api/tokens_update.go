package api

import "time"

// UpdateTokenRequest is the body of PUT /tokens/{name}. What it leaves out
// stays as it is; the role is not among what can be changed, because a token
// that may suddenly do more is a new token.
type UpdateTokenRequest struct {
	// Applications replaces the applications a deploy token is limited to.
	// An empty list lifts the limit.
	Applications *[]string `json:"applications,omitempty"`
	// ExpiresAt moves the token's end. A token that has expired works again
	// when its end is moved into the future.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// NeverExpires takes the end away. It cannot stand next to ExpiresAt.
	NeverExpires bool `json:"never_expires,omitempty"`
}

// Empty reports whether the request asks for nothing.
func (r UpdateTokenRequest) Empty() bool {
	return r.Applications == nil && r.ExpiresAt == nil && !r.NeverExpires
}
