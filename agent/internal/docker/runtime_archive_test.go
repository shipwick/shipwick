package docker

import (
	"archive/tar"
	"bytes"
	"io"
	"strings"
	"testing"
)

// archive builds a tar the way the daemon does for CopyFromContainer of
// /var/lib/data: every entry under "data/".
func archive(t *testing.T, entries []tar.Header, contents map[string]string) io.ReadCloser {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, hdr := range entries {
		body := contents[hdr.Name]
		hdr.Size = int64(len(body))
		if err := w.WriteHeader(&hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return io.NopCloser(&buf)
}

func TestExportedArchiveIsRelativeToTheMountPoint(t *testing.T) {
	long := "sub/" + strings.Repeat("n", 150) // longer than a ustar name: PAX or GNU headers must survive
	src := archive(t, []tar.Header{
		{Name: "data/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "data/a.txt", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "data/sub/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "data/sub/b.txt", Typeflag: tar.TypeReg, Mode: 0o600},
		{Name: "data/" + long, Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "data/hard", Typeflag: tar.TypeLink, Linkname: "data/a.txt"},
		{Name: "data/soft", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
	}, map[string]string{"data/a.txt": "alpha", "data/sub/b.txt": "beta", "data/" + long: "long"})

	got := map[string]tar.Header{}
	body := map[string]string{}
	r := tar.NewReader(rebaseArchive(src, "data"))
	for {
		hdr, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read rebased archive: %v", err)
		}
		data, _ := io.ReadAll(r)
		got[hdr.Name] = *hdr
		body[hdr.Name] = string(data)
	}

	if _, ok := got["data/"]; ok {
		t.Errorf("the mount point itself must not be an entry: %v", got)
	}
	for name, want := range map[string]string{"a.txt": "alpha", "sub/b.txt": "beta", long: "long"} {
		if body[name] != want {
			t.Errorf("%s = %q, want %q (entries: %v)", name, body[name], want, keys(got))
		}
	}
	if hdr, ok := got["sub/"]; !ok || hdr.Typeflag != tar.TypeDir || hdr.Mode != 0o755 {
		t.Errorf("directory entry lost or changed: %+v", got["sub/"])
	}
	if got["sub/b.txt"].Mode != 0o600 {
		t.Errorf("mode not preserved: %o", got["sub/b.txt"].Mode)
	}
	if got["hard"].Linkname != "a.txt" {
		t.Errorf("hard link target = %q, want a.txt", got["hard"].Linkname)
	}
	if got["soft"].Linkname != "/etc/passwd" {
		t.Errorf("symlink target changed to %q", got["soft"].Linkname)
	}
}

func TestExportRefusesEntriesOutsideTheMountPoint(t *testing.T) {
	src := archive(t, []tar.Header{
		{Name: "data/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "etc/shadow", Typeflag: tar.TypeReg, Mode: 0o600},
	}, nil)
	_, err := io.ReadAll(rebaseArchive(src, "data"))
	if err == nil || !strings.Contains(err.Error(), "outside data") {
		t.Fatalf("err = %v, want a refusal", err)
	}
}

func keys(m map[string]tar.Header) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
