package chatty

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

const doorRenewBefore = 30 * 24 * time.Hour

type doorCertificates struct {
	dir, hostname, binary string
	runner                Runner
	now                   func() time.Time
	current               atomic.Pointer[tls.Certificate]
}

func (c *doorCertificates) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	certificate := c.current.Load()
	if certificate == nil {
		return nil, errors.New("HTTPS certificate unavailable")
	}
	return certificate, nil
}

func (c *doorCertificates) parse(certPEM, keyPEM []byte) (*tls.Certificate, error) {
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, errors.New("invalid TLS pair")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil || leaf.VerifyHostname(c.hostname) != nil || c.now().Before(leaf.NotBefore) || !c.now().Before(leaf.NotAfter) {
		return nil, errors.New("TLS certificate invalid for hostname or validity period")
	}
	certificate.Leaf = leaf
	return &certificate, nil
}

func (c *doorCertificates) check(ctx context.Context) error {
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		return errors.New("cannot create cert-dir")
	}
	info, err := os.Lstat(c.dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("cert-dir must be a regular directory without symlink")
	}
	if err := os.Chmod(c.dir, 0700); err != nil {
		return errors.New("cannot protect cert-dir")
	}
	bundlePath := filepath.Join(c.dir, "certificate.pem")
	if c.current.Load() == nil {
		info, err := os.Lstat(bundlePath)
		if err == nil {
			if !info.Mode().IsRegular() {
				return errors.New("TLS bundle must be a regular file without symlink")
			}
			if err := os.Chmod(bundlePath, 0600); err != nil {
				return errors.New("cannot protect TLS bundle")
			}
			data, err := os.ReadFile(bundlePath)
			if err != nil {
				return errors.New("cannot read TLS bundle")
			}
			if certificate, err := c.parse(data, data); err == nil {
				c.current.Store(certificate)
			}
		} else if !os.IsNotExist(err) {
			return errors.New("cannot inspect TLS bundle")
		}
	}
	if certificate := c.current.Load(); certificate != nil && certificate.Leaf.NotAfter.Sub(c.now()) >= doorRenewBefore {
		return nil
	}

	stage, err := os.MkdirTemp(c.dir, ".renew-*")
	if err != nil {
		return errors.New("cannot prepare TLS renewal")
	}
	defer os.RemoveAll(stage)
	certPath, keyPath := filepath.Join(stage, "cert.pem"), filepath.Join(stage, "key.pem")
	// Precreate private outputs even if tailscale uses the caller's permissive umask.
	for _, path := range []string{certPath, keyPath} {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return errors.New("cannot prepare private TLS files")
		}
		if err := file.Close(); err != nil {
			return errors.New("cannot close TLS file")
		}
	}
	commandCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	// Tailscale may otherwise return a cached certificate inside our renewal window.
	// Request the CHATTY.md 30-day minimum explicitly, not just unexpired output.
	if _, err := c.runner.Run(commandCtx, c.binary, "cert", "--cert-file", certPath, "--key-file", keyPath, "--min-validity", doorRenewBefore.String(), c.hostname); err != nil {
		return errors.New("tailscale cert failed; check binary and issuance permission")
	}
	if err := os.Chmod(keyPath, 0600); err != nil {
		return errors.New("cannot protect TLS key")
	}
	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)
	if certErr != nil || keyErr != nil {
		return errors.New("files issued by tailscale cert unavailable")
	}
	certificate, err := c.parse(certPEM, keyPEM)
	if err != nil {
		return err
	}
	if certificate.Leaf.NotAfter.Sub(c.now()) < doorRenewBefore {
		return errors.New("tailscale cert did not issue a certificate with at least 30 days remaining")
	}
	// One private bundle makes the persisted cert/key pair atomic across crashes.
	// CHATTY.md documents this format; it is never written as a public certificate.
	file, err := os.CreateTemp(c.dir, ".bundle-*")
	if err != nil {
		return errors.New("cannot prepare TLS bundle")
	}
	defer os.Remove(file.Name())
	bundle := append(append(append([]byte{}, certPEM...), '\n'), keyPEM...)
	_, writeErr := file.Write(bundle)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("cannot persist TLS bundle")
	}
	if err := os.Rename(file.Name(), bundlePath); err != nil {
		return errors.New("cannot publish TLS bundle")
	}
	c.current.Store(certificate)
	return nil
}
