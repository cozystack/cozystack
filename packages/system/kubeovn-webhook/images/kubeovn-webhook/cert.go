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

	mu      sync.RWMutex
	cert    *tls.Certificate
	modTime time.Time
}

// newCertReloader loads the initial key pair and returns a reloader for it.
func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	cr := &certReloader{certFile: certFile, keyFile: keyFile}
	if err := cr.reload(); err != nil {
		return nil, err
	}
	return cr, nil
}

// reload reads the key pair from disk and atomically swaps the cached certificate.
func (cr *certReloader) reload() error {
	cert, err := tls.LoadX509KeyPair(cr.certFile, cr.keyFile)
	if err != nil {
		return err
	}

	var modTime time.Time
	if fi, statErr := os.Stat(cr.certFile); statErr == nil {
		modTime = fi.ModTime()
	}

	cr.mu.Lock()
	cr.cert = &cert
	cr.modTime = modTime
	cr.mu.Unlock()
	return nil
}

// changed reports whether the certificate file was modified since the last load.
func (cr *certReloader) changed() bool {
	fi, err := os.Stat(cr.certFile)
	if err != nil {
		return false
	}
	cr.mu.RLock()
	defer cr.mu.RUnlock()
	return fi.ModTime().After(cr.modTime)
}

// GetCertificate is a tls.Config.GetCertificate callback. It reloads the key pair
// when the file changes and keeps serving the last good certificate if a reload
// fails (for example a torn read while the mounted Secret is being updated).
func (cr *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if cr.changed() {
		if err := cr.reload(); err != nil {
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
