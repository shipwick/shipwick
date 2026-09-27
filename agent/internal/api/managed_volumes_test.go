package api

import (
	"net/http"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestManagedVolumesListEveryVolumeAndSayWhoseItIs(t *testing.T) {
	f := newFixture(t)
	f.deployStateful(map[string]string{"base/1": "0123456789", "pg_wal/2": "0123456789"})

	status, body := f.do("GET", "/api/v1/volumes", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	volumes := decode[[]api.VolumeInfo](t, body)
	if len(volumes) != 1 {
		t.Fatalf("volumes = %+v, want the one the deployment created", volumes)
	}
	v := volumes[0]
	if v.Name != "shipwick_db_data" || v.Application != "db" || v.Volume != "data" || v.SizeBytes != 20 || v.Orphan {
		t.Errorf("volume = %+v, want shipwick_db_data of db, 20 bytes, in use", v)
	}

	if status, body := f.do("DELETE", "/api/v1/volumes/shipwick_db_data", ""); status != http.StatusConflict {
		t.Fatalf("removing a volume of an existing application: status = %d, want 409: %s", status, body)
	} else if e := decodeError(t, body); e.Code != api.CodeVolumeInUse || e.Details["application"] != "db" {
		t.Errorf("error = %+v, want VOLUME_IN_USE naming db", e)
	}

	if status, _ := f.do("DELETE", "/api/v1/applications/db", ""); status != http.StatusNoContent {
		t.Fatalf("delete: status = %d", status)
	}
	_, body = f.do("GET", "/api/v1/volumes", "")
	volumes = decode[[]api.VolumeInfo](t, body)
	if len(volumes) != 1 || !volumes[0].Orphan || volumes[0].Application != "db" {
		t.Fatalf("after delete: %+v, want the same volume, now an orphan: data outlives the application on purpose", volumes)
	}

	if status, body := f.do("DELETE", "/api/v1/volumes/shipwick_db_data", ""); status != http.StatusNoContent {
		t.Fatalf("removing the orphan: status = %d: %s", status, body)
	}
	_, body = f.do("GET", "/api/v1/volumes", "")
	if volumes = decode[[]api.VolumeInfo](t, body); len(volumes) != 0 {
		t.Errorf("after removal: %+v, want none", volumes)
	}
	if removed := f.rt.RemovedVolumes(); len(removed) != 1 || removed[0] != "shipwick_db_data" {
		t.Errorf("removed volumes = %v", removed)
	}
}

func TestRemovingAVolumeValidatesItsName(t *testing.T) {
	f := newFixture(t)
	for name, want := range map[string]int{
		"shipwick_nope_data": http.StatusNotFound,   // well-formed, unknown
		"data":               http.StatusBadRequest, // not a Shipwick volume
		"shipwick_db":        http.StatusBadRequest, // no volume part
		"shipwick_D%20b_x":   http.StatusBadRequest, // not an application name
	} {
		status, body := f.do("DELETE", "/api/v1/volumes/"+name, "")
		if status != want {
			t.Errorf("DELETE %s: status = %d, want %d: %s", name, status, want, body)
		}
	}
	if removed := f.rt.RemovedVolumes(); len(removed) != 0 {
		t.Errorf("volumes removed by malformed requests: %v", removed)
	}
}

func TestManagedVolumesNeedTheReadRoleAndRemovalNeedsAdmin(t *testing.T) {
	f := newFixture(t)
	deployer := f.createToken("ci", api.RoleDeploy).Token
	if status, _ := f.doWithAuth("GET", "/api/v1/volumes", "", "Bearer "+deployer); status != http.StatusOK {
		t.Errorf("list with a deploy token: status = %d, want 200", status)
	}
	if status, body := f.doWithAuth("DELETE", "/api/v1/volumes/shipwick_db_data", "", "Bearer "+deployer); status != http.StatusForbidden {
		t.Errorf("remove with a deploy token: status = %d, want 403: %s", status, body)
	}
}
