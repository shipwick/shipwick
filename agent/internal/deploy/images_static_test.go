package deploy

import (
	"context"
	"testing"
)

func TestDeletingAStaticApplicationAsksTheDaemonForNoImage(t *testing.T) {
	s, _ := newStaticHarness(t)
	s.deployStatic(site("web"), map[string]string{"index.html": "v1"})
	s.deployStatic(site("web"), map[string]string{"index.html": "v2"})
	s.deployStatic(site("web"), map[string]string{"index.html": "v3"})

	if err := s.engine.Delete(context.Background(), "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n := s.rt.NamelessRemovals(); n != 0 {
		t.Errorf("the daemon was asked %d times to remove an image without a name: a static application has none", n)
	}
}
