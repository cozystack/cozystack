// SPDX-License-Identifier: Apache-2.0

package format

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
)

func TestGeneratePassword(t *testing.T) {
	seen := map[string]bool{}
	for range 64 {
		p, err := GeneratePassword()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if len(p) != PasswordLength {
			t.Fatalf("password %q has length %d, want %d", p, len(p), PasswordLength)
		}
		for _, c := range p {
			if !strings.ContainsRune(passwordAlphabet, c) {
				t.Fatalf("password %q carries %q outside the alphabet", p, c)
			}
		}
		if seen[p] {
			t.Fatalf("password %q generated twice", p)
		}
		seen[p] = true
	}
}

// The verifier must be the canonical PostgreSQL form, since CNPG forwards it
// unchanged and PostgreSQL stores anything else as a plaintext password. The
// keys are recomputed here from the salt the verifier carries with the
// primitives RFC 5802 names, independently of the encoder's own helpers.
func TestEncodeScramSHA256(t *testing.T) {
	const password = "pencil"
	salt, err := base64.StdEncoding.DecodeString("W22ZaJ0SNY7soEsUEjb6gQ==")
	if err != nil {
		t.Fatal(err)
	}
	got, err := encodeScramSHA256(password, salt, 4096)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	re := regexp.MustCompile(`^SCRAM-SHA-256\$(\d+):([A-Za-z0-9+/=]+)\$([A-Za-z0-9+/=]+):([A-Za-z0-9+/=]+)$`)
	m := re.FindStringSubmatch(got)
	if m == nil {
		t.Fatalf("verifier %q is not in the canonical form", got)
	}
	if m[1] != "4096" {
		t.Errorf("iterations %s, want 4096", m[1])
	}
	if m[2] != "W22ZaJ0SNY7soEsUEjb6gQ==" {
		t.Errorf("salt %s, want the input salt", m[2])
	}

	salted, err := pbkdf2.Key(sha256.New, password, salt, 4096, 32)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, salted)
	mac.Write([]byte("Client Key"))
	stored := sha256.Sum256(mac.Sum(nil))
	mac = hmac.New(sha256.New, salted)
	mac.Write([]byte("Server Key"))
	server := mac.Sum(nil)
	if want := base64.StdEncoding.EncodeToString(stored[:]); m[3] != want {
		t.Errorf("StoredKey %s, want %s", m[3], want)
	}
	if want := base64.StdEncoding.EncodeToString(server); m[4] != want {
		t.Errorf("ServerKey %s, want %s", m[4], want)
	}

	// A fresh salt every call, so two encodings of one password differ.
	a, _ := Encode(ScramSHA256, password)
	b, _ := Encode(ScramSHA256, password)
	if a == b {
		t.Errorf("two SCRAM encodings share a salt: %s", a)
	}
}

func TestEncodeFixedVectors(t *testing.T) {
	tests := []struct {
		format, password, want string
	}{
		// MySQL's own PASSWORD('password').
		{MySQLNative, "password", "*2470C0C06DEE42FD1618BB99005ADCA2EC9D1E19"},
		// SHA-256 of "abc", FIPS 180-2 example B.1.
		{SHA256, "abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{Plaintext, "as-is", "as-is"},
	}
	for _, tt := range tests {
		got, err := Encode(tt.format, tt.password)
		if err != nil {
			t.Fatalf("%s: %v", tt.format, err)
		}
		if got != tt.want {
			t.Errorf("%s(%q) = %q, want %q", tt.format, tt.password, got, tt.want)
		}
	}
}

func TestEncodeUnknownFormat(t *testing.T) {
	if _, err := Encode("apr1", "x"); err == nil {
		t.Fatal("expected an error for an unknown format")
	}
	for _, name := range []string{ScramSHA256, MySQLNative, SHA256, Plaintext} {
		if !Known(name) {
			t.Errorf("%s not known", name)
		}
	}
	if Known("apr1") {
		t.Error("apr1 reported known")
	}
}
