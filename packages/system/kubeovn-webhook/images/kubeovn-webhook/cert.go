package main

import "crypto/tls"

// newReloadingTLSConfig returns a tls.Config that reloads the certificate and
// key from disk on every TLS handshake, so a cert-manager renewal of the mounted
// Secret is picked up by the running process without a restart.
//
// Without this, the key pair is read exactly once at startup and cached for the
// lifetime of the process: after the certificate expires (roughly one year after
// install) the pod keeps presenting the expired certificate, the kube-apiserver's
// TLS call to the webhook fails, and because the MutatingWebhookConfiguration uses
// failurePolicy: Fail, every pod creation in tenant namespaces is rejected.
//
// tls.Config.GetCertificate is invoked on every handshake whenever Certificates is
// left unset, so re-reading the files there is enough; no watcher, mtime tracking
// or cache is needed:
//   - The cert/key files are mounted from a plain Secret volume with no subPath.
//     Kubernetes' atomic writer swaps the whole ..data directory via a single
//     symlink flip, so a reader always sees a complete old or complete new
//     generation of both files, never a torn mid-write mixture. A per-handshake
//     LoadX509KeyPair therefore cannot observe a partial renewal.
//   - The per-handshake cost (a few KB of file I/O plus a PEM/key parse) is
//     negligible next to the asymmetric crypto the handshake already performs.
//   - GetCertificate refreshes the serving key pair only. It does not refresh
//     tls.Config.ClientCAs, which the handshake reads directly. When
//     --client-ca-file is set, main reads that bundle once at startup and the
//     pool stays fixed for the process lifetime: rotating the API server's
//     client CA then requires restarting these pods, or the webhook keeps
//     validating against the retired CA and rejects the API server. With
//     failurePolicy: Fail that stops pod creation cluster-wide, so reloading
//     the pool has to land together with the mount that makes enforcement
//     reachable at all -- neither is usable without the other, and today
//     --client-ca-file has no supported in-pod path to point at.
//
// If the mounted files genuinely become unreadable (Secret deleted, permissions
// broken: an operator error, not a normal renewal) the handshake fails loudly
// rather than silently serving a stale certificate.
func newReloadingTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	// Fail fast at startup if the initial key pair is missing or malformed.
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		return nil, err
	}
	return &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := tls.LoadX509KeyPair(certFile, keyFile)
			if err != nil {
				return nil, err
			}
			return &cert, nil
		},
	}, nil
}
