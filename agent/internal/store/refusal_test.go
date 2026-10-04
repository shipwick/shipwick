package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The installer tells an agent that cannot run here from one that is slow to
// start by this sentence in its output, and starts the release that ran
// before (scripts/install.sh, start_previous_release). Every release since
// 0.1.0 says it in these words; an agent that says it differently is an agent
// the installer of a later release leaves restarting forever.
func TestANewerSchemaIsRefusedInTheWordsTheInstallerReads(t *testing.T) {
	const sentence = "is newer than this agent supports"

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shipwick.db")
	s, err := Open(ctx, path, testOptions)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", len(migrations)+1)); err != nil {
		t.Fatalf("set a later schema version: %v", err)
	}
	s.Close()

	_, err = Open(ctx, path, testOptions)
	if err == nil || !strings.Contains(err.Error(), sentence) {
		t.Errorf("Open on a database one migration ahead = %v, want an error that says %q", err, sentence)
	}

	installer, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "install.sh"))
	if err != nil {
		t.Fatalf("read the installer: %v", err)
	}
	if !strings.Contains(string(installer), "grep -q '"+sentence+"'") {
		t.Errorf("scripts/install.sh no longer looks for %q in the agent's output", sentence)
	}
}
