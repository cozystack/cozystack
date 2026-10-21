package main

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFrontProxyAuthVerify(t *testing.T) {
	pki := newTestPKI(t, "front-proxy-client")
	other := newTestPKI(t, "front-proxy-client")
	wrongName := newTestPKI(t, "someone-else")

	auth := &frontProxyAuth{}
	if err := auth.configure(pki.authData()); err != nil {
		t.Fatal(err)
	}

	request := func(cert *tls.Certificate) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/apis/"+apiGroup, nil)
		r.Header.Set("X-Remote-User", "alice")
		r.Header.Add("X-Remote-Group", "tenant-a")
		r.Header.Add("X-Remote-Group", "system:authenticated")
		r.Header.Set("X-Remote-Extra-Scopes", "openid")
		if cert != nil {
			leaf, err := x509.ParseCertificate(cert.Certificate[0])
			if err != nil {
				t.Fatal(err)
			}
			r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
		}
		return r
	}

	user, err := auth.verify(request(&pki.client))
	if err != nil {
		t.Fatalf("valid certificate refused: %v", err)
	}
	if user.Name != "alice" || len(user.Groups) != 2 || user.Groups[0] != "tenant-a" {
		t.Fatalf("user %+v", user)
	}
	if got := user.Extra["scopes"]; len(got) != 1 || got[0] != "openid" {
		t.Fatalf("extra %v", user.Extra)
	}

	for name, cert := range map[string]*tls.Certificate{
		"no certificate":   nil,
		"other CA":         &other.client,
		"name not allowed": &wrongName.client,
	} {
		if _, err := auth.verify(request(cert)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// With no allowed names any name the CA signed will do.
	data := pki.authData()
	data["requestheader-allowed-names"] = "[]"
	if err := auth.configure(data); err != nil {
		t.Fatal(err)
	}
	wrongNameSameCA := &tls.Certificate{Certificate: pki.client.Certificate}
	if _, err := auth.verify(request(wrongNameSameCA)); err != nil {
		t.Fatalf("empty allowed names: %v", err)
	}
}

func TestFrontProxyAuthConfigureRefusesNoCA(t *testing.T) {
	auth := &frontProxyAuth{}
	if err := auth.configure(map[string]string{}); err == nil {
		t.Fatal("configured without a CA")
	}
	if _, err := auth.verify(httptest.NewRequest(http.MethodGet, "/", nil)); err == nil {
		t.Fatal("verified while unconfigured")
	}
}
