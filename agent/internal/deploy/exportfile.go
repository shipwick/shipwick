package deploy

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/shipwick/shipwick/pkg/backupfile"
	"github.com/shipwick/shipwick/pkg/spec"
)

// An export is one tar archive, read from its first byte to its last and
// never sought in, because it is written to a stream and read from one:
//
//	export.json                                  what the server holds besides applications
//	applications/<name>/app.json                 one application: its configuration, in clear
//	applications/<name>/image.tar.0000 …         its image, when it was built by shipwick deploy
//	applications/<name>/static.tar.0000 …        its folder, when it is a static application
//	applications/<name>/volumes/<volume>.tar.0000 …
//
// Applications follow each other in the order they are to be deployed, each
// with everything that belongs to it, so that an import can deploy one before
// it has read the next. A tar entry states its size before its content, and a
// volume or an image is a stream of unknown length; such a member is therefore
// cut into parts of exportPartSize, numbered from 0000, and is the
// concatenation of its parts. The archive holds every secret of the server
// and exists only inside pkg/backupfile's encryption.

const (
	exportFormat   = 1
	exportPartSize = 4 << 20
	// exportJSONLimit bounds a JSON member on the way in.
	exportJSONLimit = 16 << 20

	exportManifestName = "export.json"
	exportAppPrefix    = "applications/"
	exportAppFile      = "/app.json"
)

// exportManifest is export.json.
type exportManifest struct {
	Format    int       `json:"format"`
	CreatedAt time.Time `json:"created_at"`
	// Shipwick is the version of the agent that wrote the export.
	Shipwick     string              `json:"shipwick"`
	Secrets      []exportSecret      `json:"secrets"`
	Registries   []exportRegistry    `json:"registries"`
	Certificates []exportCertificate `json:"certificates"`
	// Applications names what follows, in order.
	Applications []string `json:"applications"`
}

type exportSecret struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type exportRegistry struct {
	Registry string `json:"registry"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type exportCertificate struct {
	Hostname    string `json:"hostname"`
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
}

// exportApp is applications/<name>/app.json: the active deployment's
// configuration with its values in clear, and which members follow it.
type exportApp struct {
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Spec    spec.App `json:"spec"`
	// Stopped: the application was stopped when it was exported.
	Stopped bool `json:"stopped"`
	// Image is set for an image that exists on the exporting server only.
	Image *exportImage `json:"image,omitempty"`
	// Static is set for a static application; its folder follows.
	Static bool `json:"static,omitempty"`
	// Volumes are the volumes whose archives follow, in this order.
	Volumes []string `json:"volumes"`
	// References are the secret values of Spec that its document wrote as
	// references to stored secrets (config.go). Absent when there are none,
	// and in an export written before they were kept.
	References *spec.References `json:"references,omitempty"`
}

type exportImage struct {
	// Included: the image follows. Otherwise Reason says why the server
	// could not write it out.
	Included bool   `json:"included"`
	Reason   string `json:"reason,omitempty"`
}

func exportAppBase(name string) string      { return exportAppPrefix + name + "/" }
func exportImageMember(name string) string  { return exportAppBase(name) + "image.tar" }
func exportStaticMember(name string) string { return exportAppBase(name) + "static.tar" }
func exportVolumeMember(name, volume string) string {
	return exportAppBase(name) + "volumes/" + volume + ".tar"
}

// exportAppName is the application an app.json entry belongs to.
func exportAppName(entry string) (string, bool) {
	rest, ok := strings.CutPrefix(entry, exportAppPrefix)
	if !ok {
		return "", false
	}
	name, ok := strings.CutSuffix(rest, exportAppFile)
	return name, ok && name != "" && !strings.Contains(name, "/")
}

// exportWriter writes the members of an export.
type exportWriter struct {
	tw  *tar.Writer
	now time.Time
	buf []byte
}

func newExportWriter(w io.Writer, now time.Time) *exportWriter {
	return &exportWriter{tw: tar.NewWriter(w), now: now}
}

func (w *exportWriter) entry(name string, content []byte) error {
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), ModTime: w.now, Typeflag: tar.TypeReg}
	if err := w.tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := w.tw.Write(content)
	return err
}

func (w *exportWriter) json(name string, v any) error {
	content, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return w.entry(name, content)
}

// stream writes r as the member name, in parts, and returns how many bytes
// it held. A member has at least one part, so that an empty one is told from
// one that is missing.
func (w *exportWriter) stream(name string, r io.Reader) (int64, error) {
	if w.buf == nil {
		w.buf = make([]byte, exportPartSize)
	}
	var total int64
	for part := 0; ; part++ {
		n, err := io.ReadFull(r, w.buf)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return total, err
		}
		if n > 0 || part == 0 {
			if werr := w.entry(fmt.Sprintf("%s.%04d", name, part), w.buf[:n]); werr != nil {
				return total, werr
			}
			total += int64(n)
		}
		if err != nil {
			return total, nil
		}
	}
}

func (w *exportWriter) close() error { return w.tw.Close() }

// InvalidExportError means what was given to an import is not an export this
// agent can read.
type InvalidExportError struct{ Reason string }

func (e *InvalidExportError) Error() string { return e.Reason }

func damagedExport(format string, args ...any) error {
	return &InvalidExportError{Reason: "the export is damaged or incomplete: " + fmt.Sprintf(format, args...)}
}

// exportReader reads the members of an export in the order they were
// written. It looks one entry ahead, which is how a member's last part and
// an application's last member are recognised.
type exportReader struct {
	tr   *tar.Reader
	next *tar.Header // read from the archive, its content not yet
	end  bool
}

func newExportReader(r io.Reader) *exportReader {
	return &exportReader{tr: tar.NewReader(r)}
}

// peek returns the entry that comes next, or io.EOF at the end of the archive.
func (r *exportReader) peek() (*tar.Header, error) {
	if r.next != nil {
		return r.next, nil
	}
	if r.end {
		return nil, io.EOF
	}
	hdr, err := r.tr.Next()
	if errors.Is(err, io.EOF) {
		r.end = true
		return nil, io.EOF
	}
	if err != nil {
		return nil, readExportError(err)
	}
	r.next = hdr
	return hdr, nil
}

// readExportError turns what reading an archive ran into into what to tell
// its owner. A wrong passphrase shows at the first read.
func readExportError(err error) error {
	var invalid *InvalidExportError
	switch {
	case errors.As(err, &invalid):
		return err
	case errors.Is(err, backupfile.ErrPassphrase):
		return &InvalidExportError{Reason: "the passphrase does not match this export, or the file is damaged"}
	case errors.Is(err, backupfile.ErrCorrupt), errors.Is(err, tar.ErrHeader), errors.Is(err, io.ErrUnexpectedEOF):
		return damagedExport("it ends or breaks off in the middle")
	}
	return err
}

// json decodes the next entry, which must be the member name.
func (r *exportReader) json(name string, v any) error {
	hdr, err := r.peek()
	if errors.Is(err, io.EOF) || (err == nil && hdr.Name != name) {
		return damagedExport("%s is not where it belongs", name)
	} else if err != nil {
		return err
	}
	if hdr.Size > exportJSONLimit {
		return damagedExport("%s is larger than an export writes it", name)
	}
	r.next = nil
	content, err := io.ReadAll(r.tr)
	if err != nil {
		return readExportError(err)
	}
	if err := json.Unmarshal(content, v); err != nil {
		return damagedExport("%s does not read as JSON", name)
	}
	return nil
}

// stream returns the member name, which must come next. The reader must be
// read to its end, or drained, before anything else is asked of r.
func (r *exportReader) stream(name string) (*exportMember, error) {
	hdr, err := r.peek()
	if errors.Is(err, io.EOF) || (err == nil && hdr.Name != name+".0000") {
		return nil, damagedExport("%s is not where it belongs", name)
	} else if err != nil {
		return nil, err
	}
	return &exportMember{r: r, prefix: name + "."}, nil
}

// skipApplication discards what is left of the application being read: every
// entry up to the next app.json.
func (r *exportReader) skipApplication() error {
	for {
		hdr, err := r.peek()
		if errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		if _, ok := exportAppName(hdr.Name); ok {
			return nil
		}
		r.next = nil
	}
}

// exportMember reads the parts of one member as one stream.
type exportMember struct {
	r      *exportReader
	prefix string
	open   bool // a part's content is being read
	done   bool
}

func (m *exportMember) Read(p []byte) (int, error) {
	for {
		if m.done {
			return 0, io.EOF
		}
		if !m.open {
			hdr, err := m.r.peek()
			if errors.Is(err, io.EOF) || (err == nil && !strings.HasPrefix(hdr.Name, m.prefix)) {
				m.done = true
				return 0, io.EOF
			} else if err != nil {
				return 0, err
			}
			m.r.next = nil
			m.open = true
		}
		n, err := m.r.tr.Read(p)
		if errors.Is(err, io.EOF) {
			m.open = false
			if n > 0 {
				return n, nil
			}
			continue
		}
		if err != nil {
			return n, readExportError(err)
		}
		return n, nil
	}
}

// drain reads a member to its end, for a consumer that stopped early.
func (m *exportMember) drain() error {
	_, err := io.Copy(io.Discard, m)
	return err
}
