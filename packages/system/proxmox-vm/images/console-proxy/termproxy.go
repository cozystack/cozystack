package main

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Proxmox's termproxy speaks its own framing on the websocket. The client
// opens with "<user>:<ticket>\n" and the server answers "OK"; after that the
// server's frames carry the terminal's raw output, while the client wraps
// each write as "0:<byte length>:<bytes>", a resize as "1:<cols>:<rows>:"
// and a keepalive as "2". xterm in the dashboard expects the stream the
// KubeVirt serial console gives it, bytes both ways, so the proxy adds the
// framing on the way in and strips the greeting on the way out.

const (
	termproxyPingInterval = 30 * time.Second
	termproxyLoginTimeout = 15 * time.Second
)

var errTermproxyRefused = errors.New("termproxy did not accept the ticket")

// termproxyLogin sends the greeting and waits for the "OK". Whatever the
// server sent in the same message after the OK is returned to be relayed.
func termproxyLogin(conn *websocket.Conn, user, ticket string) ([]byte, error) {
	if err := conn.WriteMessage(websocket.TextMessage, []byte(user+":"+ticket+"\n")); err != nil {
		return nil, fmt.Errorf("sending termproxy login: %w", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(termproxyLoginTimeout)); err != nil {
		return nil, err
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("reading termproxy answer: %w", err)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(data, []byte("OK")) {
		return nil, errTermproxyRefused
	}
	return data[2:], nil
}

// termproxyFrame wraps terminal input for the termproxy server.
func termproxyFrame(data []byte) []byte {
	out := make([]byte, 0, len(data)+16)
	out = append(out, '0', ':')
	out = strconv.AppendInt(out, int64(len(data)), 10)
	out = append(out, ':')
	return append(out, data...)
}

// relayConsole moves bytes between the browser and termproxy until either
// side goes away, and keeps the termproxy session alive with its ping.
func relayConsole(browser, pve *websocket.Conn, leftover []byte) error {
	var pveWrite sync.Mutex
	writePVE := func(msgType int, data []byte) error {
		pveWrite.Lock()
		defer pveWrite.Unlock()
		return pve.WriteMessage(msgType, data)
	}

	errc := make(chan error, 2)
	done := make(chan struct{})

	// termproxy -> browser: raw bytes, as binary frames.
	go func() {
		if len(leftover) > 0 {
			if err := browser.WriteMessage(websocket.BinaryMessage, leftover); err != nil {
				errc <- fmt.Errorf("browser: %w", err)
				return
			}
		}
		for {
			_, data, err := pve.ReadMessage()
			if err != nil {
				errc <- fmt.Errorf("hypervisor: %w", err)
				return
			}
			if err := browser.WriteMessage(websocket.BinaryMessage, data); err != nil {
				errc <- fmt.Errorf("browser: %w", err)
				return
			}
		}
	}()

	// browser -> termproxy: framed writes.
	go func() {
		for {
			_, data, err := browser.ReadMessage()
			if err != nil {
				errc <- fmt.Errorf("browser: %w", err)
				return
			}
			if len(data) == 0 {
				continue
			}
			if err := writePVE(websocket.TextMessage, termproxyFrame(data)); err != nil {
				errc <- fmt.Errorf("hypervisor: %w", err)
				return
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(termproxyPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := writePVE(websocket.TextMessage, []byte("2")); err != nil {
					return
				}
			}
		}
	}()

	err := <-errc
	close(done)
	return err
}

// relayVNC moves bytes between the browser and the VNC server once both
// handshakes are done. leftover is what the hypervisor already sent beyond
// its handshake.
func relayVNC(browser, pve *websocket.Conn, leftover []byte) error {
	errc := make(chan error, 2)
	go func() {
		if len(leftover) > 0 {
			if err := browser.WriteMessage(websocket.BinaryMessage, leftover); err != nil {
				errc <- fmt.Errorf("browser: %w", err)
				return
			}
		}
		for {
			_, data, err := pve.ReadMessage()
			if err != nil {
				errc <- fmt.Errorf("hypervisor: %w", err)
				return
			}
			if err := browser.WriteMessage(websocket.BinaryMessage, data); err != nil {
				errc <- fmt.Errorf("browser: %w", err)
				return
			}
		}
	}()
	go func() {
		for {
			_, data, err := browser.ReadMessage()
			if err != nil {
				errc <- fmt.Errorf("browser: %w", err)
				return
			}
			if err := pve.WriteMessage(websocket.BinaryMessage, data); err != nil {
				errc <- fmt.Errorf("hypervisor: %w", err)
				return
			}
		}
	}()
	return <-errc
}
