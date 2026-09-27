package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/shipwick/shipwick/agent/internal/config"
	"github.com/shipwick/shipwick/pkg/spec"
)

// Environment values are the one secret a deployment record holds, and the
// database file travels: it is backed up, copied off the server, opened with
// sqlite3 by whoever debugs it. So they are encrypted before they are
// written, with a key kept outside the database, and everything else, the
// variable names included, stays readable.
//
// AES-256-GCM, a fresh random nonce per value, and the variable name as the
// additional data: a ciphertext cut from one variable and pasted into another
// does not decrypt there. Stored form: "enc1:" + base64(nonce || ciphertext).
// The prefix versions the scheme and tells a value written before encryption
// existed from one written after. A plaintext value that itself starts with
// "enc1:" cannot be told apart; that only matters to the one-time pass over
// rows written by earlier releases, and is accepted.
const encPrefix = "enc1:"

var errKeyMismatch = errors.New("the encryption key does not match the database")

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != config.EncryptionKeySize {
		return nil, fmt.Errorf("encryption key must be %d bytes, got %d", config.EncryptionKeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func isSealed(value string) bool {
	return strings.HasPrefix(value, encPrefix)
}

func seal(aead cipher.AEAD, name, value string) (string, error) {
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	out := aead.Seal(nonce, nonce, []byte(value), []byte(name))
	return encPrefix + base64.StdEncoding.EncodeToString(out), nil
}

// open reverses seal. It fails for a wrong key, a tampered value and a value
// sealed under another variable name alike; GCM cannot tell them apart.
func open(aead cipher.AEAD, name, stored string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, encPrefix))
	if err != nil || len(raw) < aead.NonceSize() {
		return "", fmt.Errorf("decrypt %s: malformed ciphertext", name)
	}
	n := aead.NonceSize()
	plain, err := aead.Open(nil, raw[:n], raw[n:], []byte(name))
	if err != nil {
		return "", fmt.Errorf("decrypt %s: %w", name, err)
	}
	return string(plain), nil
}

// sealSpec returns app with its environment values encrypted, ready to be
// stored. The caller's map is left alone: the engine still needs the values.
func (s *Store) sealSpec(app spec.App) (spec.App, error) {
	if s.aead == nil || len(app.Env) == 0 {
		return app, nil
	}
	env := make(map[string]string, len(app.Env))
	for name, value := range app.Env {
		sealed, err := seal(s.aead, name, value)
		if err != nil {
			return spec.App{}, err
		}
		env[name] = sealed
	}
	app.Env = env
	return app, nil
}

// openSpec decrypts, in place, the environment values of a spec read from
// the database.
func (s *Store) openSpec(app *spec.App) error {
	if s.aead == nil {
		return nil
	}
	for name, value := range app.Env {
		if !isSealed(value) {
			return fmt.Errorf("%s is stored unencrypted", name)
		}
		plain, err := open(s.aead, name, value)
		if err != nil {
			return err
		}
		app.Env[name] = plain
	}
	return nil
}

// encryptLegacyEnv rewrites the deployments written before environment values
// were encrypted, and reports how many. Once every value carries the prefix
// the pass finds nothing to rewrite; what it still does on every start is
// prove the key against the values that are encrypted, so a wrong key fails
// here, with a message that says what to do, and not at the first deployment.
//
// Only the env values change: the rest of each spec is kept as it was.
func (s *Store) encryptLegacyEnv(ctx context.Context, path string) (int, error) {
	if s.aead == nil {
		return 0, nil
	}
	var n int
	err := s.tx(ctx, func(tx *sql.Tx) error {
		type record struct {
			id   int64
			spec string
		}
		rows, err := tx.QueryContext(ctx, `SELECT id, spec FROM deployments ORDER BY id`)
		if err != nil {
			return err
		}
		var records []record
		for rows.Next() {
			var r record
			if err := rows.Scan(&r.id, &r.spec); err != nil {
				rows.Close()
				return err
			}
			records = append(records, r)
		}
		if err := rows.Close(); err != nil {
			return err
		}

		for _, r := range records {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(r.spec), &fields); err != nil {
				return fmt.Errorf("decode spec of deployment %d: %w", r.id, err)
			}
			rawEnv, ok := fields["env"]
			if !ok {
				continue
			}
			var env map[string]string
			if err := json.Unmarshal(rawEnv, &env); err != nil {
				return fmt.Errorf("decode spec of deployment %d: %w", r.id, err)
			}
			rewrite := false
			for name, value := range env {
				if isSealed(value) {
					if _, err := open(s.aead, name, value); err != nil {
						keyFile := filepath.Join(filepath.Dir(path), config.EncryptionKeyFile)
						return fmt.Errorf("%w (deployment %d, variable %s): restore %s from your backup or set %s", errKeyMismatch, r.id, name, keyFile, config.EnvEncryptionKey)
					}
					continue
				}
				if env[name], err = seal(s.aead, name, value); err != nil {
					return err
				}
				rewrite = true
			}
			if !rewrite {
				continue
			}
			if fields["env"], err = json.Marshal(env); err != nil {
				return err
			}
			specJSON, err := json.Marshal(fields)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE deployments SET spec = ? WHERE id = ?`, string(specJSON), r.id); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if errors.Is(err, errKeyMismatch) {
		return 0, err
	} else if err != nil {
		return 0, fmt.Errorf("encrypt environment values: %w", err)
	}
	return n, nil
}
