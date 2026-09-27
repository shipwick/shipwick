package config

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveEncryptionKeyGeneratesOnceAndReuses(t *testing.T) {
	cfg := Config{DataDir: filepath.Join(t.TempDir(), "data")}

	key, created, err := ResolveEncryptionKey(cfg)
	if err != nil {
		t.Fatalf("ResolveEncryptionKey: %v", err)
	}
	if !created || len(key) != EncryptionKeySize {
		t.Errorf("created = %v, len(key) = %d; want a fresh %d-byte key", created, len(key), EncryptionKeySize)
	}

	info, err := os.Stat(cfg.EncryptionKeyPath())
	if err != nil {
		t.Fatalf("key file: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %o, want 0600", info.Mode().Perm())
	}
	onDisk, _ := os.ReadFile(cfg.EncryptionKeyPath())
	if strings.TrimSpace(string(onDisk)) != hex.EncodeToString(key) {
		t.Error("the file must hold the key as hex")
	}

	again, created, err := ResolveEncryptionKey(cfg)
	if err != nil {
		t.Fatalf("second ResolveEncryptionKey: %v", err)
	}
	if created || !bytes.Equal(again, key) {
		t.Error("the persisted key should be reused on later starts")
	}
}

func TestResolveEncryptionKeyFromEnvWins(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	fileKey, _, err := ResolveEncryptionKey(cfg)
	if err != nil {
		t.Fatal(err)
	}

	configured := strings.Repeat("ab", EncryptionKeySize)
	cfg.EncryptionKey = configured
	key, created, err := ResolveEncryptionKey(cfg)
	if err != nil {
		t.Fatalf("ResolveEncryptionKey: %v", err)
	}
	if created || hex.EncodeToString(key) != configured || bytes.Equal(key, fileKey) {
		t.Error("a configured key must win over the file")
	}

	empty := Config{DataDir: filepath.Join(t.TempDir(), "data"), EncryptionKey: configured}
	if _, _, err := ResolveEncryptionKey(empty); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(empty.EncryptionKeyPath()); !os.IsNotExist(err) {
		t.Error("a configured key must not be persisted")
	}
}

func TestResolveEncryptionKeyRejectsACorruptFile(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	os.WriteFile(cfg.EncryptionKeyPath(), []byte("not-hex\n"), 0o600)
	_, _, err := ResolveEncryptionKey(cfg)
	if err == nil {
		t.Fatal("expected an error for a corrupt key file")
	}
	if !strings.Contains(err.Error(), "backup") || !strings.Contains(err.Error(), EnvEncryptionKey) {
		t.Errorf("the error should say what to do next: %v", err)
	}
}

func TestLoadValidatesTheEncryptionKey(t *testing.T) {
	good := strings.Repeat("0f", EncryptionKeySize)
	cfg, err := Load(env(map[string]string{EnvEncryptionKey: " " + good + "\n"}))
	if err != nil || cfg.EncryptionKey != good {
		t.Errorf("a 64-character hex key should be accepted: %v", err)
	}

	for name, bad := range map[string]string{
		"not hex":   strings.Repeat("zz", EncryptionKeySize),
		"too short": strings.Repeat("0f", EncryptionKeySize-1),
		"too long":  strings.Repeat("0f", EncryptionKeySize+1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(env(map[string]string{EnvEncryptionKey: bad}))
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), bad) {
				t.Errorf("the error must not echo the key: %v", err)
			}
		})
	}
}
