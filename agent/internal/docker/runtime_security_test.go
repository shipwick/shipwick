package docker

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

func TestConfigureSecurityLeavesAContainerAloneUnlessAsked(t *testing.T) {
	host := &container.HostConfig{SecurityOpt: []string{"no-new-privileges:true"}}
	configureSecurity(ContainerSpec{}, host)
	configureSecurity(ContainerSpec{Security: &Security{}}, host)
	if host.ReadonlyRootfs || host.CapDrop != nil || host.CapAdd != nil || len(host.Mounts) != 0 {
		t.Errorf("nothing was asked for, nothing must be set: %+v", host)
	}
}

func TestConfigureSecurityOnlyTakesAway(t *testing.T) {
	volume := mount.Mount{Type: mount.TypeVolume, Source: "shipwick_api_data", Target: "/data"}
	host := &container.HostConfig{Mounts: []mount.Mount{volume}, SecurityOpt: []string{"no-new-privileges:true"}}
	configureSecurity(ContainerSpec{Security: &Security{
		ReadOnly:         true,
		Tmpfs:            []Tmpfs{{Path: "/tmp", SizeBytes: 64 << 20}},
		DropCapabilities: true,
		Capabilities:     []string{"CHOWN", "SETUID"},
	}}, host)

	if !host.ReadonlyRootfs {
		t.Error("the root filesystem is not read-only")
	}
	if !reflect.DeepEqual(host.CapDrop, []string{"ALL"}) || !reflect.DeepEqual(host.CapAdd, []string{"CHOWN", "SETUID"}) {
		t.Errorf("drop %v, add %v: want everything dropped and the two kept ones back", host.CapDrop, host.CapAdd)
	}
	if len(host.Mounts) != 2 || host.Mounts[0] != volume {
		t.Fatalf("mounts = %+v, want the volume and the tmpfs", host.Mounts)
	}
	tmp := host.Mounts[1]
	if tmp.Type != mount.TypeTmpfs || tmp.Target != "/tmp" || tmp.Source != "" || tmp.TmpfsOptions == nil ||
		tmp.TmpfsOptions.SizeBytes != 64<<20 || tmp.TmpfsOptions.Mode != 0o1777 || len(tmp.TmpfsOptions.Options) != 0 {
		t.Errorf("tmpfs = %+v (%+v): want a bounded tmpfs with the daemon's own mount flags", tmp, tmp.TmpfsOptions)
	}
	if host.Privileged || !reflect.DeepEqual(host.SecurityOpt, []string{"no-new-privileges:true"}) {
		t.Errorf("what every container has was changed: %+v", host)
	}

	none := &container.HostConfig{}
	configureSecurity(ContainerSpec{Security: &Security{DropCapabilities: true}}, none)
	if !reflect.DeepEqual(none.CapDrop, []string{"ALL"}) || len(none.CapAdd) != 0 {
		t.Errorf("capabilities none: drop %v, add %v", none.CapDrop, none.CapAdd)
	}
}

func TestNonRootHoldsDeployYamlsUserBeforeTheImages(t *testing.T) {
	for name, tc := range map[string]struct {
		user, imageUser string
		refused         bool
		says            string
	}{
		"an image without a user":             {"", "", true, "names no user"},
		"an image that says root":             {"", "root", true, `runs as user "root"`},
		"an image that says 0":                {"", "0:0", true, "id 0 is root"},
		"an image with a named user":          {"", "node", true, "/etc/passwd"},
		"an image with a numeric user":        {"", "1000", false, ""},
		"an image with a numeric user, group": {"", "101:101", false, ""},
		"a numeric user over a root image":    {"1000:1000", "", false, ""},
		"a numeric user over a named image":   {"1000", "node", false, ""},
		"root over a numeric image":           {"0", "1000", true, `refuses user "0"`},
		"a name over a numeric image":         {"app", "1000", true, `refuses user "app"`},
	} {
		err := NonRoot("api:1", tc.user, tc.imageUser)
		var root *RootError
		if errors.As(err, &root) != tc.refused {
			t.Errorf("%s: %v, want refused = %v", name, err, tc.refused)
			continue
		}
		if tc.refused && (!strings.Contains(err.Error(), tc.says) || !strings.Contains(err.Error(), "set user in deploy.yaml")) {
			t.Errorf("%s: %q, want it to say %q and what to set", name, err, tc.says)
		}
	}
}
