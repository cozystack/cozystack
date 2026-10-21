package main

import (
	"crypto/des" //nolint:gosec // RFB 6.2.2 defines VNC authentication over DES; the key is a one-shot ticket.
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
)

// The Remote Framebuffer protocol, RFC 6143. The hypervisor's VNC server
// insists on VNC authentication with the ticket vncproxy issued; the
// browser's noVNC expects no authentication, as it gets from KubeVirt's
// virt-api. So the proxy completes the handshake on both sides itself and
// only then relays bytes: the ticket never leaves the platform.

const (
	rfbSecurityNone    = 1
	rfbSecurityVNCAuth = 2
)

// rfbVersion is one side's protocol version, "RFB 003.008\n" on the wire.
type rfbVersion struct{ major, minor int }

func readRFBVersion(r io.Reader) (rfbVersion, error) {
	var buf [12]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return rfbVersion{}, fmt.Errorf("reading protocol version: %w", err)
	}
	var v rfbVersion
	if _, err := fmt.Sscanf(string(buf[:]), "RFB %03d.%03d\n", &v.major, &v.minor); err != nil {
		return rfbVersion{}, fmt.Errorf("not an RFB server: %q", buf[:])
	}
	if v.major != 3 {
		return rfbVersion{}, fmt.Errorf("unsupported RFB version %d.%d", v.major, v.minor)
	}
	return v, nil
}

func (v rfbVersion) String() string { return fmt.Sprintf("RFB %03d.%03d\n", v.major, v.minor) }

// readRFBReason reads the failure reason that follows a zero security-type
// count or a failed SecurityResult: a u32 length and that many bytes.
func readRFBReason(r io.Reader) string {
	var n uint32
	if err := binary.Read(r, binary.BigEndian, &n); err != nil || n > 4096 {
		return ""
	}
	reason := make([]byte, n)
	if _, err := io.ReadFull(r, reason); err != nil {
		return ""
	}
	return string(reason)
}

// vncAuthResponse answers a VNC authentication challenge: the first eight
// bytes of the password, each bit-reversed, key DES in ECB mode over the two
// halves of the challenge.
func vncAuthResponse(challenge [16]byte, password string) [16]byte {
	var key [8]byte
	copy(key[:], password)
	for i := range key {
		key[i] = bits.Reverse8(key[i])
	}
	block, err := des.NewCipher(key[:]) //nolint:gosec // see the package comment: this is the protocol.
	if err != nil {
		panic(err) // an 8-byte key never fails
	}
	var resp [16]byte
	block.Encrypt(resp[:8], challenge[:8])
	block.Encrypt(resp[8:], challenge[8:])
	return resp
}

// authenticateRFB plays the client in the handshake with the hypervisor's
// VNC server and authenticates with password. It returns once the server
// has accepted the security exchange, so the next bytes the caller writes
// are the ClientInit message.
func authenticateRFB(rw io.ReadWriter, password string) error {
	server, err := readRFBVersion(rw)
	if err != nil {
		return err
	}
	ours := rfbVersion{3, 8}
	if server.minor < 8 {
		ours = server
	}
	if _, err := io.WriteString(rw, ours.String()); err != nil {
		return fmt.Errorf("sending protocol version: %w", err)
	}

	var chosen byte
	if ours.minor >= 7 {
		var count [1]byte
		if _, err := io.ReadFull(rw, count[:]); err != nil {
			return fmt.Errorf("reading security types: %w", err)
		}
		if count[0] == 0 {
			return fmt.Errorf("VNC server refused the connection: %s", readRFBReason(rw))
		}
		types := make([]byte, count[0])
		if _, err := io.ReadFull(rw, types); err != nil {
			return fmt.Errorf("reading security types: %w", err)
		}
		for _, t := range types {
			switch {
			case t == rfbSecurityVNCAuth:
				chosen = t
			case t == rfbSecurityNone && chosen == 0:
				chosen = t
			}
		}
		if chosen == 0 {
			return fmt.Errorf("VNC server offers no security type the proxy speaks: %v", types)
		}
		if _, err := rw.Write([]byte{chosen}); err != nil {
			return fmt.Errorf("choosing security type: %w", err)
		}
	} else {
		var t uint32
		if err := binary.Read(rw, binary.BigEndian, &t); err != nil {
			return fmt.Errorf("reading security type: %w", err)
		}
		switch t {
		case 0:
			return fmt.Errorf("VNC server refused the connection: %s", readRFBReason(rw))
		case rfbSecurityNone, rfbSecurityVNCAuth:
			chosen = byte(t)
		default:
			return fmt.Errorf("VNC server offers no security type the proxy speaks: %d", t)
		}
	}

	if chosen == rfbSecurityVNCAuth {
		var challenge [16]byte
		if _, err := io.ReadFull(rw, challenge[:]); err != nil {
			return fmt.Errorf("reading VNC challenge: %w", err)
		}
		resp := vncAuthResponse(challenge, password)
		if _, err := rw.Write(resp[:]); err != nil {
			return fmt.Errorf("answering VNC challenge: %w", err)
		}
	}
	// 3.3 and 3.7 send no SecurityResult for None.
	if chosen == rfbSecurityVNCAuth || ours.minor >= 8 {
		var result uint32
		if err := binary.Read(rw, binary.BigEndian, &result); err != nil {
			return fmt.Errorf("reading security result: %w", err)
		}
		if result != 0 {
			reason := ""
			if ours.minor >= 8 {
				reason = readRFBReason(rw)
			}
			return fmt.Errorf("VNC authentication failed: %s", reason)
		}
	}
	return nil
}

// errRFBClientRefused is returned when the browser does not accept the only
// security type the proxy offers it.
var errRFBClientRefused = errors.New("client did not choose security type None")

// serveRFBNone plays the server in the handshake with the browser and offers
// it security type None only. It returns once the browser is at the point of
// sending ClientInit, so its next bytes belong to the hypervisor.
func serveRFBNone(rw io.ReadWriter) error {
	if _, err := io.WriteString(rw, rfbVersion{3, 8}.String()); err != nil {
		return fmt.Errorf("sending protocol version: %w", err)
	}
	client, err := readRFBVersion(rw)
	if err != nil {
		return err
	}
	if client.minor < 7 {
		return binary.Write(rw, binary.BigEndian, uint32(rfbSecurityNone))
	}
	if _, err := rw.Write([]byte{1, rfbSecurityNone}); err != nil {
		return fmt.Errorf("sending security types: %w", err)
	}
	var chosen [1]byte
	if _, err := io.ReadFull(rw, chosen[:]); err != nil {
		return fmt.Errorf("reading chosen security type: %w", err)
	}
	if chosen[0] != rfbSecurityNone {
		if client.minor >= 8 {
			reason := errRFBClientRefused.Error()
			_ = binary.Write(rw, binary.BigEndian, uint32(1))
			_ = binary.Write(rw, binary.BigEndian, uint32(len(reason)))
			_, _ = io.WriteString(rw, reason)
		}
		return errRFBClientRefused
	}
	if client.minor >= 8 {
		return binary.Write(rw, binary.BigEndian, uint32(0))
	}
	return nil
}
