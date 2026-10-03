package deploy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	"golang.org/x/crypto/bcrypt"

	"github.com/shipwick/shipwick/agent/internal/proxy"
	"github.com/shipwick/shipwick/pkg/spec"
)

// The proxy checks basic-auth passwords against bcrypt hashes, and is given
// nothing else: the passwords stay in the deployment record, sealed. Hashing
// is slow on purpose, and routes are computed every second, so a hash is made
// once and kept for as long as a deployment that could be routed has the
// account. A new hash of the same password is a different string — the salt
// is random — and a different string is a reload of the proxy; keeping the
// hash is also what keeps the config still.
//
// The hashes are not stored. After a restart of the agent they are made
// again, which costs one reload of the proxy when an application has accounts.

// hashCost is bcrypt's work factor. The proxy pays it for the first request
// with a password, not for every one: it remembers what it has verified.
const hashCost = 10

// hashCache holds the hash of every account in use. The zero value is ready.
type hashCache struct {
	mu     sync.Mutex
	hashes map[string]string
}

// accountKey names an account by what its hash depends on, without being the
// password: the cache must not become a second place where passwords are.
func accountKey(a spec.BasicAuth) string {
	sum := sha256.Sum256([]byte(a.Username + "\x00" + a.Password))
	return hex.EncodeToString(sum[:])
}

// accounts turns the accounts of a proxy block into what the proxy is told,
// hashing the passwords it has not hashed before, and marks them in used.
func (c *hashCache) accounts(in []spec.BasicAuth, used map[string]bool) ([]proxy.BasicAuth, error) {
	if len(in) == 0 {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hashes == nil {
		c.hashes = map[string]string{}
	}
	out := make([]proxy.BasicAuth, 0, len(in))
	for _, a := range in {
		key := accountKey(a)
		hash, ok := c.hashes[key]
		if !ok {
			h, err := bcrypt.GenerateFromPassword([]byte(a.Password), hashCost)
			if err != nil {
				// The error names lengths at most, never the password.
				return nil, fmt.Errorf("account %q: %w", a.Username, err)
			}
			hash = string(h)
			c.hashes[key] = hash
		}
		used[key] = true
		out = append(out, proxy.BasicAuth{Path: a.Path, Username: a.Username, Hash: hash})
	}
	return out, nil
}

// keep forgets every hash that is not in used: the accounts of deployments
// that are no longer routed.
func (c *hashCache) keep(used map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.hashes {
		if !used[key] {
			delete(c.hashes, key)
		}
	}
}
