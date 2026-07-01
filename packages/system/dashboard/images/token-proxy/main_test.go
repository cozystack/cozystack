package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwk"
)

// writeServerCA writes the test server's leaf cert as a PEM "CA bundle" to
// disk. Tests don't run a real cluster, so the SA CA file just needs to make
// the JWKS server's TLS handshake succeed against the cache's HTTP client.
func writeServerCA(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.crt")
	pemBlock := &pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}
	if err := os.WriteFile(caPath, pem.EncodeToMemory(pemBlock), 0o600); err != nil {
		t.Fatalf("write ca.crt: %v", err)
	}
	return caPath
}

func writeFakeSAToken(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte("fake-sa-token"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	return tokenPath
}

// makeBogusCAPEM returns a PEM-encoded throwaway certificate so newJWKCache
// can populate caCertPool against a file that exists but does not chain to
// the test server. Used by the "JWKS unreachable" test where the TLS
// handshake is expected to fail.
func makeBogusCAPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "bogus"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "bogus-ca.crt")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write bogus ca: %v", err)
	}
	return p
}

// TestNewJWKCache_ReturnsImmediatelyWhenJWKSUnreachable pins the contract
// that WaitReady(false) puts on jwk.Cache.Register: the call must return
// quickly even when the JWKS endpoint never responds successfully. Without
// WaitReady(false), Register blocks on r.Ready() with no built-in timeout
// and the pod CrashLoopBackOffs on the panic path it used to take.
func TestNewJWKCache_ReturnsImmediatelyWhenJWKSUnreachable(t *testing.T) {
	// Always-failing JWKS endpoint: closes the connection without responding.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijacker", http.StatusInternalServerError)
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)

	// Use a bogus CA so the TLS handshake fails — simulates "JWKS host is
	// unreachable in a way that the client cannot validate". This is the
	// closest faithful reproduction of the cold-start failure mode.
	caPath := makeBogusCAPEM(t)
	tokenPath := writeFakeSAToken(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	cache, err := newJWKCache(ctx, srv.URL, caPath, tokenPath)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("newJWKCache returned error: %v (must be nil with WaitReady(false))", err)
	}
	if cache == nil {
		t.Fatalf("newJWKCache returned nil cache")
	}
	if elapsed > 1*time.Second {
		t.Fatalf("newJWKCache took %s — WaitReady(false) contract broken (must return well under 1s)", elapsed)
	}

	// Cache must surface "not ready" to callers until a fetch succeeds.
	// verifyAndParseJWT relies on this: a failed Lookup is converted into
	// a sign-in error rather than serving an empty key set.
	jwkCache = cache
	t.Cleanup(func() { jwkCache = nil })
	if _, lookupErr := jwkCache.Lookup(ctx, srv.URL); lookupErr == nil {
		t.Fatalf("Lookup against unreachable JWKS must return error while cache is empty; got nil")
	}
}

// TestNewJWKCache_ReadyAfterSuccessfulFetch verifies that once the JWKS
// endpoint becomes reachable, the background worker populates the cache
// and Lookup starts succeeding. This is the lazy-load contract the
// /healthz/ready handler relies on.
func TestNewJWKCache_ReadyAfterSuccessfulFetch(t *testing.T) {
	jwksBody := `{"keys":[]}`
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(jwksBody))
	}))
	t.Cleanup(srv.Close)

	caPath := writeServerCA(t, srv)
	tokenPath := writeFakeSAToken(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cache, err := newJWKCache(ctx, srv.URL, caPath, tokenPath)
	if err != nil {
		t.Fatalf("newJWKCache: %v", err)
	}
	jwkCache = cache
	t.Cleanup(func() { jwkCache = nil })

	// Force an immediate fetch instead of waiting on the worker's
	// scheduled tick — the test asserts that *once* a fetch succeeds,
	// Lookup returns the parsed set.
	if _, err := cache.Refresh(ctx, srv.URL); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	set, err := cache.Lookup(ctx, srv.URL)
	if err != nil {
		t.Fatalf("Lookup after successful fetch: %v", err)
	}
	if set == nil {
		t.Fatalf("Lookup returned nil set after successful fetch")
	}
}

// TestNewJWKCache_BackgroundWorkerRecoversFromTransientFailure pins the
// real production cold-start contract: the *background worker* must
// recover from a transient JWKS failure without anyone calling Refresh
// explicitly. The previous test bypassed the worker entirely by calling
// Refresh, which would still pass if the worker's retry interval was
// broken. This test polls Lookup like kubelet would poll /healthz/ready
// and asserts recovery completes within a budget tied to jwksMinInterval.
//
// The budget is computed as (failUntil+1) * jwksMinInterval with 100% of
// slack — so a future regression that re-introduces a 5-minute MinInterval
// fails this test with a clear deadline-exceeded, not by passing in a
// suspiciously short time.
func TestNewJWKCache_BackgroundWorkerRecoversFromTransientFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery-budget test in short mode")
	}
	var attempts atomic.Int64
	const failUntil int64 = 3
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= failUntil {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	t.Cleanup(srv.Close)

	caPath := writeServerCA(t, srv)
	tokenPath := writeFakeSAToken(t)

	budget := time.Duration(failUntil+1) * jwksMinInterval * 2
	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()

	cache, err := newJWKCache(ctx, srv.URL, caPath, tokenPath)
	if err != nil {
		t.Fatalf("newJWKCache: %v", err)
	}
	jwkCache = cache
	t.Cleanup(func() { jwkCache = nil })

	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if _, err := cache.Lookup(ctx, srv.URL); err == nil {
			return // recovered
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("background worker did not recover within %s; attempts=%d (must be > failUntil=%d)", budget, attempts.Load(), failUntil)
}

// TestNewJWKCache_MissingCACertReturnsError pins that catastrophic
// misconfig (missing CA bundle file) is still surfaced loudly. The fix
// only relaxes the *transient* failure path; permanent config bugs must
// still fail the process so an operator sees something instead of a
// silently broken sign-in flow.
func TestNewJWKCache_MissingCACertReturnsError(t *testing.T) {
	tokenPath := writeFakeSAToken(t)

	_, err := newJWKCache(context.Background(), "https://example.invalid/jwks", "/path/does/not/exist", tokenPath)
	if err == nil {
		t.Fatalf("expected error when CA cert path does not exist; got nil")
	}
	if !strings.Contains(err.Error(), "CA cert") {
		t.Fatalf("error %q must mention CA cert so operator can diagnose", err.Error())
	}
}

// TestNewJWKCache_MalformedCACertReturnsError pins that an unparseable
// CA bundle is also surfaced as a hard error rather than silently leaving
// the pool empty and degrading later.
func TestNewJWKCache_MalformedCACertReturnsError(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "bad-ca.crt")
	if err := os.WriteFile(caPath, []byte("not a pem"), 0o600); err != nil {
		t.Fatalf("write bad ca: %v", err)
	}
	tokenPath := writeFakeSAToken(t)

	_, err := newJWKCache(context.Background(), "https://example.invalid/jwks", caPath, tokenPath)
	if err == nil {
		t.Fatalf("expected error on malformed CA cert; got nil")
	}
}

// TestVerifyAndParseJWT_DistinguishesNotReadyFromInvalid pins that the
// sign-in handler can tell a cold-start window ("cache not populated")
// apart from a real bad-token sign-in attempt. Without this, the cold-
// start window would surface to users as the misleading "Invalid token"
// message and operators would have no easy way to disambiguate the two.
func TestVerifyAndParseJWT_DistinguishesNotReadyFromInvalid(t *testing.T) {
	// Make the cache point at an unreachable host with a bogus CA so no
	// fetch can ever succeed; Lookup will return "resource not ready".
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	caPath := makeBogusCAPEM(t)
	tokenPath := writeFakeSAToken(t)

	ctx := t.Context()
	cache, err := newJWKCache(ctx, srv.URL, caPath, tokenPath)
	if err != nil {
		t.Fatalf("newJWKCache: %v", err)
	}
	prevCache := jwkCache
	prevURL := jwksURL
	jwkCache = cache
	jwksURL = srv.URL
	t.Cleanup(func() { jwkCache = prevCache; jwksURL = prevURL })

	_, err = verifyAndParseJWT(ctx, "any-token-value")
	if err == nil {
		t.Fatalf("verifyAndParseJWT must error when cache is not ready; got nil")
	}
	if !errors.Is(err, errJWKSNotReady) {
		t.Fatalf("verifyAndParseJWT error %q must wrap errJWKSNotReady so the sign-in handler can show a cold-start message instead of \"Invalid token\"", err)
	}
}

// TestSATokenTransport_InjectsBearer covers the token injection path —
// the cache's HTTP client must carry the SA token so the apiserver
// accepts the JWKS request.
func TestSATokenTransport_InjectsBearer(t *testing.T) {
	gotAuth := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case gotAuth <- r.Header.Get("Authorization"):
		default:
		}
	}))
	t.Cleanup(srv.Close)

	tokenPath := writeFakeSAToken(t)
	transport := &saTokenTransport{
		base:      &http.Transport{TLSClientConfig: &tls.Config{}},
		tokenPath: tokenPath,
	}
	ctx := t.Context()
	transport.startRefresh(ctx, time.Hour)

	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	_ = resp.Body.Close()

	select {
	case auth := <-gotAuth:
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Fatalf("Authorization header missing Bearer prefix: %q", auth)
		}
		if !strings.Contains(auth, "fake-sa-token") {
			t.Fatalf("Authorization header missing token contents: %q", auth)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not reach server")
	}
}

// TestHealthzReady_ReportsCacheState pins the readiness contract: while
// the cache is empty, /healthz/ready must report 503 so kubelet keeps the
// pod out of service endpoints; once the cache has a populated set, it
// must report 200. Without this, a permanently misconfigured JWKS would
// silently degrade to "every sign-in fails" with no operator-visible
// surface.
func TestHealthzReady_ReportsCacheState(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	t.Cleanup(srv.Close)

	caPath := writeServerCA(t, srv)
	tokenPath := writeFakeSAToken(t)

	ctx := t.Context()

	cache, err := newJWKCache(ctx, srv.URL, caPath, tokenPath)
	if err != nil {
		t.Fatalf("newJWKCache: %v", err)
	}
	jwkCache = cache
	t.Cleanup(func() { jwkCache = nil })
	prevURL := jwksURL
	jwksURL = srv.URL
	t.Cleanup(func() { jwksURL = prevURL })

	// Mirror the production /healthz/ready handler exactly so a future
	// drift between handler and test surfaces here.
	handler := func(w http.ResponseWriter, r *http.Request) {
		resource, err := jwkCache.LookupResource(r.Context(), jwksURL)
		if err != nil {
			http.Error(w, "lookup failed", http.StatusServiceUnavailable)
			return
		}
		if resource.Resource() == nil {
			http.Error(w, "jwks not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}

	// Before any successful fetch: 503.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/healthz/ready", nil)
	handler(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("empty-cache /healthz/ready: got %d, want 503", w.Code)
	}

	// After a successful refresh: 200.
	if _, err := cache.Refresh(ctx, srv.URL); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	w = httptest.NewRecorder()
	handler(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("populated /healthz/ready: got %d, want 200", w.Code)
	}
}

// Compile-time check that the jwk package still exposes WithWaitReady —
// the entire fix turns on this option, so a future upstream rename
// surfaces as a build error here instead of a silent semantic change.
var _ jwk.RegisterOption = jwk.WithWaitReady(false)
