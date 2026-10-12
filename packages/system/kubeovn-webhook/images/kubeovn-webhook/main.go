package main

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

var (
	PortSecurityGlobal bool
	RoutesGlobal       string
)

// Observation state for logClientCert.
//
// Deliberately NOT a set keyed on the issuer. With tls.RequestClientCert the
// issuer string is supplied by the caller, so anything keyed on it is both
// unbounded and forgeable: remembering every value grows without limit, and
// remembering only the first N lets anyone who can reach the port fill those N
// with fabricated issuers before the API server's first call and permanently
// suppress the signal this exists to produce. Rate limiting cannot be starved
// that way -- a burst costs the attacker at most the current interval, and the
// next real handshake is logged in the one after it.
//
// The two outcomes are limited separately so a flood of one cannot hide the
// other: "no certificate" and "certificate presented" answer different
// questions, and it is the pair that decides whether enforcement is safe.
const observationInterval = 30 * time.Second

type observationLimiter struct {
	mu   sync.Mutex
	last time.Time
	seen bool
}

// allow reports whether this observation should be logged: always the first
// one, then at most one per observationInterval.
func (l *observationLimiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen && now.Sub(l.last) < observationInterval {
		return false
	}
	l.seen = true
	l.last = now
	return true
}

var (
	noCertObservations   observationLimiter
	withCertObservations observationLimiter
)

// logClientCert reports, once per distinct issuer, whether the caller presented
// a client certificate and who signed it.
//
// What it reports is untrusted. With tls.RequestClientCert the certificate is
// accepted without being verified against anything, so the issuer is a claim
// and not an identity: it says what reaches this port, and feeds a decision a
// human makes out of band. Nothing here turns it into a trust decision the
// process makes for itself — enforcement depends solely on --client-ca-file.
//
// This is the evidence needed to decide whether that enforcement can be turned
// on. The handler serves namespace-derived values to whoever asks
// (see GHSA-g883-q79m-8225), and the fix is to accept only the API server — but
// the API server presents a certificate only when the cluster is configured for
// it (an AdmissionConfiguration with a kubeConfigFile), which is not universal
// across the variants this ships on. Switching straight to
// RequireAndVerifyClientCert on a cluster that sends nothing would fail every
// admission call, and this webhook is registered failurePolicy: Fail — so pod
// creation would stop cluster-wide. Observe first, enforce second.
func logClientCert(enforcing bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer := ""
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			issuer = r.TLS.PeerCertificates[0].Issuer.String()
		}
		now := time.Now()
		switch {
		case issuer == "" && noCertObservations.allow(now):
			log.Printf("client certificate: none presented by %s -- client-ca-file enforcement is NOT yet safe on this cluster", r.RemoteAddr)
		case issuer != "" && withCertObservations.allow(now):
			if enforcing {
				log.Printf("client certificate: presented by %s, issuer %q -- verified against --client-ca-file during the handshake", r.RemoteAddr, issuer)
			} else {
				log.Printf("client certificate: presented by %s, issuer %q (UNVERIFIED -- nothing has checked this certificate, and any caller can claim any issuer). Confirm out of band that this is your API server's CA before pinning it with --client-ca-file", r.RemoteAddr, issuer)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	var (
		tlsCertFile  string
		tlsKeyFile   string
		clientCAFile string
	)

	flag.StringVar(&tlsCertFile, "tls-cert-file", "/etc/webhook/certs/tls.crt", "TLS certificate file.")
	flag.StringVar(&tlsKeyFile, "tls-key-file", "/etc/webhook/certs/tls.key", "TLS key file.")
	flag.StringVar(&clientCAFile, "client-ca-file", "", "CA bundle for verifying client certificates. When set, callers MUST present a certificate signed by it; when empty, certificates are requested and logged but not required.")
	flag.BoolVar(&PortSecurityGlobal, "port-security", true, "If false, skip adding port_security unless specified by the Namespace.")
	flag.StringVar(&RoutesGlobal, "routes", "", "Default ovn.kubernetes.io/routes if not in Namespace.")

	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/mutate-pods", HandleMutatePods)

	tlsConfig, err := newReloadingTLSConfig(tlsCertFile, tlsKeyFile)
	if err != nil {
		log.Fatalf("Failed to load key pair: %v", err)
	}

	tlsConfig.MinVersion = tls.VersionTLS12

	// Default: ask for a client certificate, accept the connection either way.
	// This never rejects a caller, so it cannot break admission; it only makes
	// the certificate visible to logClientCert.
	tlsConfig.ClientAuth = tls.RequestClientCert

	if clientCAFile != "" {
		clientCAPool := x509.NewCertPool()
		clientCAData, err := os.ReadFile(clientCAFile)
		if err != nil {
			log.Fatalf("Failed to read client CA file: %v", err)
		}
		if !clientCAPool.AppendCertsFromPEM(clientCAData) {
			log.Fatalf("Failed to parse client CA certificate")
		}
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		tlsConfig.ClientCAs = clientCAPool
		log.Printf("mTLS enabled: requiring client certificates signed by %s", clientCAFile)
	} else {
		log.Printf("mTLS not enforced (--client-ca-file empty): client certificates are requested and logged only")
	}

	server := &http.Server{
		Addr:              ":8443",
		TLSConfig:         tlsConfig,
		Handler:           logClientCert(clientCAFile != "", mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("Starting webhook server on %s", server.Addr)
	if err := server.ListenAndServeTLS("", ""); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
