/*
Copyright 2025 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package proxmoxnetworkcontroller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The keys of Kube-OVN's TLS secret, which Kube-OVN mounts at /var/run/tls.
const (
	tlsKeyCA   = "cacert"
	tlsKeyCert = "cert"
	tlsKeyKey  = "key"
)

// +kubebuilder:object:generate=false

// SecretTLSSource builds the OVN NB client TLS configuration from Kube-OVN's
// TLS secret, read through the API with a single get (no list, no watch, no
// copy of the secret). The parsed configuration is kept until the secret's
// resourceVersion changes or a handshake fails. Nothing of the secret's data
// is ever logged or put into an error.
type SecretTLSSource struct {
	Reader client.Reader
	Secret client.ObjectKey

	mu      sync.Mutex
	version string
	config  *tls.Config
}

// TLSConfig returns the configuration for the secret as it is now.
func (s *SecretTLSSource) TLSConfig(ctx context.Context) (*tls.Config, error) {
	sec := &corev1.Secret{}
	if err := s.Reader.Get(ctx, s.Secret, sec); err != nil {
		return nil, fmt.Errorf("read OVN TLS secret %s: %w", s.Secret, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.config != nil && s.version == sec.ResourceVersion {
		return s.config, nil
	}
	for _, k := range []string{tlsKeyCA, tlsKeyCert, tlsKeyKey} {
		if len(sec.Data[k]) == 0 {
			return nil, fmt.Errorf("OVN TLS secret %s has no %q key", s.Secret, k)
		}
	}
	cfg, err := ovnClientTLSConfig(sec.Data[tlsKeyCA], sec.Data[tlsKeyCert], sec.Data[tlsKeyKey])
	if err != nil {
		return nil, fmt.Errorf("OVN TLS secret %s: %w", s.Secret, err)
	}
	s.config, s.version = cfg, sec.ResourceVersion
	return cfg, nil
}

// Invalidate drops the parsed configuration.
func (s *SecretTLSSource) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config, s.version = nil, ""
}

// ovnClientTLSConfig presents the client certificate and checks the server's
// certificate chain against the CA, but not its host name. That is how OVS's
// own SSL streams, ovn-nbctl and Kube-OVN's clients treat the OVN databases.
// Cozystack's kube-ovn chart issues the secret with Helm's genCA and
// genSignedCert: one certificate, CN "ovn" and no subject alternative names,
// for both the servers and the clients, signed by a CA private to Kube-OVN.
func ovnClientTLSConfig(caPEM, certPEM, keyPEM []byte) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		// The error of X509KeyPair names the failure, never the key.
		return nil, fmt.Errorf("client certificate: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("no CA certificate in the cacert key")
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		// The chain is verified below, without a host name.
		InsecureSkipVerify: true, // #nosec G402
		VerifyConnection: func(cs tls.ConnectionState) error {
			return verifyOVNPeer(cs, roots)
		},
	}, nil
}

func verifyOVNPeer(cs tls.ConnectionState, roots *x509.CertPool) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("OVN NB presented no certificate")
	}
	inter := x509.NewCertPool()
	for _, c := range cs.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{
		Roots: roots, Intermediates: inter,
		// genSignedCert sets serverAuth and clientAuth. A certificate with no
		// extended key usage at all, as ovs-pki issues them, passes too.
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	if err != nil {
		return fmt.Errorf("OVN NB certificate: %w", err)
	}
	return nil
}
