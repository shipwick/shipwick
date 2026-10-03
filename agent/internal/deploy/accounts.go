package deploy

import (
	"crypto/hmac"
	"crypto/rand"
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
	// secret keys the names accounts are cached under: see key. Made up when
	// first needed, never stored.
	secret []byte
}

// key names an account by what its hash depends on, without being the
// password: the cache must not become a second place where passwords are. A
// plain digest of a password can be tried against a word list at any speed;
// this one is keyed with a secret that dies with the process. Called with
// c.mu held.
func (c *hashCache) key(a spec.BasicAuth) (string, error) {
	if c.secret == nil {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return "", fmt.Errorf("generate a key for the account cache: %w", err)
		}
		c.secret = secret
	}
	mac := hmac.New(sha256.New, c.secret)
	mac.Write([]byte(a.Username + "\x00" + a.Password))
	return hex.EncodeToString(mac.Sum(nil)), nil
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
		key, err := c.key(a)
		if err != nil {
			return nil, err
		}
		hash, ok := c.hashes[key]
		if !ok {
			h, err := bcrypt.GenerateFromPassword([]byte(a.Password), hashCost)
			if err != nil {
				// Said in words of its own: nothing that was computed from
				// the password travels into an error, a log or an event.
				return nil, fmt.Errorf("account %q: its password cannot be hashed; bcrypt takes at most %d bytes", a.Username, spec.MaxPasswordBytes)
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
