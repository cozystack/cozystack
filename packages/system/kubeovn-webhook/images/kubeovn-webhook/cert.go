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

	mu            sync.RWMutex
	cert          *tls.Certificate
	loadedModTime time.Time
}

// newCertReloader loads the initial key pair and returns a reloader for it.
func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	cr := &certReloader{certFile: certFile, keyFile: keyFile}
	if err := cr.reload(cr.certModTime()); err != nil {
		return nil, err
	}
	return cr, nil
}

// certModTime returns the modification time of the certificate file, or the zero
// time if it cannot be stat'd (logged so a broken mount is not silently invisible).
func (cr *certReloader) certModTime() time.Time {
	fi, err := os.Stat(cr.certFile)
	if err != nil {
		log.Printf("certificate stat failed, keeping previous certificate: %v", err)
		return time.Time{}
	}
	return fi.ModTime()
}

// reload reads the key pair from disk and atomically swaps the cached certificate.
//
// modTime is the certificate file's modification time observed BEFORE the read, so a
// Secret swap racing the read leaves modTime older than the file on disk and is caught
// on the next handshake instead of being cached under a newer mtime and missed. The
// attempt is recorded regardless of outcome, so a persistently unreadable file is
// retried only once its mtime advances, not on every handshake.
func (cr *certReloader) reload(modTime time.Time) error {
	cert, err := tls.LoadX509KeyPair(cr.certFile, cr.keyFile)

	cr.mu.Lock()
	cr.loadedModTime = modTime
	if err == nil {
		cr.cert = &cert
	}
	cr.mu.Unlock()
	return err
}

// GetCertificate is a tls.Config.GetCertificate callback. It reloads the key pair when
// the certificate file's modification time changes and keeps serving the last good
// certificate if a reload fails (for example a torn read while the mounted Secret is
// being updated).
func (cr *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	modTime := cr.certModTime()

	cr.mu.RLock()
	stale := !modTime.IsZero() && !modTime.Equal(cr.loadedModTime)
	cr.mu.RUnlock()

	if stale {
		if err := cr.reload(modTime); err != nil {
			log.Printf("certificate reload failed, keeping previous certificate: %v", err)
		}
	}

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
