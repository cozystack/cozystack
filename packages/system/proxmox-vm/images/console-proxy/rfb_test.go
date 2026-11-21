package main

import (
	"bytes"
	"crypto/des" //nolint:gosec // the reference for the test vector
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"testing"
)

// fakeVNCServer plays a VNC server of the given version over rw: VNC
// authentication with password, then it echoes whatever it reads.
func fakeVNCServer(t *testing.T, rw io.ReadWriter, version rfbVersion, password string, reject bool) {
	t.Helper()
	challenge := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	if _, err := io.WriteString(rw, version.String()); err != nil {
		t.Errorf("server: %v", err)
		return
	}
	client, err := readRFBVersion(rw)
	if err != nil {
		t.Errorf("server: %v", err)
		return
	}
	if client.minor > version.minor {
		t.Errorf("client negotiated %v above the server's %v", client, version)
	}
	if version.minor >= 7 {
		if _, err := rw.Write([]byte{2, rfbSecurityVNCAuth, 19}); err != nil {
			return
		}
		var chosen [1]byte
		if _, err := io.ReadFull(rw, chosen[:]); err != nil || chosen[0] != rfbSecurityVNCAuth {
			t.Errorf("server: client chose %v, %v", chosen, err)
			return
		}
	} else if err := binary.Write(rw, binary.BigEndian, uint32(rfbSecurityVNCAuth)); err != nil {
		return
	}
	if _, err := rw.Write(challenge[:]); err != nil {
		return
	}
	var resp [16]byte
	if _, err := io.ReadFull(rw, resp[:]); err != nil {
		t.Errorf("server: %v", err)
		return
	}
	want := vncAuthResponse(challenge, password)
	if resp != want || reject {
		_ = binary.Write(rw, binary.BigEndian, uint32(1))
		if version.minor >= 8 {
			_ = binary.Write(rw, binary.BigEndian, uint32(len("bad ticket")))
			_, _ = io.WriteString(rw, "bad ticket")
		}
		return
	}
	if err := binary.Write(rw, binary.BigEndian, uint32(0)); err != nil {
		return
	}
	_, _ = io.Copy(rw, rw)
}

func TestVNCAuthResponseMatchesReferenceDES(t *testing.T) {
	// "secret12", each byte bit-reversed, as the RFB VNC authentication keys DES.
	key, _ := hex.DecodeString("cea6c64ea62e8c4c")
	block, err := des.NewCipher(key) //nolint:gosec // reference
	if err != nil {
		t.Fatal(err)
	}
	challenge := [16]byte{0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4, 0xca, 0xfe, 0xba, 0xbe, 5, 6, 7, 8}
	var want [16]byte
	block.Encrypt(want[:8], challenge[:8])
	block.Encrypt(want[8:], challenge[8:])

	for _, password := range []string{"secret12", "secret12 and the rest of a long ticket"} {
		if got := vncAuthResponse(challenge, password); got != want {
			t.Errorf("password %q: got %x, want %x", password, got, want)
		}
	}
}

func TestAuthenticateRFB(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version rfbVersion
		reject  bool
		wantErr bool
	}{
		{"3.8", rfbVersion{3, 8}, false, false},
		{"3.7", rfbVersion{3, 7}, false, false},
		{"3.3", rfbVersion{3, 3}, false, false},
		{"rejected", rfbVersion{3, 8}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = client.Close() }()
			go func() {
				defer func() { _ = server.Close() }()
				fakeVNCServer(t, server, tc.version, "PVE:ticket", tc.reject)
			}()
			err := authenticateRFB(client, "PVE:ticket")
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected the handshake to fail")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			// Past the handshake the server echoes: the stream is clean.
			if _, err := client.Write([]byte("ClientInit")); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 10)
			if _, err := io.ReadFull(client, buf); err != nil || string(buf) != "ClientInit" {
				t.Fatalf("echo: %q, %v", buf, err)
			}
		})
	}
}

func TestAuthenticateRFBWrongPassword(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	go func() {
		defer func() { _ = server.Close() }()
		fakeVNCServer(t, server, rfbVersion{3, 8}, "right", false)
	}()
	err := authenticateRFB(client, "wrong")
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("bad ticket")) {
		t.Fatalf("got %v, want the server's reason", err)
	}
}

// fakeNoVNC plays the browser: it expects security type None only.
func fakeNoVNC(t *testing.T, rw io.ReadWriter, version rfbVersion, choose byte) error {
	t.Helper()
	server, err := readRFBVersion(rw)
	if err != nil {
		return err
	}
	if server != (rfbVersion{3, 8}) {
		t.Errorf("proxy offered %v", server)
	}
	if _, err := io.WriteString(rw, version.String()); err != nil {
		return err
	}
	if version.minor < 7 {
		var sec uint32
		if err := binary.Read(rw, binary.BigEndian, &sec); err != nil {
			return err
		}
		if sec != rfbSecurityNone {
			return errors.New("not None")
		}
		return nil
	}
	var count [1]byte
	if _, err := io.ReadFull(rw, count[:]); err != nil {
		return err
	}
	types := make([]byte, count[0])
	if _, err := io.ReadFull(rw, types); err != nil {
		return err
	}
	if !bytes.Equal(types, []byte{rfbSecurityNone}) {
		return errors.New("proxy offered more than None")
	}
	if _, err := rw.Write([]byte{choose}); err != nil {
		return err
	}
	if version.minor >= 8 {
		var result uint32
		if err := binary.Read(rw, binary.BigEndian, &result); err != nil {
			return err
		}
		if result != 0 {
			return errors.New("security result " + readRFBReason(rw))
		}
	}
	return nil
}

func TestServeRFBNone(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version rfbVersion
		choose  byte
		wantErr bool
	}{
		{"3.8", rfbVersion{3, 8}, rfbSecurityNone, false},
		{"3.7", rfbVersion{3, 7}, rfbSecurityNone, false},
		{"3.3", rfbVersion{3, 3}, 0, false},
		{"refuses None", rfbVersion{3, 8}, rfbSecurityVNCAuth, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy, browser := net.Pipe()
			defer func() { _ = browser.Close() }()
			errc := make(chan error, 1)
			go func() {
				defer func() { _ = proxy.Close() }()
				errc <- serveRFBNone(proxy)
			}()
			clientErr := fakeNoVNC(t, browser, tc.version, tc.choose)
			serverErr := <-errc
			if tc.wantErr {
				if serverErr == nil || clientErr == nil {
					t.Fatalf("expected both sides to fail: proxy %v, browser %v", serverErr, clientErr)
				}
				return
			}
			if serverErr != nil || clientErr != nil {
				t.Fatalf("proxy %v, browser %v", serverErr, clientErr)
			}
		})
	}
}
