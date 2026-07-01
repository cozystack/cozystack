package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/securecookie"
	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/httprc/v3/errsink"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

/* ----------------------------- flags ------------------------------------ */

var (
	upstream, httpAddr, proxyPrefix string
	cookieName, cookieSecretB64     string
	cookieSecure                    bool
	cookieRefresh                   time.Duration
	jwksURL                         string
	saTokenPath                     string
	saCACertPath                    string
)

func init() {
	flag.StringVar(&upstream, "upstream", "", "Upstream URL to proxy to (required)")
	flag.StringVar(&httpAddr, "http-address", "0.0.0.0:8000", "Listen address")
	flag.StringVar(&proxyPrefix, "proxy-prefix", "/oauth2", "URL prefix for control endpoints")

	flag.StringVar(&cookieName, "cookie-name", "_oauth2_proxy_0", "Cookie name")
	flag.StringVar(&cookieSecretB64, "cookie-secret", "", "Base64-encoded cookie secret")
	flag.BoolVar(&cookieSecure, "cookie-secure", false, "Set Secure flag on cookie")
	flag.DurationVar(&cookieRefresh, "cookie-refresh", 0, "Cookie refresh interval (e.g. 1h)")
	flag.StringVar(&jwksURL, "jwks-url", "https://kubernetes.default.svc/openid/v1/jwks", "JWKS URL for token verification")
	flag.StringVar(&saTokenPath, "sa-token-path", "/var/run/secrets/kubernetes.io/serviceaccount/token", "Path to service account token")
	flag.StringVar(&saCACertPath, "sa-ca-cert-path", "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt", "Path to service account CA certificate")
}

// newJWKCache builds the JWK cache used to verify Kubernetes-issued JWTs.
// Returns an error rather than panicking so callers can handle catastrophic
// misconfig (missing CA bundle, malformed URL) loudly while leaving transient
// JWKS unreachability to the cache's own background retry loop.
func newJWKCache(ctx context.Context, url, caCertPath, tokenPath string) (*jwk.Cache, error) {
	caCert, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA cert: %w", err)
	}
	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA cert")
	}

	transport := &saTokenTransport{
		base: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: caCertPool,
			},
		},
		tokenPath: tokenPath,
	}
	transport.startRefresh(ctx, 5*time.Minute)

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}

	// jwx v3 / httprc v3 regression: jwk.Cache.Register *always* overrides
	// the HTTPClient for each Resource. The client from httprc.NewClient is
	// ignored; without an explicit jwk.WithHTTPClient on Register, jwk falls
	// back to its own plain http.Client which has neither our cluster CA nor
	// the SA bearer token, so the in-cluster JWKS fetch fails TLS verification
	// and the controller's Ready() never signals — Register blocks forever.
	// See jwx@v3.1.0/jwk/cache.go:141-180.
	//
	// WithErrorSink wires background fetch failures into the container log.
	// Without this, a persistent JWKS misconfig (wrong URL, broken RBAC,
	// permanent 5xx from apiserver) would be invisible to operators —
	// /healthz/ready would surface the symptom but the underlying error
	// would never reach stdout.
	cache, err := jwk.NewCache(ctx, httprc.NewClient(httprc.WithErrorSink(errsink.NewFunc(
		func(_ context.Context, e error) { log.Printf("jwks background fetch failed: %v", e) },
	))))
	if err != nil {
		return nil, fmt.Errorf("failed to create JWK cache: %w", err)
	}

	// WithWaitReady(false) prevents Register from blocking on r.Ready() until
	// the first fetch succeeds. The default behavior has no built-in timeout,
	// so a brief cold-start unreachability of kubernetes.default.svc (cluster
	// DNS warmup, CNI not yet up) would block this call indefinitely; the old
	// code wrapped that block with a panic that turned every transient cold
	// start into a CrashLoopBackOff.
	//
	// MinInterval=5s is the retry floor honored by httprc both when the
	// HTTP request returns a non-OK status (apiserver 5xx, RBAC 403 — no
	// Cache-Control header, so the next-fetch time falls back to
	// MinInterval) and when the request itself errors (DNS, dial, TLS;
	// Sync skips SetNext, and periodicCheck falls back to MinInterval on
	// the next tick). In steady state the actual polling cadence is
	// determined by the apiserver's Cache-Control header, further capped
	// by MaxInterval=15m. The 5s floor trades a small steady-state
	// polling overhead for fast cold-start recovery; an earlier 5m value
	// left the pod NotReady for up to five minutes after a single bad
	// response.
	//
	// verifyAndParseJWT surfaces an errJWKSNotReady sentinel until the
	// cache is populated, and /healthz/ready reflects the same state so
	// kubelet keeps the pod out of service endpoints until JWKS is
	// reachable.
	if err := cache.Register(ctx, url,
		jwk.WithHTTPClient(httpClient),
		jwk.WithMinInterval(jwksMinInterval),
		jwk.WithMaxInterval(jwksMaxInterval),
		jwk.WithWaitReady(false),
	); err != nil {
		return nil, fmt.Errorf("failed to register JWKS URL: %w", err)
	}

	return cache, nil
}

/* ----------------------------- templates -------------------------------- */

var loginTmpl = template.Must(template.New("login").Parse(`
<!doctype html>
<html lang="en">
<head>
	<meta charset="UTF-8">
	<title>Login</title>
	<style>
		body {
			margin: 0;
			height: 100vh;
			display: flex;
			align-items: center;
			justify-content: center;
			background: #f4f6f8;
			font-family: Arial, sans-serif;
		}
		.card {
			background: #fff;
			padding: 2rem;
			border-radius: 12px;
			box-shadow: 0 4px 20px rgba(0,0,0,0.1);
			width: 400px;
			text-align: center;
		}
		h2 {
			margin-bottom: 1rem;
			color: #333;
		}
		input {
			width: 100%;
			padding: 0.8rem;
			margin-bottom: 1rem;
			border: 1px solid #ccc;
			border-radius: 8px;
			font-size: 1rem;
			transition: border 0.3s;
		}
		input:focus {
			outline: none;
			border-color: #4a90e2;
		}
		button {
			width: 100%;
			padding: 0.8rem;
			background: #4a90e2;
			color: white;
			font-size: 1rem;
			font-weight: bold;
			border: none;
			border-radius: 8px;
			cursor: pointer;
			transition: background 0.3s;
		}
		button:hover {
			background: #357ABD;
		}
		.error {
			color: #e74c3c;
			margin-bottom: 1rem;
		}
	</style>
</head>
<body>
	<div class="card">
		<h2>Kubernetes API Token</h2>
		{{if .Err}}<p class="error">{{.Err}}</p>{{end}}
		<form method="POST" action="{{.Action}}">
			<input type="text" name="token" placeholder="Paste token here" autofocus />
			<button type="submit">Login</button>
		</form>
	</div>
</body>
</html>`))

/* ----------------------------- JWK cache -------------------------------- */

var (
	jwkCache *jwk.Cache
)

// saTokenTransport adds the service account token to requests and refreshes it periodically.
type saTokenTransport struct {
	base      http.RoundTripper
	tokenPath string
	mu        sync.RWMutex
	token     string
}

func (t *saTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.RLock()
	token := t.token
	t.mu.RUnlock()

	if token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return t.base.RoundTrip(req)
}

func (t *saTokenTransport) refreshToken() {
	data, err := os.ReadFile(t.tokenPath)
	if err != nil {
		log.Printf("warning: failed to read SA token: %v", err)
		return
	}
	t.mu.Lock()
	t.token = string(data)
	t.mu.Unlock()
}

func (t *saTokenTransport) startRefresh(ctx context.Context, interval time.Duration) {
	t.refreshToken()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				t.refreshToken()
			}
		}
	}()
}

/* ----------------------------- helpers ---------------------------------- */

// jwksMinInterval is the retry floor on background JWKS fetches; see the
// long comment in newJWKCache. Exposed as a named constant so the
// background-recovery test can compute its deadline budget from the same
// value rather than hard-coding 30s, which would silently drift if the
// interval ever changed.
const jwksMinInterval = 5 * time.Second

// jwksMaxInterval caps the steady-state polling cadence regardless of
// what Cache-Control max-age the apiserver advertises.
const jwksMaxInterval = 15 * time.Minute

// errJWKSNotReady is returned by verifyAndParseJWT when the JWK cache has
// been registered but the background worker has not yet completed its
// first successful fetch. The sign-in handler distinguishes it from a
// genuinely invalid token so the cold-start window between pod start and
// the first successful JWKS fetch surfaces as "service starting, retry"
// rather than the misleading "invalid token".
var errJWKSNotReady = errors.New("jwks not ready")

// verifyAndParseJWT verifies the token signature and returns the parsed token.
//
// LookupResource is used instead of Lookup so the not-ready state (cache
// registered, resource set still nil) is detected by a typed nil check
// rather than by string-matching jwk.Cache.Lookup's "resource %q is not
// ready" error, which jwx returns as a bare fmt.Errorf without a
// sentinel to errors.Is against.
func verifyAndParseJWT(ctx context.Context, raw string) (jwt.Token, error) {
	if raw == "" {
		return nil, fmt.Errorf("empty token")
	}

	resource, err := jwkCache.LookupResource(ctx, jwksURL)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup JWKS resource: %w", err)
	}
	keySet := resource.Resource()
	if keySet == nil {
		return nil, errJWKSNotReady
	}

	token, err := jwt.Parse([]byte(raw), jwt.WithKeySet(keySet))
	if err != nil {
		return nil, fmt.Errorf("failed to verify token: %w", err)
	}

	return token, nil
}

// getClaim extracts a claim value from a verified token.
func getClaim(token jwt.Token, key string) any {
	if token == nil {
		return nil
	}
	var val any
	if err := token.Get(key, &val); err != nil {
		return nil
	}
	return val
}

func encodeSession(sc *securecookie.SecureCookie, token string, exp, issued int64) (string, error) {
	v := map[string]any{
		"access_token": token,
		"expires":      exp,
		"issued":       issued,
	}
	if sc != nil {
		return sc.Encode(cookieName, v)
	}
	return token, nil
}

/* ----------------------------- main ------------------------------------- */

func main() {
	flag.Parse()

	if upstream == "" {
		log.Fatal("--upstream is required")
	}
	upURL, err := url.Parse(upstream)
	if err != nil {
		log.Fatalf("invalid upstream url: %v", err)
	}

	if cookieSecretB64 == "" {
		cookieSecretB64 = os.Getenv("COOKIE_SECRET")
	}
	var sc *securecookie.SecureCookie
	if cookieSecretB64 != "" {
		secret, err := base64.StdEncoding.DecodeString(cookieSecretB64)
		if err != nil {
			log.Fatalf("cookie-secret: %v", err)
		}
		sc = securecookie.New(secret, nil)
	} else {
		log.Println("warning: no cookie-secret provided, cookies will be stored unsigned")
	}

	jwkCache, err = newJWKCache(context.Background(), jwksURL, saCACertPath, saTokenPath)
	if err != nil {
		log.Fatalf("jwk cache setup: %v", err)
	}
	log.Printf("JWK cache initialized with JWKS URL: %s", jwksURL)

	// control paths
	signIn := path.Join(proxyPrefix, "sign_in")
	signOut := path.Join(proxyPrefix, "sign_out")
	userInfo := path.Join(proxyPrefix, "userinfo")

	proxy := httputil.NewSingleHostReverseProxy(upURL)

	/* ------------------------- /sign_in ---------------------------------- */

	http.HandleFunc(signIn, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = loginTmpl.Execute(w, struct {
				Action string
				Err    string
			}{Action: signIn})
		case http.MethodPost:
			token := strings.TrimSpace(r.FormValue("token"))
			if token == "" {
				_ = loginTmpl.Execute(w, struct {
					Action string
					Err    string
				}{Action: signIn, Err: "Token required"})
				return
			}

			// Verify token signature using JWKS
			verifiedToken, err := verifyAndParseJWT(r.Context(), token)
			if err != nil {
				log.Printf("token verification failed: %v", err)
				msg := "Invalid token"
				if errors.Is(err, errJWKSNotReady) {
					msg = "Service is starting, please retry in a moment"
				}
				_ = loginTmpl.Execute(w, struct {
					Action string
					Err    string
				}{Action: signIn, Err: msg})
				return
			}

			exp := time.Now().Add(24 * time.Hour).Unix()
			if expTime, ok := verifiedToken.Expiration(); ok && !expTime.IsZero() {
				exp = expTime.Unix()
			}
			session, _ := encodeSession(sc, token, exp, time.Now().Unix())
			http.SetCookie(w, &http.Cookie{
				Name:     cookieName,
				Value:    session,
				Path:     "/",
				Expires:  time.Unix(exp, 0),
				Secure:   cookieSecure,
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
			http.Redirect(w, r, "/", http.StatusSeeOther)
		}
	})

	/* ------------------------- /sign_out --------------------------------- */

	http.HandleFunc(signOut, func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     cookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			Secure:   cookieSecure,
			HttpOnly: true,
		})
		http.Redirect(w, r, signIn, http.StatusSeeOther)
	})

	/* ------------------------- /userinfo --------------------------------- */

	http.HandleFunc(userInfo, func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var token string
		var sess map[string]any
		if sc != nil {
			if err := sc.Decode(cookieName, c.Value, &sess); err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			token, _ = sess["access_token"].(string)
		} else {
			token = c.Value
			sess = map[string]any{
				"expires": time.Now().Add(24 * time.Hour).Unix(),
				"issued":  time.Now().Unix(),
			}
		}

		// Re-verify the token to ensure it's still valid
		verifiedToken, err := verifyAndParseJWT(r.Context(), token)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		out := map[string]any{
			"token":                 token,
			"sub":                   getClaim(verifiedToken, "sub"),
			"email":                 getClaim(verifiedToken, "email"),
			"preferred_username":    getClaim(verifiedToken, "preferred_username"),
			"groups":                getClaim(verifiedToken, "groups"),
			"expires":               sess["expires"],
			"issued":                sess["issued"],
			"cookie_refresh_enable": cookieRefresh > 0,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	/* ------------------------- /healthz ---------------------------------- */
	// Liveness can always be served; the process is alive as soon as we get
	// here. Readiness must reflect JWKS cache state: if the cache has not
	// been populated yet, every sign-in attempt would fail with "Invalid
	// token", so kubelet should keep the pod out of service endpoints until
	// the background worker succeeds. /healthz/live and /healthz/ready are
	// separate paths so a misconfigured JWKS (wrong URL, missing RBAC,
	// permanent unreachability) shows up as 0/1 Ready instead of silently
	// returning generic auth failures to users.

	http.HandleFunc("/healthz/live", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	http.HandleFunc("/healthz/ready", func(w http.ResponseWriter, r *http.Request) {
		resource, err := jwkCache.LookupResource(r.Context(), jwksURL)
		if err != nil {
			http.Error(w, fmt.Sprintf("jwks lookup failed: %v", err), http.StatusServiceUnavailable)
			return
		}
		if resource.Resource() == nil {
			http.Error(w, "jwks not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	/* ----------------------------- proxy --------------------------------- */

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			http.Redirect(w, r, signIn, http.StatusFound)
			return
		}
		var token string
		var sess map[string]any
		if sc != nil {
			if err := sc.Decode(cookieName, c.Value, &sess); err != nil {
				http.Redirect(w, r, signIn, http.StatusFound)
				return
			}
			token, _ = sess["access_token"].(string)
		} else {
			token = c.Value
			sess = map[string]any{
				"expires": time.Now().Add(24 * time.Hour).Unix(),
				"issued":  time.Now().Unix(),
			}
		}
		if token == "" {
			http.Redirect(w, r, signIn, http.StatusFound)
			return
		}

		// cookie refresh
		if cookieRefresh > 0 {
			if issued, ok := sess["issued"].(float64); ok {
				if time.Since(time.Unix(int64(issued), 0)) > cookieRefresh {
					enc, _ := encodeSession(sc, token, int64(sess["expires"].(float64)), time.Now().Unix())
					http.SetCookie(w, &http.Cookie{
						Name:     cookieName,
						Value:    enc,
						Path:     "/",
						Expires:  time.Unix(int64(sess["expires"].(float64)), 0),
						Secure:   cookieSecure,
						HttpOnly: true,
						SameSite: http.SameSiteLaxMode,
					})
				}
			}
		}

		r.Header.Set("Authorization", "Bearer "+token)
		proxy.ServeHTTP(w, r)
	})

	log.Printf("Listening on %s → %s (control prefix %s)", httpAddr, upURL, proxyPrefix)
	if err := http.ListenAndServe(httpAddr, nil); err != nil {
		log.Fatal(err)
	}
}
