package main

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureLog redirects the standard logger for the duration of fn and returns
// what was written.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf strings.Builder
	flags := log.Flags()
	out := log.Writer()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(out)
		log.SetFlags(flags)
	}()
	fn()
	return buf.String()
}

// resetObservations clears the package-level limiters between cases.
func resetObservations() {
	noCertObservations = observationLimiter{}
	withCertObservations = observationLimiter{}
}

func TestObservationLimiterAllowsFirstThenRateLimits(t *testing.T) {
	var l observationLimiter
	base := time.Now()

	if !l.allow(base) {
		t.Fatal("first observation must always be logged")
	}
	if l.allow(base.Add(observationInterval - time.Nanosecond)) {
		t.Error("a second observation inside the interval must be suppressed")
	}
	if !l.allow(base.Add(observationInterval)) {
		t.Error("an observation at the interval boundary must be logged again")
	}
}

// A burst of fabricated issuers is the attack the rate limiter exists to
// survive: it may cost the current interval, but it must not suppress
// observation permanently the way a first-come set of issuers would.
func TestObservationSurvivesBurstOfFabricatedIssuers(t *testing.T) {
	var l observationLimiter
	base := time.Now()

	for i := 0; i < 1000; i++ {
		l.allow(base)
	}
	if !l.allow(base.Add(observationInterval)) {
		t.Error("observation must resume after the interval, whatever the burst")
	}
}

func TestObservationLimiterIsConcurrencySafe(t *testing.T) {
	var l observationLimiter
	now := time.Now()

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.allow(now) {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowed != 1 {
		t.Errorf("exactly one concurrent observation may pass at a fixed instant, got %d", allowed)
	}
}

// request builds a request carrying either no peer certificate or one issued by
// the named issuer.
func request(issuer string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/mutate-pods", nil)
	r.RemoteAddr = "10.0.0.7:34512"
	if issuer != "" {
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{
			{Issuer: pkix.Name{CommonName: issuer}},
		}}
	}
	return r
}

func TestLogClientCertNoCertificate(t *testing.T) {
	resetObservations()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	out := captureLog(t, func() {
		logClientCert(false, next).ServeHTTP(httptest.NewRecorder(), request(""))
	})

	if !strings.Contains(out, "none presented") {
		t.Errorf("a certless caller must be reported, got %q", out)
	}
	if !strings.Contains(out, "10.0.0.7:34512") {
		t.Errorf("the peer address must be logged so the caller can be attributed, got %q", out)
	}
}

func TestLogClientCertUnverifiedWhenNotEnforcing(t *testing.T) {
	resetObservations()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	out := captureLog(t, func() {
		logClientCert(false, next).ServeHTTP(httptest.NewRecorder(), request("some-ca"))
	})

	if !strings.Contains(out, "UNVERIFIED") {
		t.Errorf("without enforcement the issuer is a claim and must be marked unverified, got %q", out)
	}
	if !strings.Contains(out, "some-ca") {
		t.Errorf("the issuer must appear in the log, got %q", out)
	}
}

func TestLogClientCertVerifiedWhenEnforcing(t *testing.T) {
	resetObservations()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	out := captureLog(t, func() {
		logClientCert(true, next).ServeHTTP(httptest.NewRecorder(), request("some-ca"))
	})

	if strings.Contains(out, "UNVERIFIED") {
		t.Errorf("with enforcement on the handshake has verified the chain; the warning is wrong, got %q", out)
	}
	if !strings.Contains(out, "verified against --client-ca-file") {
		t.Errorf("the log should say the certificate was verified, got %q", out)
	}
}

// The two outcomes are limited separately, so a caller flooding one branch
// cannot hide the other. That pairing is what decides whether enforcement is
// safe, so losing either half would defeat the observation.
func TestLogClientCertBranchesAreLimitedIndependently(t *testing.T) {
	resetObservations()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	h := logClientCert(false, next)

	out := captureLog(t, func() {
		for i := 0; i < 20; i++ {
			h.ServeHTTP(httptest.NewRecorder(), request("flooding-ca"))
		}
		h.ServeHTTP(httptest.NewRecorder(), request(""))
	})

	// "none presented by ..." also contains "presented by", so key on the
	// issuer field, which only the certificate branch emits.
	if strings.Count(out, ", issuer ") != 1 {
		t.Errorf("the flooded branch must be rate limited to one line, got %q", out)
	}
	if !strings.Contains(out, "none presented") {
		t.Errorf("the other branch must still be reported despite the flood, got %q", out)
	}
}

// The handler is observation only: it must never interfere with admission.
func TestLogClientCertAlwaysCallsNext(t *testing.T) {
	resetObservations()
	called := 0
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called++ })
	h := logClientCert(false, next)

	captureLog(t, func() {
		h.ServeHTTP(httptest.NewRecorder(), request(""))
		h.ServeHTTP(httptest.NewRecorder(), request("some-ca"))
		h.ServeHTTP(httptest.NewRecorder(), request("some-ca"))
	})

	if called != 3 {
		t.Errorf("every request must reach the handler regardless of logging, got %d of 3", called)
	}
}
