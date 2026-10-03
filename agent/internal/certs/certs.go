// Package certs checks the certificates an operator supplies for a hostname
// before the agent stores them and the proxy serves them.
package certs

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"time"
)

// Info is what a certificate chain says about itself: everything the API
// shows of it.
type Info struct {
	// Subjects are the DNS names of the first certificate, lower case.
	Subjects  []string
	Issuer    string
	NotBefore time.Time
	NotAfter  time.Time
}

// Error is a certificate or key the agent refuses, with the reason as a
// sentence for the person who supplied it. It never quotes the input.
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

func refuse(format string, args ...any) error {
	return &Error{Reason: fmt.Sprintf(format, args...)}
}

// Check decides whether certPEM and keyPEM can serve hostname at the moment
// now: the chain parses, the key belongs to its first certificate, that
// certificate covers hostname and is within its validity.
func Check(hostname, certPEM, keyPEM string, now time.Time) (Info, error) {
	leaf, err := parseChain(certPEM)
	if err != nil {
		return Info{}, err
	}
	if err := checkKey(certPEM, keyPEM); err != nil {
		return Info{}, err
	}
	info := describe(leaf)
	switch {
	case len(info.Subjects) == 0:
		return Info{}, refuse("the certificate names no hostname: it has no DNS names (subject alternative names), and browsers accept nothing else")
	case !Covers(info.Subjects, hostname):
		return Info{}, refuse("the certificate does not cover %s: it is for %s", hostname, strings.Join(info.Subjects, ", "))
	case now.After(leaf.NotAfter):
		return Info{}, refuse("the certificate expired on %s", leaf.NotAfter.UTC().Format("2006-01-02"))
	case now.Before(leaf.NotBefore):
		return Info{}, refuse("the certificate is not valid before %s", leaf.NotBefore.UTC().Format("2006-01-02"))
	}
	return info, nil
}

// Describe reads what a stored chain says about itself.
func Describe(certPEM string) (Info, error) {
	leaf, err := parseChain(certPEM)
	if err != nil {
		return Info{}, err
	}
	return describe(leaf), nil
}

// Covers reports whether a certificate for subjects can be served for host. A
// wildcard subject stands for exactly one label, and a wildcard host is
// covered only by the same wildcard: *.example.com is more than the names a
// certificate for a.example.com and b.example.com lists.
func Covers(subjects []string, host string) bool {
	for _, s := range subjects {
		if s == host {
			return true
		}
		if strings.HasPrefix(host, "*.") {
			continue
		}
		if rest, ok := strings.CutPrefix(s, "*."); ok {
			if _, parent, found := strings.Cut(host, "."); found && parent == rest {
				return true
			}
		}
	}
	return false
}

// parseChain returns the first certificate of a PEM chain in which every
// block is a certificate.
func parseChain(certPEM string) (*x509.Certificate, error) {
	var leaf *x509.Certificate
	rest := []byte(certPEM)
	for n := 1; ; n++ {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			if strings.Contains(block.Type, "PRIVATE KEY") {
				return nil, refuse("the certificate file contains a private key: give the key separately, and only certificates here")
			}
			return nil, refuse("the certificate file contains a %q block: only certificates belong in it", block.Type)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, refuse("certificate %d in the chain cannot be read: %s", n, err)
		}
		if leaf == nil {
			leaf = cert
		}
	}
	if leaf == nil {
		return nil, refuse("the certificate is not PEM: expected one or more -----BEGIN CERTIFICATE----- blocks, the server's own first")
	}
	if len(bytes.TrimSpace(rest)) > 0 {
		return nil, refuse("the certificate file has text after its last certificate that is not PEM")
	}
	return leaf, nil
}

func checkKey(certPEM, keyPEM string) error {
	block, _ := pem.Decode([]byte(keyPEM))
	switch {
	case block == nil:
		return refuse("the key is not PEM: expected a -----BEGIN PRIVATE KEY----- block")
	case strings.Contains(block.Type, "ENCRYPTED") || block.Headers["Proc-Type"] != "":
		return refuse("the key is protected by a passphrase, which the proxy cannot enter: remove it with openssl pkey -in <key> -out privkey.pem")
	case !strings.Contains(block.Type, "PRIVATE KEY"):
		return refuse("the key file contains a %q block, not a private key", block.Type)
	}
	// The same check the proxy makes when it loads the pair.
	if _, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM)); err != nil {
		if strings.Contains(err.Error(), "does not match") {
			return refuse("the key does not belong to the first certificate of the chain: the server's own certificate comes first, the intermediates after it")
		}
		return refuse("the key cannot be used: %s", strings.TrimPrefix(err.Error(), "tls: "))
	}
	return nil
}

func describe(leaf *x509.Certificate) Info {
	info := Info{NotBefore: leaf.NotBefore.UTC(), NotAfter: leaf.NotAfter.UTC()}
	for _, name := range leaf.DNSNames {
		info.Subjects = append(info.Subjects, strings.ToLower(name))
	}
	switch issuer := leaf.Issuer; {
	case issuer.CommonName != "":
		info.Issuer = issuer.CommonName
	case len(issuer.Organization) > 0:
		info.Issuer = issuer.Organization[0]
	default:
		info.Issuer = issuer.String()
	}
	return info
}
