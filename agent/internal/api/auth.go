package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

type principalKey struct{}

// principalFrom returns the token a request was authenticated with.
func principalFrom(ctx context.Context) api.TokenIdentity {
	who, _ := ctx.Value(principalKey{}).(api.TokenIdentity)
	return who
}

// identify resolves the hash of a presented token to the token it is. The
// root token comes first: it is not in the database, and it is compared in
// constant time as it always was. Stored tokens are found by their hash — the
// column is unique, so this is one indexed lookup — and the row's hash is
// compared in constant time as well, so the lookup cannot be turned into an
// oracle by whatever the database does with near-equal keys.
func (s *Server) identify(ctx context.Context, presented [sha256.Size]byte) (api.TokenIdentity, bool) {
	if subtle.ConstantTimeCompare(presented[:], s.tokenHash[:]) == 1 {
		return api.TokenIdentity{Name: api.RootTokenName, Role: api.RoleAdmin}, true
	}
	t, err := s.store.GetTokenByHash(ctx, presented[:])
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("could not look up the API token", "error", err)
		}
		return api.TokenIdentity{}, false
	}
	if subtle.ConstantTimeCompare(t.Hash, presented[:]) != 1 {
		return api.TokenIdentity{}, false
	}
	s.recordUse(ctx, t)
	return api.TokenIdentity{Name: t.Name, Role: t.Role}, true
}

// lastUsedResolution is how often a token's use is written down at most.
// last_used_at answers "is anything still using this token?", for which the
// minute is plenty; writing it on every request would turn each poll of a
// dashboard into a database write.
const lastUsedResolution = time.Minute

func (s *Server) recordUse(ctx context.Context, t store.Token) {
	now := s.now()
	s.usedMu.Lock()
	last, seen := s.lastUsed[t.ID]
	if !seen && t.LastUsedAt != nil {
		// First sighting since the agent started: what the database
		// remembers counts, or every restart would cost one write per token.
		last, seen = *t.LastUsedAt, true
	}
	if seen && now.Sub(last) < lastUsedResolution {
		s.usedMu.Unlock()
		return
	}
	s.lastUsed[t.ID] = now
	s.usedMu.Unlock()

	if err := s.store.TouchToken(context.WithoutCancel(ctx), t.ID, now); err != nil {
		s.log.Warn("could not record token use", "token", t.Name, "error", err)
	}
}

// forbiddenMessage tells a caller which role they have and which one the
// operation wants, in the words the CLI and the dashboard show.
func forbiddenMessage(have, required api.Role) string {
	need := "this needs " + string(required)
	if required == api.RoleDeploy {
		need = "deploying needs deploy or admin"
	}
	return fmt.Sprintf("this token has the %s role; %s", have, need)
}
