package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// EncryptionKeyFile holds the key environment values are encrypted with
// before they are written to the database: 64 hexadecimal characters in the
// data directory. The database is only readable together with it.
const EncryptionKeyFile = "encryption.key"

// EncryptionKeySize is the key length in bytes: AES-256.
const EncryptionKeySize = 32

func (c Config) EncryptionKeyPath() string {
	return filepath.Join(c.DataDir, EncryptionKeyFile)
}

// ResolveEncryptionKey determines the key the store encrypts environment
// values with.
//
//  1. SHIPWICK_ENCRYPTION_KEY, when set, always wins.
//  2. Otherwise the key file in the data directory, written by a previous run.
//  3. Otherwise a key is generated and written there, mode 0600. `created`
//     reports that, so the caller can tell the operator to back the file up.
func ResolveEncryptionKey(cfg Config) (key []byte, created bool, err error) {
	if cfg.EncryptionKey != "" {
		key, err = decodeEncryptionKey(cfg.EncryptionKey)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", EnvEncryptionKey, err)
		}
		return key, false, nil
	}

	path := cfg.EncryptionKeyPath()
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		key, err = decodeEncryptionKey(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, false, fmt.Errorf("%s is corrupt (%v): restore it from your backup, or set %s to the key the database was written with", path, err, EnvEncryptionKey)
		}
		return key, false, nil
	case !errors.Is(err, fs.ErrNotExist):
		return nil, false, fmt.Errorf("read encryption key: %w", err)
	}

	key = make([]byte, EncryptionKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, false, fmt.Errorf("generate encryption key: %w", err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, false, fmt.Errorf("create data directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, false, fmt.Errorf("persist encryption key: %w", err)
	}
	return key, true, nil
}

func decodeEncryptionKey(s string) ([]byte, error) {
	key, err := hex.DecodeString(s)
	if err != nil || len(key) != EncryptionKeySize {
		return nil, fmt.Errorf("expected %d hexadecimal characters", 2*EncryptionKeySize)
	}
	return key, nil
}
