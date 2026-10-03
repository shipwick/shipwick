package certs

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/certs/certstest"
)

var now = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func TestCheckAcceptsACertificateThatCoversTheHostname(t *testing.T) {
	pair := certstest.Issue(now.AddDate(0, 3, 0), "Example.com", "*.example.com")
	for _, hostname := range []string{"example.com", "api.example.com", "*.example.com"} {
		info, err := Check(hostname, pair.Cert, pair.Key, now)
		if err != nil {
			t.Fatalf("Check(%s): %v", hostname, err)
		}
		if strings.Join(info.Subjects, ",") != "example.com,*.example.com" {
			t.Errorf("Subjects = %v, want the certificate's DNS names in lower case", info.Subjects)
		}
		if info.Issuer != "Shipwick Test Authority" || !info.NotAfter.Equal(now.AddDate(0, 3, 0)) || !info.NotBefore.Before(now) {
			t.Errorf("info = %+v", info)
		}
	}
}

func TestCheckAcceptsAChainAndDescribesItsFirstCertificate(t *testing.T) {
	leaf := certstest.Issue(now.AddDate(0, 3, 0), "example.com")
	other := certstest.Issue(now.AddDate(5, 0, 0), "intermediate.invalid")
	info, err := Check("example.com", leaf.Cert+other.Cert, leaf.Key, now)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if strings.Join(info.Subjects, ",") != "example.com" {
		t.Errorf("Subjects = %v, want the first certificate's", info.Subjects)
	}
	if described, err := Describe(leaf.Cert + other.Cert); err != nil || strings.Join(described.Subjects, ",") != "example.com" {
		t.Errorf("Describe = %+v, %v", described, err)
	}
}

func TestCheckSaysWhyItRefuses(t *testing.T) {
	good := certstest.Issue(now.AddDate(0, 3, 0), "example.com")
	other := certstest.Issue(now.AddDate(0, 3, 0), "example.com")
	expired := certstest.Issue(now.AddDate(0, 0, -2), "example.com")
	future := certstest.Issue(now.AddDate(2, 0, 0), "example.com")
	nameless := certstest.Issue(now.AddDate(0, 3, 0))
	encrypted := "-----BEGIN ENCRYPTED PRIVATE KEY-----\nMIIB\n-----END ENCRYPTED PRIVATE KEY-----\n"

	tests := []struct {
		name, hostname, cert, key, want string
	}{
		{"not PEM", "example.com", "hello", good.Key, "the certificate is not PEM"},
		{"a key where the chain belongs", "example.com", good.Cert + good.Key, good.Key, "the certificate file contains a private key"},
		{"a damaged certificate", "example.com", "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n", good.Key, "certificate 1 in the chain cannot be read"},
		{"text after the chain", "example.com", good.Cert + "trailing", good.Key, "text after its last certificate"},
		{"a key that is not PEM", "example.com", good.Cert, "hello", "the key is not PEM"},
		{"a certificate as the key", "example.com", good.Cert, good.Cert, `the key file contains a "CERTIFICATE" block`},
		{"a key with a passphrase", "example.com", good.Cert, encrypted, "protected by a passphrase"},
		{"another certificate's key", "example.com", good.Cert, other.Key, "the key does not belong to the first certificate of the chain"},
		{"the chain in the wrong order", "example.com", other.Cert + good.Cert, good.Key, "the server's own certificate comes first"},
		{"another hostname", "example.org", good.Cert, good.Key, "the certificate does not cover example.org: it is for example.com"},
		{"a wildcard asked of a single name", "*.example.com", good.Cert, good.Key, "the certificate does not cover *.example.com"},
		{"expired", "example.com", expired.Cert, expired.Key, "the certificate expired on 2026-09-29"},
		{"not valid yet", "example.com", future.Cert, future.Key, "the certificate is not valid before 2027-10-01"},
		{"no DNS names", "example.com", nameless.Cert, nameless.Key, "the certificate names no hostname"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Check(tt.hostname, tt.cert, tt.key, now)
			var refused *Error
			if !errors.As(err, &refused) || !strings.Contains(refused.Reason, tt.want) {
				t.Fatalf("err = %v, want a refusal containing %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "MII") || strings.Contains(err.Error(), "hello") || strings.Contains(err.Error(), "trailing") {
				t.Errorf("the refusal quotes the input: %v", err)
			}
		})
	}
}

func TestCoversMatchesAWildcardAgainstOneLabel(t *testing.T) {
	tests := []struct {
		subjects []string
		host     string
		want     bool
	}{
		{[]string{"example.com"}, "example.com", true},
		{[]string{"example.com"}, "api.example.com", false},
		{[]string{"*.example.com"}, "api.example.com", true},
		{[]string{"*.example.com"}, "example.com", false},
		{[]string{"*.example.com"}, "a.b.example.com", false},
		{[]string{"*.example.com"}, "*.example.com", true},
		{[]string{"a.example.com", "b.example.com"}, "*.example.com", false},
		{[]string{"*.example.com"}, "*.api.example.com", false},
		{nil, "example.com", false},
	}
	for _, tt := range tests {
		if got := Covers(tt.subjects, tt.host); got != tt.want {
			t.Errorf("Covers(%v, %q) = %v, want %v", tt.subjects, tt.host, got, tt.want)
		}
	}
}
