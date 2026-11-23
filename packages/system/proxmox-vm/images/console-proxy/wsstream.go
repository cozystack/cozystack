package main

import (
	"github.com/gorilla/websocket"
)

// wsStream presents the messages of a websocket as one byte stream, which
// is what the RFB handshake reads and writes. Reads drain one message at a
// time; whatever a message carried beyond what the handshake consumed stays
// in the buffer and is handed over to the browser before the relay starts.
type wsStream struct {
	conn *websocket.Conn
	buf  []byte
}

func (s *wsStream) Read(p []byte) (int, error) {
	for len(s.buf) == 0 {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			return 0, err
		}
		s.buf = data
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

func (s *wsStream) Write(p []byte) (int, error) {
	if err := s.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// pending returns the bytes read from the socket that nobody consumed yet.
func (s *wsStream) pending() []byte {
	out := s.buf
	s.buf = nil
	return out
}
