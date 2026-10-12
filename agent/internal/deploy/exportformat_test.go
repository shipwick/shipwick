package deploy

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

// An export of a format this agent does not read is refused before anything
// of it is taken in, and the refusal says which version wrote it and what to
// do: upgrade the agent, or export with a matching version.
func TestAnExportOfANewerFormatIsRefusedInWords(t *testing.T) {
	b := newServer(t, otherKey)
	newer := strings.Replace(manifestBefore09, `"format":1`, `"format":2`, 1)
	newer = strings.Replace(newer, `"shipwick":"v0.8.1"`, `"shipwick":"v2.3.0"`, 1)
	_, err := b.engine.Import(context.Background(), bytes.NewReader(handMade(t, newer)), ImportOptions{})
	var invalid *InvalidExportError
	if !errors.As(err, &invalid) {
		t.Fatalf("Import: %v", err)
	}
	for _, want := range []string{"format 2", "written by Shipwick v2.3.0", "this agent reads format 1", "Upgrade the agent, or export with a matching version"} {
		if !strings.Contains(invalid.Reason, want) {
			t.Errorf("the refusal lacks %q: %s", want, invalid.Reason)
		}
	}
	status, ok := b.engine.ImportStatus()
	if !ok || status.Status != api.ImportFailed || len(status.Applications) != 0 {
		t.Errorf("nothing of the export may have been taken in: %+v, %v", status, ok)
	}
}
