package outbound

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestCheckRefusesAProxyVariableWithoutRepeatingItsValue(t *testing.T) {
	for name, value := range map[string]string{
		"a scheme that is no proxy's": "ftp://alice:hunter2secret@proxy.example.com:3128",
		"not an address at all":       "http://alice:hunter2secret@:bad port",
	} {
		t.Run(name, func(t *testing.T) {
			err := Check(env(map[string]string{"HTTPS_PROXY": value}))
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.HasPrefix(err.Error(), "HTTPS_PROXY: ") {
				t.Errorf("the error does not name the variable: %v", err)
			}
			if strings.Contains(err.Error(), "hunter2secret") {
				t.Errorf("the error repeats the password: %v", err)
			}
		})
	}
}

func TestCheckAcceptsWhatNetHTTPAccepts(t *testing.T) {
	for _, value := range []string{"", "http://proxy.example.com:3128", "https://u:p@proxy.example.com", "proxy.example.com:3128", "socks5://127.0.0.1:1080"} {
		if err := Check(env(map[string]string{"HTTP_PROXY": value})); err != nil {
			t.Errorf("%q: %v", value, err)
		}
	}
}

func TestProxyHostLeavesTheCredentialsOut(t *testing.T) {
	got := ProxyHost(env(map[string]string{"https_proxy": "http://alice:hunter2secret@proxy.example.com:3128"}))
	if got != "proxy.example.com:3128" {
		t.Errorf("ProxyHost = %q", got)
	}
	if got := ProxyHost(env(nil)); got != "" {
		t.Errorf("without a proxy: %q", got)
	}
}

func TestANameInsideTheInstallationNeverGoesThroughTheProxy(t *testing.T) {
	proxy, _ := url.Parse("http://proxy.example.com:3128")
	asked := 0
	fromEnvironment := func(*http.Request) (*url.URL, error) { asked++; return proxy, nil }
	for target, through := range map[string]bool{
		"http://minio:9000/bucket":         false,
		"http://my-api:8080/hook":          false,
		"https://hooks.slack.com/services": true,
		"https://10.0.0.5/hook":            true,
		"https://[2001:db8::1]/hook":       true,
	} {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		got, err := proxyFor(req, fromEnvironment)
		if err != nil {
			t.Fatal(err)
		}
		if (got != nil) != through {
			t.Errorf("%s: proxy = %v, want through=%v", target, got, through)
		}
	}
	if asked != 3 {
		t.Errorf("the environment was asked %d times, want 3", asked)
	}
}

func TestAProxyTheEnvironmentCannotParseIsReportedWithoutItsValue(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	_, err := proxyFor(req, func(*http.Request) (*url.URL, error) {
		return nil, errors.New(`invalid proxy address "http://alice:hunter2secret@proxy:bad"`)
	})
	if err == nil || strings.Contains(err.Error(), "hunter2secret") {
		t.Errorf("err = %v", err)
	}
}

func TestARefusedConnectNamesTheProxyAndTheDestination(t *testing.T) {
	for status, advice := range map[int]string{
		http.StatusForbidden:         "check that the proxy allows this destination",
		http.StatusProxyAuthRequired: "a user and a password it accepts",
	} {
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		proxyURL, _ := url.Parse(proxy.URL)
		proxyURL.User = url.UserPassword("alice", "hunter2secret")
		transport := &http.Transport{
			Proxy:                  func(*http.Request) (*url.URL, error) { return proxyURL, nil },
			OnProxyConnectResponse: ProxyRefused,
		}
		_, err := (&http.Client{Transport: transport}).Get("https://bucket.example.com/key")
		proxy.Close()

		var refused *RefusedError
		if !errors.As(err, &refused) {
			t.Fatalf("HTTP %d: err = %v, want a RefusedError", status, err)
		}
		if refused.Target != "bucket.example.com:443" || refused.Proxy != proxyURL.Host {
			t.Errorf("refused = %+v", refused)
		}
		if msg := refused.Error(); !strings.Contains(msg, advice) || strings.Contains(msg, "hunter2secret") {
			t.Errorf("message = %q", msg)
		}
	}
}

// authority returns a self-signed certificate authority and a certificate it
// issued for a server, both PEM.
func authority(t *testing.T, notAfter time.Time) (caPEM, leafPEM, keyPEM []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Example Internal Root"},
		NotBefore:             notAfter.Add(-24 * time.Hour),
		NotAfter:              notAfter,
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "hooks.example.internal"},
		DNSNames:     []string{"hooks.example.internal"},
		NotBefore:    notAfter.Add(-24 * time.Hour),
		NotAfter:     notAfter,
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func TestACAFileThatIsWrongGetsASentenceOfItsOwn(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	caPEM, leafPEM, keyPEM := authority(t, now.Add(24*time.Hour))
	oldPEM, _, _ := authority(t, now.Add(-48*time.Hour))
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if err := os.Mkdir(filepath.Join(dir, "mounted"), 0o700); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, path, want string
	}{
		{"a file that is not there", filepath.Join(dir, "missing.pem"), "does not exist"},
		{"the directory Docker made of a missing mount", filepath.Join(dir, "mounted"), "is a directory, not a file"},
		{"no PEM in it", write("text.pem", []byte("not a certificate\n")), "holds no certificate"},
		{"the key instead of the certificate", write("key.pem", keyPEM), "holds a private key and no certificate"},
		{"the server's certificate instead of the authority's", write("leaf.pem", leafPEM), "is a server's certificate, not an authority's"},
		{"a block that is not a certificate", write("broken.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("junk")})), "certificate 1 cannot be read"},
		{"only authorities that have expired", write("old.pem", oldPEM), "has expired, the last on 2026-10-01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadCAFile(tt.path, now)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to say %q", err, tt.want)
			}
		})
	}

	// An expired authority next to a current one is how a rotation looks.
	pool, err := loadCAFile(write("both.pem", append(append([]byte{}, oldPEM...), caPEM...)), now)
	if err != nil || pool == nil {
		t.Fatalf("a bundle with one current authority: %v", err)
	}
}

func TestTrustMakesAServerOfTheAuthorityVerify(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	t.Cleanup(func() { roots.Store(nil) })

	if _, err := (&http.Client{Transport: Transport()}).Get(server.URL); err == nil {
		t.Fatal("the test server verified before its certificate was trusted")
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(path, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Trust(path); err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: Transport()}).Get(server.URL)
	if err != nil {
		t.Fatalf("after Trust: %v", err)
	}
	resp.Body.Close()
}

func TestTrustWithoutAFileChangesNothing(t *testing.T) {
	if err := Trust(""); err != nil {
		t.Fatal(err)
	}
	if TLSConfig() != nil {
		t.Error("a TLS configuration without a file")
	}
}
