package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeTermproxy is Proxmox's termproxy: it wants "<user>:<ticket>\n",
// answers "OK" with the first output, then unwraps "0:<len>:<data>" frames
// and echoes their payload upper-cased, and drops the connection on a
// frame it does not understand.
func fakeTermproxy(t *testing.T, user, ticket string, greeting string) http.HandlerFunc {
	t.Helper()
	up := websocket.Upgrader{Subprotocols: []string{"binary"}}
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		msgType, login, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if msgType != websocket.TextMessage || string(login) != user+":"+ticket+"\n" {
			_ = conn.WriteMessage(websocket.TextMessage, []byte("ERR"))
			return
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, append([]byte("OK"), greeting...)); err != nil {
			return
		}
		for {
			_, frame, err := conn.ReadMessage()
			if err != nil {
				return
			}
			switch {
			case string(frame) == "2":
				continue
			case bytes.HasPrefix(frame, []byte("0:")):
				parts := bytes.SplitN(frame, []byte(":"), 3)
				n, err := strconv.Atoi(string(parts[1]))
				if err != nil || len(parts[2]) != n {
					t.Errorf("termproxy: bad frame %q", frame)
					return
				}
				if err := conn.WriteMessage(websocket.BinaryMessage, bytes.ToUpper(parts[2])); err != nil {
					return
				}
			default:
				t.Errorf("termproxy: unexpected frame %q", frame)
				return
			}
		}
	}
}

func TestTermproxyFrame(t *testing.T) {
	got := termproxyFrame([]byte("ls\n"))
	if string(got) != "0:3:ls\n" {
		t.Fatalf("got %q", got)
	}
	// Byte length, not rune count.
	if got := termproxyFrame([]byte("é")); string(got) != "0:2:é" {
		t.Fatalf("got %q", got)
	}
}

func dialTest(t *testing.T, server *httptest.Server, subprotocol string) *websocket.Conn {
	t.Helper()
	d := websocket.Dialer{Subprotocols: []string{subprotocol}}
	conn, resp, err := d.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = resp.Body.Close()
	return conn
}

func TestTermproxyLogin(t *testing.T) {
	srv := httptest.NewServer(fakeTermproxy(t, "u@pve", "TICKET", "login: "))
	defer srv.Close()

	conn := dialTest(t, srv, "binary")
	defer func() { _ = conn.Close() }()
	rest, err := termproxyLogin(conn, "u@pve", "TICKET")
	if err != nil {
		t.Fatal(err)
	}
	if string(rest) != "login: " {
		t.Fatalf("leftover %q", rest)
	}

	bad := dialTest(t, srv, "binary")
	defer func() { _ = bad.Close() }()
	if _, err := termproxyLogin(bad, "u@pve", "WRONG"); err == nil {
		t.Fatal("expected the login to be refused")
	}
}

// TestRelayConsole runs the full path: a browser-side websocket, the relay,
// and the fake termproxy. The browser sends and receives raw bytes.
func TestRelayConsole(t *testing.T) {
	pveSrv := httptest.NewServer(fakeTermproxy(t, "u@pve", "TICKET", "login: "))
	defer pveSrv.Close()

	relayDone := make(chan error, 1)
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pve := dialTest(t, pveSrv, "binary")
		defer func() { _ = pve.Close() }()
		leftover, err := termproxyLogin(pve, "u@pve", "TICKET")
		if err != nil {
			t.Error(err)
			return
		}
		up := websocket.Upgrader{Subprotocols: []string{consoleSubprotocol}}
		browser, err := up.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = browser.Close() }()
		relayDone <- relayConsole(browser, pve, leftover)
	}))
	defer proxySrv.Close()

	browser := dialTest(t, proxySrv, consoleSubprotocol)
	defer func() { _ = browser.Close() }()
	if browser.Subprotocol() != consoleSubprotocol {
		t.Fatalf("subprotocol %q", browser.Subprotocol())
	}

	read := func() string {
		_ = browser.SetReadDeadline(time.Now().Add(5 * time.Second))
		msgType, data, err := browser.ReadMessage()
		if err != nil {
			t.Fatalf("browser read: %v", err)
		}
		if msgType != websocket.BinaryMessage {
			t.Fatalf("browser got message type %d", msgType)
		}
		return string(data)
	}
	if got := read(); got != "login: " {
		t.Fatalf("greeting %q", got)
	}
	if err := browser.WriteMessage(websocket.BinaryMessage, []byte("echo hi\n")); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "ECHO HI\n" {
		t.Fatalf("echo %q", got)
	}

	// The browser leaving ends the relay.
	_ = browser.Close()
	select {
	case err := <-relayDone:
		if err == nil || !strings.HasPrefix(err.Error(), "browser:") {
			t.Fatalf("relay ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not end")
	}
}
