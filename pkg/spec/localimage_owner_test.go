package spec

import (
	"errors"
	"strings"
	"testing"
)

func TestALocalImageIsTheApplicationsOwn(t *testing.T) {
	for name, image := range map[string]string{
		"its own":         LocalImagePrefix("my-api") + "20260927-153000-a1b2",
		"from a registry": "ghcr.io/company/other:1.0",
		"a short name":    "other:1.0",
	} {
		if err := ValidateLocalImage(image, "my-api"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	err := ValidateLocalImage("shipwick.local/other:20260927-153000-a1b2", "my-api")
	if err == nil || !strings.Contains(err.Error(), "built for other") || !strings.Contains(err.Error(), "shipwick deploy") {
		t.Errorf("another application's image: err = %v; it should name the owner and say to deploy from that project", err)
	}
	if err := ValidateLocalImage("shipwick.local/my-api-2:x", "my-api"); err == nil || !strings.Contains(err.Error(), "built for my-api-2") {
		t.Errorf("a name that merely starts with the application's: err = %v", err)
	}
}

func TestADeploymentOfAnotherApplicationsLocalImageIsRefused(t *testing.T) {
	_, err := Parse([]byte("name: my-api\nbuild: .\nimage: shipwick.local/other:20260927-153000-a1b2\n"))
	var verr *ValidationError
	if !errors.As(err, &verr) || len(verr.Fields) != 1 || verr.Fields[0].Field != "image" {
		t.Fatalf("err = %v, want one error on image", err)
	}
	if f := verr.Fields[0]; !strings.Contains(f.Message, "built for other") || !strings.Contains(f.Expected, LocalImagePrefix("my-api")) {
		t.Errorf("field = %+v", f)
	}
	// Without build as well: the rule is about whose image it is.
	if _, err := Parse([]byte("name: my-api\nimage: shipwick.local/other:20260927-153000-a1b2\n")); err == nil || !strings.Contains(err.Error(), "built for other") {
		t.Errorf("err = %v", err)
	}
	if _, err := Parse([]byte("name: my-api\nbuild: .\nimage: shipwick.local/my-api:20260927-153000-a1b2\n")); err != nil {
		t.Errorf("the application's own image: %v", err)
	}
	// A bad name is reported once, as the name's problem.
	_, err = Parse([]byte("name: Not_Valid\nimage: shipwick.local/other:1\n"))
	if !errors.As(err, &verr) || len(verr.Fields) != 1 || verr.Fields[0].Field != "name" {
		t.Errorf("err = %v, want the name's error alone", err)
	}
}
