package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// genSelfSigned returns a PEM-encoded self-signed cert/key pair carrying the given
// serial number, so tests can tell two generations of the certificate apart.
func genSelfSigned(t *testing.T, serial int64) (certPEM, keyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: "kube-ovn-webhook-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"localhost"},
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// writeKeyPair writes the cert/key files and sets their mtime, mirroring how
// cert-manager replaces the mounted Secret on renewal.
func writeKeyPair(t *testing.T, certFile, keyFile string, certPEM, keyPEM []byte, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	setModTime(t, certFile, mtime)
	setModTime(t, keyFile, mtime)
}

func setModTime(t *testing.T, name string, mtime time.Time) {
	t.Helper()
	if err := os.Chtimes(name, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", name, err)
	}
}

// certSerial returns the serial number of a *tls.Certificate leaf without mutating the
// shared value returned by GetCertificate (it parses into a local when Leaf is unset).
func certSerial(t *testing.T, c *tls.Certificate) int64 {
	t.Helper()
	if c == nil {
		t.Fatal("nil certificate")
	}
	leaf := c.Leaf
	if leaf == nil {
		if len(c.Certificate) == 0 {
			t.Fatal("certificate has no DER data")
		}
		parsed, err := x509.ParseCertificate(c.Certificate[0])
		if err != nil {
			t.Fatalf("parse served cert: %v", err)
		}
		leaf = parsed
	}
	return leaf.SerialNumber.Int64()
}

// servedSerial completes a TLS handshake against addr and returns the serial number
// of the leaf certificate the server actually presented.
func servedSerial(t *testing.T, addr string) int64 {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test-only, inspecting the served cert
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		t.Fatalf("server presented no certificate")
	}
	return certs[0].SerialNumber.Int64()
}

// TestReloadingTLSConfigServesRenewedCertificate is the core regression test: it fails
// against the old code that loads the key pair once into tls.Config.Certificates, and
// passes once the certificate is served via a reloading GetCertificate callback.
func TestReloadingTLSConfigServesRenewedCertificate(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")

	certA, keyA := genSelfSigned(t, 1)
	writeKeyPair(t, certFile, keyFile, certA, keyA, time.Now().Add(-2*time.Second))

	tlsConfig, err := newReloadingTLSConfig(certFile, keyFile)
	if err != nil {
		t.Fatalf("newReloadingTLSConfig: %v", err)
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				if tc, ok := conn.(*tls.Conn); ok {
					_ = tc.Handshake()
				}
				conn.Close()
			}()
		}
	}()

	addr := ln.Addr().String()

	if got := servedSerial(t, addr); got != 1 {
		t.Fatalf("before renewal: expected serial 1, got %d", got)
	}

	// cert-manager renews the Secret: the mounted files are replaced in place.
	certB, keyB := genSelfSigned(t, 2)
	writeKeyPair(t, certFile, keyFile, certB, keyB, time.Now().Add(2*time.Second))

	if got := servedSerial(t, addr); got != 2 {
		t.Fatalf("after renewal: expected renewed serial 2, got %d (certificate was not reloaded)", got)
	}
}

// TestCertReloaderKeepsLastGoodCertificate verifies that a failed reload (e.g. a torn
// read while the volume is updated) does not take the webhook down: it keeps serving
// the previously loaded certificate instead of erroring the handshake.
func TestCertReloaderKeepsLastGoodCertificate(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")

	certA, keyA := genSelfSigned(t, 1)
	writeKeyPair(t, certFile, keyFile, certA, keyA, time.Now().Add(-2*time.Second))

	cr, err := newCertReloader(certFile, keyFile)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}

	// Corrupt the cert file and advance its mtime so a reload is attempted and fails.
	if err := os.WriteFile(certFile, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("corrupt cert: %v", err)
	}
	setModTime(t, certFile, time.Now().Add(2*time.Second))

	got, err := cr.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate returned error instead of serving last good cert: %v", err)
	}
	if certSerial(t, got) != 1 {
		t.Fatalf("expected to keep serving serial 1 after failed reload, got %d", certSerial(t, got))
	}
}

// TestCertReloaderRetriesAfterTransientFailure is the regression test for the reloader
// abandoning a renewal on a transient error. A failed reload must NOT advance the
// recorded mtime, so once the (unchanged-mtime) file becomes readable the renewed
// certificate is still picked up. Against the code that advanced the mtime on failure,
// the renewal is permanently missed and this test fails.
func TestCertReloaderRetriesAfterTransientFailure(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")

	certA, keyA := genSelfSigned(t, 1)
	writeKeyPair(t, certFile, keyFile, certA, keyA, time.Now().Add(-2*time.Second))

	cr, err := newCertReloader(certFile, keyFile)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}
	cr.retryInterval = 0 // retry immediately, no backoff wait in the test

	// Renewal advances the mtime, but the first load fails (simulating a transient,
	// content-independent error) by writing an unreadable cert at the new mtime.
	renewMod := time.Now().Add(2 * time.Second)
	if err := os.WriteFile(certFile, []byte("transiently unreadable"), 0o600); err != nil {
		t.Fatalf("write bad cert: %v", err)
	}
	setModTime(t, certFile, renewMod)

	if got, gerr := cr.GetCertificate(&tls.ClientHelloInfo{}); gerr != nil {
		t.Fatalf("GetCertificate errored during transient failure: %v", gerr)
	} else if certSerial(t, got) != 1 {
		t.Fatalf("during transient failure: expected to keep serial 1, got %d", certSerial(t, got))
	}

	// The renewed, valid certificate is now readable at the SAME mtime as the failed
	// attempt. The reloader must still pick it up on retry.
	certB, keyB := genSelfSigned(t, 2)
	if err := os.WriteFile(certFile, certB, 0o600); err != nil {
		t.Fatalf("write renewed cert: %v", err)
	}
	if err := os.WriteFile(keyFile, keyB, 0o600); err != nil {
		t.Fatalf("write renewed key: %v", err)
	}
	setModTime(t, certFile, renewMod)
	setModTime(t, keyFile, renewMod)

	got, gerr := cr.GetCertificate(&tls.ClientHelloInfo{})
	if gerr != nil {
		t.Fatalf("GetCertificate errored after retry: %v", gerr)
	}
	if certSerial(t, got) != 2 {
		t.Fatalf("after transient failure: expected renewed serial 2 on retry, got %d (mtime advanced on failure, renewal permanently missed)", certSerial(t, got))
	}
}
