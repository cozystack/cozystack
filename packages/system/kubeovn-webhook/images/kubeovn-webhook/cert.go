package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// certReloader keeps the webhook serving certificate in sync with the key pair
// files written by cert-manager.
//
// The certificate is served through tls.Config.GetCertificate, so a cert-manager
// renewal of the backing Secret is picked up by the running process without a
// restart. Without this, the key pair is read exactly once at startup and cached
// for the lifetime of the process: after the certificate expires (roughly one year
// after install) the pod keeps presenting the expired certificate, the
// kube-apiserver's TLS call to the webhook fails, and because the
// MutatingWebhookConfiguration uses failurePolicy: Fail, every pod creation in
// tenant namespaces is rejected.
type certReloader struct {
	certFile string
	keyFile  string

	// retryInterval bounds how often a failed reload (or a repeated stat failure) is
	// retried, so a persistently broken file does not read/parse/log on every handshake.
	retryInterval time.Duration

	mu            sync.RWMutex
	cert          *tls.Certificate
	loadedModTime time.Time
	reloading     bool
	nextRetry     time.Time
	nextLog       time.Time
}

const defaultReloadRetryInterval = 30 * time.Second

// newCertReloader loads the initial key pair and returns a reloader for it.
func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	cr := &certReloader{
		certFile:      certFile,
		keyFile:       keyFile,
		retryInterval: defaultReloadRetryInterval,
	}
	modTime, err := fileModTime(certFile)
	if err != nil {
		return nil, err
	}
	if err := cr.load(modTime); err != nil {
		return nil, err
	}
	return cr, nil
}

// fileModTime returns the modification time of the given file.
func fileModTime(name string) (time.Time, error) {
	fi, err := os.Stat(name)
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}

// load reads the key pair and, on success, caches it tagged with modTime. On failure
// the cached certificate and its recorded mtime are left unchanged, so a transient
// failure on a valid file is retried rather than the renewal being abandoned.
func (cr *certReloader) load(modTime time.Time) error {
	cert, err := tls.LoadX509KeyPair(cr.certFile, cr.keyFile)
	if err != nil {
		return err
	}
	cr.mu.Lock()
	cr.cert = &cert
	cr.loadedModTime = modTime
	cr.mu.Unlock()
	return nil
}

// refresh reloads the key pair when the certificate file's modification time changes.
//
// A failed reload keeps the last good certificate and does NOT advance the recorded
// mtime, so a transient error (fd exhaustion, a torn read) is retried, at most once per
// retryInterval, until it succeeds: a transient failure must never permanently pin an
// expiring certificate. A single reload is kept in flight so a burst of handshakes does
// not trigger a thundering herd of reads.
func (cr *certReloader) refresh() {
	modTime, err := fileModTime(cr.certFile)
	if err != nil {
		cr.logRateLimited("certificate stat failed, keeping previous certificate: %v", err)
		return
	}

	cr.mu.Lock()
	if modTime.Equal(cr.loadedModTime) || cr.reloading || time.Now().Before(cr.nextRetry) {
		cr.mu.Unlock()
		return
	}
	cr.reloading = true
	cr.mu.Unlock()

	loadErr := cr.load(modTime)

	cr.mu.Lock()
	cr.reloading = false
	if loadErr == nil {
		cr.nextRetry = time.Time{}
	} else {
		cr.nextRetry = time.Now().Add(cr.retryInterval)
	}
	cr.mu.Unlock()

	if loadErr != nil {
		cr.logRateLimited("certificate reload failed, keeping previous certificate: %v", loadErr)
	}
}

// logRateLimited emits a log line at most once per retryInterval, so a persistently
// broken mount does not spam the log on every TLS handshake.
func (cr *certReloader) logRateLimited(format string, args ...any) {
	cr.mu.Lock()
	if time.Now().Before(cr.nextLog) {
		cr.mu.Unlock()
		return
	}
	cr.nextLog = time.Now().Add(cr.retryInterval)
	cr.mu.Unlock()
	log.Printf(format, args...)
}

// GetCertificate is a tls.Config.GetCertificate callback. It refreshes the key pair
// when the certificate file changes and keeps serving the last good certificate if a
// reload fails.
func (cr *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	cr.refresh()

	cr.mu.RLock()
	defer cr.mu.RUnlock()
	if cr.cert == nil {
		return nil, fmt.Errorf("no certificate loaded")
	}
	return cr.cert, nil
}

// newReloadingTLSConfig builds a tls.Config that serves the key pair via a
// certReloader, so cert-manager renewals are honoured without a restart.
func newReloadingTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	cr, err := newCertReloader(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{GetCertificate: cr.GetCertificate}, nil
}
