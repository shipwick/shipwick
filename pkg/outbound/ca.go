package outbound

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"
)

// maxCAFileBytes is far more than a bundle of authorities takes; a file
// beyond it is something else.
const maxCAFileBytes = 4 << 20

// LoadCAFile returns the system's certificate authorities with the ones in
// the PEM file at path added. Every way the file can be wrong gets a sentence
// of its own: a wrong file found at the first failed handshake reads as the
// other side's fault.
func LoadCAFile(path string) (*x509.CertPool, error) {
	return loadCAFile(path, time.Now())
}

func loadCAFile(path string, now time.Time) (*x509.CertPool, error) {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%s does not exist; for an agent in a container, mount the file there in compose.override.yml", path)
	case err != nil:
		return nil, fmt.Errorf("%s cannot be read: %w", path, cause(err))
	case info.IsDir():
		// What Docker leaves behind when the left side of a mount is missing.
		return nil, fmt.Errorf("%s is a directory, not a file; Docker creates one when the file it is told to mount does not exist on the server, so check the path left of the colon in compose.override.yml", path)
	case info.Size() > maxCAFileBytes:
		return nil, fmt.Errorf("%s is larger than %d MB, which no bundle of certificate authorities is", path, maxCAFileBytes>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s cannot be read: %w", path, cause(err))
	}

	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	var (
		added, expired int
		keys           bool
		lastExpiry     time.Time
	)
	for n := 1; ; {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		if strings.Contains(block.Type, "PRIVATE KEY") {
			keys = true
			continue
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%s: certificate %d cannot be read: %w", path, n, err)
		}
		// A server's own certificate, issued by somebody else, verifies
		// nothing: the mistake to catch is the leaf given for its authority.
		if !cert.IsCA && cert.Subject.String() != cert.Issuer.String() {
			return nil, fmt.Errorf("%s: certificate %d (%s) is a server's certificate, not an authority's; the file must hold the certificate of the authority that issued it (%s)",
				path, n, name(cert.Subject.CommonName, cert.Subject.String()), name(cert.Issuer.CommonName, cert.Issuer.String()))
		}
		n++
		if now.After(cert.NotAfter) {
			expired++
			if cert.NotAfter.After(lastExpiry) {
				lastExpiry = cert.NotAfter
			}
			continue
		}
		pool.AddCert(cert)
		added++
	}
	switch {
	case added > 0:
		return pool, nil
	case expired > 0:
		return nil, fmt.Errorf("%s: every certificate in it has expired, the last on %s", path, lastExpiry.UTC().Format("2006-01-02"))
	case keys:
		return nil, fmt.Errorf("%s holds a private key and no certificate; the file must hold the authority's certificate only", path)
	}
	return nil, fmt.Errorf("%s holds no certificate; expected PEM, one or more blocks that begin with -----BEGIN CERTIFICATE-----", path)
}

// cause is the error without the path the sentence around it already names.
func cause(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

func name(preferred, fallback string) string {
	if preferred != "" {
		return preferred
	}
	return fallback
}
