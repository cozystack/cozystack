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
	"testing"
	"time"
)

// testPKI is a certificate authority and one client certificate it issued,
// standing in for the apiserver's front-proxy CA and its proxy client cert.
type testPKI struct {
	caPEM  []byte
	client tls.Certificate
}

func newTestPKI(t *testing.T, clientCN string) *testPKI {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "front-proxy-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: clientCN},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTmpl, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return &testPKI{
		caPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		client: tls.Certificate{Certificate: [][]byte{clientDER}, PrivateKey: clientKey},
	}
}

// authData is the extension-apiserver-authentication ConfigMap for this CA.
func (p *testPKI) authData() map[string]string {
	return map[string]string{
		"requestheader-client-ca-file":       string(p.caPEM),
		"requestheader-allowed-names":        `["front-proxy-client"]`,
		"requestheader-username-headers":     `["X-Remote-User"]`,
		"requestheader-group-headers":        `["X-Remote-Group"]`,
		"requestheader-extra-headers-prefix": `["X-Remote-Extra-"]`,
	}
}
