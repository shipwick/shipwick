package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

const (
	testChain = "-----BEGIN CERTIFICATE-----\nchain\n-----END CERTIFICATE-----\n"
	testKey   = "-----BEGIN PRIVATE KEY-----\nthe-key\n-----END PRIVATE KEY-----\n"
)

func TestCertificatesRoundTripInHostnameOrder(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	stored, err := s.SetCertificate(ctx, "example.com", testChain, testKey, now)
	if err != nil {
		t.Fatalf("SetCertificate: %v", err)
	}
	if !stored.CreatedAt.Equal(now) || !stored.UpdatedAt.Equal(now) {
		t.Errorf("stored = %+v", stored)
	}
	if _, err := s.SetCertificate(ctx, "*.example.com", testChain, "another-key", now); err != nil {
		t.Fatalf("SetCertificate: %v", err)
	}

	list, err := s.ListCertificates(ctx)
	if err != nil {
		t.Fatalf("ListCertificates: %v", err)
	}
	if len(list) != 2 || list[0].Hostname != "*.example.com" || list[1].Hostname != "example.com" {
		t.Fatalf("list = %+v", list)
	}
	if list[1].CertPEM != testChain || list[1].KeyPEM != testKey || list[0].KeyPEM != "another-key" {
		t.Errorf("the chain or the key did not come back as stored: %+v", list)
	}
}

func TestSetCertificateReplacesAndKeepsCreatedAt(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	created := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	later := created.Add(time.Hour)

	s.SetCertificate(ctx, "example.com", testChain, "old-key", created)
	stored, err := s.SetCertificate(ctx, "example.com", testChain, "new-key", later)
	if err != nil {
		t.Fatalf("SetCertificate again: %v", err)
	}
	if !stored.CreatedAt.Equal(created) || !stored.UpdatedAt.Equal(later) {
		t.Errorf("stored = %+v, want created %v and updated %v", stored, created, later)
	}
	list, _ := s.ListCertificates(ctx)
	if len(list) != 1 || list[0].KeyPEM != "new-key" || !list[0].CreatedAt.Equal(created) || !list[0].UpdatedAt.Equal(later) {
		t.Errorf("list = %+v", list)
	}
}

func TestDeleteCertificate(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	s.SetCertificate(ctx, "example.com", testChain, testKey, time.Now())

	if err := s.DeleteCertificate(ctx, "example.com"); err != nil {
		t.Fatalf("DeleteCertificate: %v", err)
	}
	if err := s.DeleteCertificate(ctx, "example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting again = %v, want ErrNotFound", err)
	}
	if list, _ := s.ListCertificates(ctx); len(list) != 0 {
		t.Errorf("list = %+v", list)
	}
}

func TestACertificatesKeyIsEncryptedAtRestAndBoundToItsHostname(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	s.SetCertificate(ctx, "example.com", testChain, testKey, time.Now())
	s.SetCertificate(ctx, "example.org", testChain, "other", time.Now())

	var chain, key string
	if err := s.db.QueryRowContext(ctx, `SELECT certificate, key FROM certificates WHERE hostname = 'example.com'`).Scan(&chain, &key); err != nil {
		t.Fatal(err)
	}
	if chain != testChain {
		t.Errorf("the chain is public and should be readable in the database, got %q", chain)
	}
	if !isSealed(key) || strings.Contains(key, "the-key") {
		t.Fatalf("the key is stored as %q", key)
	}

	// A key moved to another hostname's row must not decrypt there.
	if _, err := s.db.ExecContext(ctx, `UPDATE certificates SET key = ? WHERE hostname = 'example.org'`, []byte(key)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListCertificates(ctx); err == nil {
		t.Error("a key pasted under another hostname was accepted")
	}
}

func TestSetCertificateBoundsHowManyAreStored(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	for i := 0; i < api.MaxCertificates; i++ {
		if _, err := s.SetCertificate(ctx, fmt.Sprintf("h%d.example.com", i), testChain, testKey, time.Now()); err != nil {
			t.Fatalf("certificate %d: %v", i, err)
		}
	}
	if _, err := s.SetCertificate(ctx, "one-more.example.com", testChain, testKey, time.Now()); !errors.Is(err, ErrTooManyCertificates) {
		t.Errorf("err = %v, want ErrTooManyCertificates", err)
	}
	if _, err := s.SetCertificate(ctx, "h0.example.com", testChain, testKey, time.Now()); err != nil {
		t.Errorf("replacing one at the limit: %v", err)
	}
}
