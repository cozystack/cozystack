// SPDX-License-Identifier: Apache-2.0

// Package format turns a password into the value an engine stores to check
// it, and generates the passwords. The migration tooling imports it too, so a
// migrated release and a mint store the same form.
package format

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// Built-in format names.
const (
	// ScramSHA256 is PostgreSQL's SCRAM-SHA-256 verifier,
	// SCRAM-SHA-256$<iterations>:<salt>$<StoredKey>:<ServerKey>, which
	// ALTER ROLE ... PASSWORD stores as it is.
	ScramSHA256 = "scram-sha-256"
	// MySQLNative is the mysql_native_password hash, "*" followed by the
	// upper-case hex of SHA1(SHA1(password)), which mariadb-operator pastes
	// into ALTER USER ... IDENTIFIED BY PASSWORD as it is.
	MySQLNative = "mysql-native"
	// SHA256 is the lower-case hex of SHA-256(password), ClickHouse's
	// password_sha256_hex.
	SHA256 = "sha256"
	// Plaintext stores the password itself, for an engine whose operator or
	// server computes its own verifier from it.
	Plaintext = "plaintext"
)

// PasswordLength is the length of every generated password.
const PasswordLength = 32

// passwordAlphabet needs no escaping in SQL, connection URIs or shells, and
// 32 characters of it carry about 190 bits of entropy.
const passwordAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// scramIterations is PostgreSQL's default for SCRAM-SHA-256.
const scramIterations = 4096

// scramSaltLength matches what PostgreSQL itself generates.
const scramSaltLength = 16

// Known reports whether name is a built-in format.
func Known(name string) bool {
	switch name {
	case ScramSHA256, MySQLNative, SHA256, Plaintext:
		return true
	}
	return false
}

// GeneratePassword draws PasswordLength characters uniformly from the
// alphabet with crypto/rand. Rejection sampling keeps the draw unbiased: a
// byte is accepted only below the largest multiple of the alphabet size.
func GeneratePassword() (string, error) {
	const limit = byte(256 - 256%len(passwordAlphabet))
	out := make([]byte, 0, PasswordLength)
	buf := make([]byte, PasswordLength*2)
	for len(out) < PasswordLength {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("read random bytes: %w", err)
		}
		for _, b := range buf {
			if b >= limit {
				continue
			}
			out = append(out, passwordAlphabet[int(b)%len(passwordAlphabet)])
			if len(out) == PasswordLength {
				break
			}
		}
	}
	return string(out), nil
}

// Encode returns the stored form of password in the named built-in format.
func Encode(name, password string) (string, error) {
	switch name {
	case ScramSHA256:
		salt := make([]byte, scramSaltLength)
		if _, err := rand.Read(salt); err != nil {
			return "", fmt.Errorf("read salt: %w", err)
		}
		return encodeScramSHA256(password, salt, scramIterations)
	case MySQLNative:
		return encodeMySQLNative(password), nil
	case SHA256:
		// Unsalted: ClickHouse's password_sha256_hex accepts no other form.
		sum := sha256.Sum256([]byte(password))
		return hex.EncodeToString(sum[:]), nil
	case Plaintext:
		return password, nil
	}
	return "", fmt.Errorf("unknown password format %q", name)
}

// encodeScramSHA256 builds the verifier PostgreSQL stores for a SCRAM
// password (RFC 5802 with SHA-256, RFC 7677): the salted password through
// PBKDF2, the client key hashed into the stored key, and the server key.
// The password is used as is: the generated alphabet is plain ASCII, which
// SASLprep leaves unchanged.
func encodeScramSHA256(password string, salt []byte, iterations int) (string, error) {
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return "", fmt.Errorf("derive salted password: %w", err)
	}
	clientKey := hmacSHA256(salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	serverKey := hmacSHA256(salted, "Server Key")
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s",
		iterations,
		base64.StdEncoding.EncodeToString(salt),
		base64.StdEncoding.EncodeToString(storedKey[:]),
		base64.StdEncoding.EncodeToString(serverKey),
	), nil
}

func hmacSHA256(key []byte, message string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return mac.Sum(nil)
}

// encodeMySQLNative is MariaDB's PASSWORD(): SHA-1 of SHA-1, unsalted, the
// only form mysql_native_password accepts.
func encodeMySQLNative(password string) string {
	first := sha1.Sum([]byte(password))
	second := sha1.Sum(first[:])
	return "*" + strings.ToUpper(hex.EncodeToString(second[:]))
}
