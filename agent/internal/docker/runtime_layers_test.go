package docker

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// The daemon's answers below are those of Docker 29 to an archive that left
// out layers the daemon did not have, one per image store, and to one that
// left out only layers it had.

func TestALoadedImageIsReportedByItsReference(t *testing.T) {
	answer := `{"status":"Loading layer","progressDetail":{"current":153,"total":153},"id":"2e1fe0cd7c9f"}
{"stream":"Loaded image: shipwick.local/my-api:20260927-153000-a1b2\n"}
`
	refs, err := loadedImages(strings.NewReader(answer))
	if err != nil || !slices.Equal(refs, []string{"shipwick.local/my-api:20260927-153000-a1b2"}) {
		t.Errorf("refs = %v, %v", refs, err)
	}
}

func TestAnImageTheContainerdStoreCouldNotUnpackIsNotLoaded(t *testing.T) {
	// Not an error to the daemon: the image is recorded, tagged, and cannot
	// start a container.
	answer := `{"stream":"Loaded image: shipwick.local/my-api:20260927-153000-a1b2\n"}
{"stream":"Error unpacking image shipwick.local/my-api:20260927-153000-a1b2: apply layer error for \"shipwick.local/my-api:20260927-153000-a1b2\": failed to extract layer sha256:74d97c428c51a828f9051a7a40a53ff1fc99e54fc30323ce36760701b0b7f711: NotFound: failed to get reader from content store: content digest sha256:e2de96513ba9eb53b431787ec8a65cdde380ac4772a3e4c4b714dcfde2a102b5: not found\n"}
`
	refs, err := loadedImages(strings.NewReader(answer))
	if !errors.Is(err, ErrImageIncomplete) {
		t.Fatalf("err = %v, want ErrImageIncomplete", err)
	}
	if !slices.Equal(refs, []string{"shipwick.local/my-api:20260927-153000-a1b2"}) {
		t.Errorf("refs = %v: the tag the daemon left behind must be known, to be removed", refs)
	}
}

func TestAnArchiveTheClassicStoreFindsALayerMissingFromIsIncomplete(t *testing.T) {
	answer := `{"errorDetail":{"message":"open /var/lib/docker/tmp/docker-import-3561347790/blobs/sha256/e2de96513ba9eb53b431787ec8a65cdde380ac4772a3e4c4b714dcfde2a102b5: no such file or directory"},"error":"open /var/lib/docker/tmp/docker-import-3561347790/blobs/sha256/e2de96513ba9eb53b431787ec8a65cdde380ac4772a3e4c4b714dcfde2a102b5: no such file or directory"}
`
	refs, err := loadedImages(strings.NewReader(answer))
	if !errors.Is(err, ErrImageIncomplete) || len(refs) != 0 {
		t.Errorf("refs = %v, err = %v, want ErrImageIncomplete", refs, err)
	}
}

func TestAnyOtherRefusalOfAnArchiveIsReportedInTheDaemonsWords(t *testing.T) {
	refs, err := loadedImages(strings.NewReader(`{"error":"archive/tar: invalid tar header"}` + "\n"))
	if err == nil || errors.Is(err, ErrImageIncomplete) || !strings.Contains(err.Error(), "invalid tar header") || len(refs) != 0 {
		t.Errorf("refs = %v, err = %v", refs, err)
	}
}
