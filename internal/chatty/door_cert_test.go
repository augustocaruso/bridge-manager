package chatty

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The test executable doubles as a fake tailscale binary, without shell scripts.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "tailscale-fake" {
		os.Exit(fakeDoorTailscale())
	}
	os.Exit(m.Run())
}

func fakeDoorTailscale() int {
	if len(os.Args) != 9 || os.Args[1] != "cert" || os.Args[2] != "--cert-file" || os.Args[4] != "--key-file" || os.Args[6] != "--min-validity" || os.Args[7] != doorRenewBefore.String() || os.Args[8] != "test.tailnet.ts.net" {
		return 2
	}
	if os.Getenv("DOOR_FAKE_FAIL") == "1" {
		return 3
	}
	for _, pair := range [][2]string{{os.Getenv("DOOR_FAKE_CERT"), os.Args[3]}, {os.Getenv("DOOR_FAKE_KEY"), os.Args[5]}} {
		data, err := os.ReadFile(pair[0])
		if err != nil {
			return 4
		}
		if err := os.WriteFile(pair[1], data, 0600); err != nil {
			return 5
		}
	}
	return 0
}

func syntheticDoorCertificate(t *testing.T, serial int64, hostname string, notBefore, notAfter time.Time) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: hostname}, DNSNames: []string{hostname}, NotBefore: notBefore, NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func fakeDoorBinary(t *testing.T, cert, key []byte) string {
	t.Helper()
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "source.crt"), filepath.Join(dir, "source.key")
	if err := os.WriteFile(certPath, cert, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOOR_FAKE_CERT", certPath)
	t.Setenv("DOOR_FAKE_KEY", keyPath)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "tailscale-fake")
	if err := os.Symlink(executable, binary); err != nil {
		t.Fatal(err)
	}
	return binary
}

func assertDoorCertificateFiles(t *testing.T, dir string) {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("insecure certificate directory: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "certificate.pem" {
		t.Fatalf("renewal left staging files: %v", entries)
	}
	info, err = os.Stat(filepath.Join(dir, "certificate.pem"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("insecure private bundle: %v", err)
	}
}

func TestDoorCertificateRenewalSwapsOnSameServer(t *testing.T) {
	now := time.Now().UTC()
	cert, key := syntheticDoorCertificate(t, 1, "test.tailnet.ts.net", now.Add(-time.Hour), now.Add(90*24*time.Hour))
	binary := fakeDoorBinary(t, cert, key)
	clock := now
	certificates := &doorCertificates{dir: filepath.Join(t.TempDir(), "private"), hostname: "test.tailnet.ts.net", binary: binary, runner: ExecRunner{}, now: func() time.Time { return clock }}
	if err := certificates.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertDoorCertificateFiles(t, certificates.dir)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "same-server") }))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: certificates.getCertificate}
	server.StartTLS()
	defer server.Close()
	serial := func() int64 {
		t.Helper()
		conn, err := tls.Dial("tcp", server.Listener.Addr().String(), &tls.Config{InsecureSkipVerify: true, ServerName: "test.tailnet.ts.net"})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		return conn.ConnectionState().PeerCertificates[0].SerialNumber.Int64()
	}
	if serial() != 1 {
		t.Fatal("initial certificate not served")
	}
	t.Setenv("DOOR_FAKE_FAIL", "1")
	if err := certificates.check(context.Background()); err != nil {
		t.Fatalf("renewed before threshold: %v", err)
	}
	clock = now.Add(61 * 24 * time.Hour)
	if err := certificates.check(context.Background()); err == nil {
		t.Fatal("failed renewal accepted")
	}
	if serial() != 1 {
		t.Fatal("failed renewal replaced working certificate")
	}
	t.Setenv("DOOR_FAKE_FAIL", "0")
	cert, key = syntheticDoorCertificate(t, 2, "test.tailnet.ts.net", now.Add(-time.Hour), clock.Add(90*24*time.Hour))
	if err := os.WriteFile(os.Getenv("DOOR_FAKE_CERT"), cert, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("DOOR_FAKE_KEY"), key, 0600); err != nil {
		t.Fatal(err)
	}
	if err := certificates.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if serial() != 2 {
		t.Fatal("new certificate not served on unchanged listener")
	}
	assertDoorCertificateFiles(t, certificates.dir)
	reloaded := &doorCertificates{dir: certificates.dir, hostname: certificates.hostname, binary: "does-not-exist", runner: ExecRunner{}, now: func() time.Time { return clock }}
	if err := reloaded.check(context.Background()); err != nil {
		t.Fatalf("persisted pair not reusable without issuance: %v", err)
	}
	if cert, _ := reloaded.getCertificate(nil); cert.Leaf.SerialNumber.Int64() != 2 {
		t.Fatal("persisted pair mismatch")
	}
}

func TestDoorCertificateRejectsInvalidIssuance(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name, hostname string
		before, after  time.Time
	}{
		{"wrong-host", "other.ts.net", now.Add(-time.Hour), now.Add(90 * 24 * time.Hour)},
		{"expired", "test.tailnet.ts.net", now.Add(-90 * 24 * time.Hour), now.Add(-time.Hour)},
		{"future", "test.tailnet.ts.net", now.Add(time.Hour), now.Add(90 * 24 * time.Hour)},
		{"short-validity", "test.tailnet.ts.net", now.Add(-time.Hour), now.Add(29 * 24 * time.Hour)},
	} {
		t.Run(test.name, func(t *testing.T) {
			cert, key := syntheticDoorCertificate(t, 1, test.hostname, test.before, test.after)
			certificates := &doorCertificates{dir: filepath.Join(t.TempDir(), "private"), hostname: "test.tailnet.ts.net", binary: fakeDoorBinary(t, cert, key), runner: ExecRunner{}, now: func() time.Time { return now }}
			if err := certificates.check(context.Background()); err == nil {
				t.Fatal("invalid certificate accepted")
			}
			if _, err := certificates.getCertificate(nil); err == nil {
				t.Fatal("invalid certificate activated")
			}
			entries, err := os.ReadDir(certificates.dir)
			if err != nil || len(entries) != 0 {
				t.Fatal("invalid issuance left private outputs")
			}
		})
	}
}

func TestDoorCertificateRejectsSymlinks(t *testing.T) {
	for _, kind := range []string{"directory", "bundle"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "target")
			private := filepath.Join(dir, "private")
			if kind == "directory" {
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, private); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(private, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte("preserved"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(private, "certificate.pem")); err != nil {
					t.Fatal(err)
				}
			}
			certificates := &doorCertificates{dir: private, hostname: "test.tailnet.ts.net", binary: "never-execute", runner: ExecRunner{}, now: time.Now}
			if err := certificates.check(context.Background()); err == nil {
				t.Fatal("symlink accepted")
			}
		})
	}
}

func TestDoorCLIRefusesNonlocalWithoutIssuance(t *testing.T) {
	m, out, runner := fixture(t)
	dir := filepath.Join(t.TempDir(), "certs")
	err := runApp(m, "door", "--listen", "192.0.2.123:8448", "--hostname", "test.tailnet.ts.net", "--cert-dir", dir, "--wellknown-file", "unused", "--wait-timeout", "1ms")
	if err == nil || !strings.Contains(err.Error(), "192.0.2.123") || len(runner.calls) != 0 {
		t.Fatalf("unsafe CLI result: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected CLI output: %s", out.String())
	}
}
