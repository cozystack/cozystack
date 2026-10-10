package main

import (
	"testing"

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
