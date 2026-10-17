package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/securecookie"
)

func testCodec() *securecookie.SecureCookie {
	return securecookie.New([]byte("0123456789abcdef0123456789abcdef"), nil)
}

// The handlers read the session back with sc.Decode; the field types they
// get decide whether their type assertions hold.
func TestEncodeSessionRoundTrip(t *testing.T) {
	sc := testCodec()
	enc, err := encodeSession(sc, "tok", 2000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var sess map[string]any
	if err := sc.Decode(cookieName, enc, &sess); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sess["access_token"] != "tok" {
		t.Errorf("access_token = %v", sess["access_token"])
	}
	if sess["expires"] != int64(2000) || sess["issued"] != int64(1000) {
		t.Errorf("expires, issued = %#v, %#v; want int64 2000, 1000", sess["expires"], sess["issued"])
	}
}

func TestEncodeSessionWithoutSecretIsTheBareToken(t *testing.T) {
	enc, err := encodeSession(nil, "tok", 2000, 1000)
	if err != nil || enc != "tok" {
		t.Fatalf("encodeSession(nil) = %q, %v; want the token", enc, err)
	}
}

func TestSessionCookieRejectsTampering(t *testing.T) {
	sc := testCodec()
	enc, err := encodeSession(sc, "tok", 2000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	b := []byte(enc)
	b[len(b)/2] ^= 1
	var sess map[string]any
	if err := sc.Decode(cookieName, string(b), &sess); err == nil {
		t.Fatalf("tampered cookie decoded: %v", sess)
	}
	other := securecookie.New([]byte("fedcba9876543210fedcba9876543210"), nil)
	if err := other.Decode(cookieName, enc, &sess); err == nil {
		t.Fatal("cookie signed with another secret decoded")
	}
}

func TestSessionCookieRejectsExpiredTimestamp(t *testing.T) {
	enc, err := encodeSession(testCodec(), "tok", 2000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	// A negative max age makes every timestamp too old.
	var sess map[string]any
	if err := testCodec().MaxAge(-1).Decode(cookieName, enc, &sess); err == nil {
		t.Fatal("expired cookie decoded")
	}
}

func TestDecodeSessionWithoutSecretTakesTheBareToken(t *testing.T) {
	now := time.Unix(5000, 0)
	token, sess, err := decodeSession(nil, "tok", now)
	if err != nil || token != "tok" {
		t.Fatalf("decodeSession(nil) = %q, %v", token, err)
	}
	if sess["issued"] != now.Unix() || sess["expires"] != now.Add(24*time.Hour).Unix() {
		t.Errorf("session = %v", sess)
	}
}

func TestDecodeSessionRejectsForgedCookie(t *testing.T) {
	if _, _, err := decodeSession(testCodec(), "tok", time.Now()); err == nil {
		t.Fatal("a bare token decoded as a signed session")
	}
}

func signedSession(t *testing.T, sc *securecookie.SecureCookie, expires, issued time.Time) (string, map[string]any) {
	t.Helper()
	enc, err := encodeSession(sc, "tok", expires.Unix(), issued.Unix())
	if err != nil {
		t.Fatal(err)
	}
	token, sess, err := decodeSession(sc, enc, issued)
	if err != nil || token != "tok" {
		t.Fatalf("decodeSession = %q, %v", token, err)
	}
	return token, sess
}

// A refreshed cookie gets a new issued time and keeps the token's expiry, so
// refreshing never lets the cookie outlive the token.
func TestRefreshedCookieReissuesASignedSessionUntilTokenExpiry(t *testing.T) {
	sc := testCodec()
	now := time.Now()
	expires := now.Add(time.Hour)
	token, sess := signedSession(t, sc, expires, now.Add(-2*time.Hour))

	c := refreshedCookie(sc, token, sess, time.Hour, now)
	if c == nil {
		t.Fatal("refreshedCookie = nil for a session older than the refresh interval")
	}
	if c.Expires.Unix() != expires.Unix() {
		t.Errorf("cookie Expires = %v, want the token expiry %v", c.Expires, expires)
	}
	if c.Name != cookieName || c.Path != "/" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie = %+v, want %s on / HttpOnly SameSite=Lax", c, cookieName)
	}
	_, got, err := decodeSession(sc, c.Value, now)
	if err != nil {
		t.Fatalf("decode refreshed cookie: %v", err)
	}
	if got["issued"] != now.Unix() || got["expires"] != expires.Unix() {
		t.Errorf("refreshed session = %v, want issued %d and expires %d", got, now.Unix(), expires.Unix())
	}
}

func TestRefreshedCookieWaitsForTheInterval(t *testing.T) {
	sc := testCodec()
	now := time.Now()
	token, sess := signedSession(t, sc, now.Add(time.Hour), now.Add(-30*time.Minute))
	if c := refreshedCookie(sc, token, sess, time.Hour, now); c != nil {
		t.Errorf("refreshed a session younger than the interval: %+v", c)
	}
	token, sess = signedSession(t, sc, now.Add(time.Hour), now.Add(-2*time.Hour))
	if c := refreshedCookie(sc, token, sess, 0, now); c != nil {
		t.Errorf("refreshed with refresh disabled: %+v", c)
	}
}

// securecookie refuses a value longer than its MaxLength, which a large token
// reaches; no half-written cookie may come out of that.
func TestSessionCookieReportsAnEncodingFailure(t *testing.T) {
	sc := testCodec().MaxLength(64)
	if c, err := sessionCookie(sc, strings.Repeat("t", 512), time.Now().Add(time.Hour).Unix(), time.Now().Unix()); err == nil {
		t.Fatalf("sessionCookie = %+v, want an error", c)
	}
}

func TestRefreshedCookieSkipsASessionItCannotEncode(t *testing.T) {
	now := time.Now()
	token, sess := signedSession(t, testCodec(), now.Add(time.Hour), now.Add(-2*time.Hour))
	if c := refreshedCookie(testCodec().MaxLength(64), token, sess, time.Hour, now); c != nil {
		t.Fatalf("refreshedCookie = %+v, want nil when the session cannot be encoded", c)
	}
}
