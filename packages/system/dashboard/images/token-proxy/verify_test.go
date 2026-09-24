package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

const testKid = "k1"

// serveJWKS points the package-level cache at a TLS server publishing pub
// the way kube-apiserver does, and waits until the set is loaded.
func serveJWKS(t *testing.T, pub *rsa.PublicKey) {
	t.Helper()
	key, err := jwk.Import(pub)
	if err != nil {
		t.Fatalf("import public key: %v", err)
	}
	for k, v := range map[string]any{jwk.KeyIDKey: testKid, jwk.AlgorithmKey: jwa.RS256(), jwk.KeyUsageKey: "sig"} {
		if err := key.Set(k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
	set := jwk.NewSet()
	if err := set.AddKey(key); err != nil {
		t.Fatalf("add key: %v", err)
	}
	body, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/jwk-set+json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	ctx := t.Context()
	cache, err := newJWKCache(ctx, srv.URL, writeServerCA(t, srv), writeFakeSAToken(t))
	if err != nil {
		t.Fatalf("newJWKCache: %v", err)
	}
	if _, err := cache.Refresh(ctx, srv.URL); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	prevCache, prevURL := jwkCache, jwksURL
	jwkCache, jwksURL = cache, srv.URL
	t.Cleanup(func() { jwkCache, jwksURL = prevCache, prevURL })
}

func newRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	return k
}

func claims(t *testing.T, notBefore, expiry time.Time) jwt.Token {
	t.Helper()
	tok, err := jwt.NewBuilder().
		Subject("system:serviceaccount:tenant-root:dashboard").
		IssuedAt(notBefore).
		NotBefore(notBefore).
		Expiration(expiry).
		Build()
	if err != nil {
		t.Fatalf("build claims: %v", err)
	}
	return tok
}

// sign signs tok with alg and key, putting kid in the protected header
// unless kid is empty.
func sign(t *testing.T, tok jwt.Token, alg jwa.SignatureAlgorithm, key any, kid string) string {
	t.Helper()
	hdr := jws.NewHeaders()
	if kid != "" {
		if err := hdr.Set(jws.KeyIDKey, kid); err != nil {
			t.Fatalf("set kid: %v", err)
		}
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(alg, key, jws.WithProtectedHeaders(hdr)))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return string(signed)
}

func TestVerifyAndParseJWT_AcceptsValidToken(t *testing.T) {
	priv := newRSAKey(t)
	serveJWKS(t, &priv.PublicKey)
	now := time.Now()
	exp := now.Add(time.Hour).Truncate(time.Second)

	tok, err := verifyAndParseJWT(t.Context(), sign(t, claims(t, now.Add(-time.Minute), exp), jwa.RS256(), priv, testKid))
	if err != nil {
		t.Fatalf("a token signed by the published key must verify: %v", err)
	}
	if got, ok := tok.Expiration(); !ok || !got.Equal(exp) {
		t.Fatalf("expiration = %v (present %v), want %v", got, ok, exp)
	}
	if got := getClaim(tok, "sub"); got != "system:serviceaccount:tenant-root:dashboard" {
		t.Fatalf("sub claim = %v", got)
	}
}

// Every token below must be refused, and refused as invalid rather than as
// the cold-start errJWKSNotReady, which the sign-in page words differently.
func TestVerifyAndParseJWT_RejectsInvalidTokens(t *testing.T) {
	priv := newRSAKey(t)
	serveJWKS(t, &priv.PublicKey)
	now := time.Now()
	valid := func() jwt.Token { return claims(t, now.Add(-time.Minute), now.Add(time.Hour)) }

	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	payload, err := json.Marshal(valid())
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	unsigned := b64([]byte(`{"alg":"none","kid":"`+testKid+`"}`)) + "." + b64(payload) + "."

	cases := map[string]string{
		"expired":          sign(t, claims(t, now.Add(-2*time.Hour), now.Add(-time.Hour)), jwa.RS256(), priv, testKid),
		"not yet valid":    sign(t, claims(t, now.Add(time.Hour), now.Add(2*time.Hour)), jwa.RS256(), priv, testKid),
		"foreign key":      sign(t, valid(), jwa.RS256(), newRSAKey(t), testKid),
		"unknown kid":      sign(t, valid(), jwa.RS256(), priv, "k2"),
		"no kid":           sign(t, valid(), jwa.RS256(), priv, ""),
		"alg none":         unsigned,
		"hs256 public key": sign(t, valid(), jwa.HS256(), pubDER, testKid),
		"not a jwt":        "not-a-jwt",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := verifyAndParseJWT(t.Context(), raw)
			if err == nil {
				t.Fatal("token must be rejected")
			}
			if errors.Is(err, errJWKSNotReady) {
				t.Fatalf("rejection reported as cold start: %v", err)
			}
		})
	}
}
