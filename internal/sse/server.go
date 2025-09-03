// Package sse provides a tiny Server-Sent Events server with pluggable routes.
// No external deps; safe for quick demos and small dashboards.
package sse

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Options configures the SSE server.
type Options struct {
	// Addr is the listening address, e.g. ":8080" or "127.0.0.1:0".
	Addr string

	// IndexPath is the path serving a minimal live HTML page ("" to disable).
	// e.g. "/" or "/status"
	IndexPath string

	// StreamPath is the SSE endpoint path, e.g. "/stream".
	StreamPath string

	// Title for the index page (cosmetic).
	Title string

	// AllowCORS, if true, sets Access-Control-Allow-Origin: * for /stream.
	AllowCORS bool

	// ClientBuf is the per-client buffered message queue size.
	// If 0, defaults to 16. When full, new messages are dropped for that client.
	ClientBuf int

	// Heartbeat sends a comment line every interval to keep connections alive.
	// If 0, defaults to 25s.
	Heartbeat time.Duration

	// Logger (optional). If nil, log.Printf is used.
	Logger *log.Logger
}

// Server is a simple SSE broadcaster.
type Server struct {
	opts Options
	mux  *http.ServeMux
	http *http.Server

	clientsMu sync.RWMutex
	clients   map[*client]struct{}

	// latest holds the most recent payload (sent to new clients on connect).
	latestMu sync.RWMutex
	latest   string
}

type client struct {
	ch        chan string
	closeCh   chan struct{}
	flusher   http.Flusher
	w         http.ResponseWriter
	req       *http.Request
	logf      func(string, ...any)
	heartbeat time.Duration
}

func New(opts Options) *Server {
	if opts.ClientBuf <= 0 {
		opts.ClientBuf = 16
	}
	if opts.Heartbeat <= 0 {
		opts.Heartbeat = 25 * time.Second
	}
	if opts.Addr == "" {
		opts.Addr = ":8080"
	}
	if opts.StreamPath == "" {
		opts.StreamPath = "/stream"
	}
	if opts.IndexPath == "" {
		opts.IndexPath = "/"
	}
	s := &Server{
		opts:    opts,
		mux:     http.NewServeMux(),
		clients: make(map[*client]struct{}),
	}
	s.routes()
	s.http = &http.Server{
		Addr:              opts.Addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

func (s *Server) routes() {
	if s.opts.IndexPath != "" {
		s.mux.HandleFunc(s.opts.IndexPath, s.handleIndex)
	}
	s.mux.HandleFunc(s.opts.StreamPath, s.handleStream)
}

func (s *Server) logf(format string, args ...any) {
	if s.opts.Logger != nil {
		s.opts.Logger.Printf(format, args...)
	} else {
		log.Printf(format, args...)
	}
}

// ListenAndServe starts the HTTP server (blocking).
func (s *Server) ListenAndServe() error {
	s.logf("sse: listening on http://%s (index=%s, stream=%s)", s.http.Addr, s.opts.IndexPath, s.opts.StreamPath)
	return s.http.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.clientsMu.Lock()
	for c := range s.clients {
		close(c.closeCh)
	}
	s.clientsMu.Unlock()
	return s.http.Shutdown(ctx)
}

// Publish broadcasts a new payload to all clients and stores it as latest.
func (s *Server) Publish(payload string) {
	// Store latest
	s.latestMu.Lock()
	s.latest = payload
	s.latestMu.Unlock()

	// Broadcast
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()
	for c := range s.clients {
		select {
		case c.ch <- payload:
		default:
			// Drop if client is slow (buffer full)
			if s.opts.Logger != nil {
				s.opts.Logger.Printf("sse: dropping message to slow client %p", c)
			}
		}
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := indexTemplate(s.opts.Title, s.opts.StreamPath)
	_, _ = w.Write([]byte(page))
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	// Required SSE headers
	if s.opts.AllowCORS {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	c := &client{
		ch:        make(chan string, s.opts.ClientBuf),
		closeCh:   make(chan struct{}),
		flusher:   flusher,
		w:         w,
		req:       r,
		logf:      s.logf,
		heartbeat: s.opts.Heartbeat,
	}

	// Register client
	s.clientsMu.Lock()
	s.clients[c] = struct{}{}
	s.clientsMu.Unlock()

	// Initial comment to open the stream for some proxies
	fmt.Fprintf(w, ": connected %s\n\n", time.Now().Format(time.RFC3339))
	flusher.Flush()

	// Send latest if any
	s.latestMu.RLock()
	latest := s.latest
	s.latestMu.RUnlock()
	if latest != "" {
		writeSSE(w, latest)
		flusher.Flush()
	}

	// Start pump
	go c.pump()

	// Block until client disconnects
	<-r.Context().Done()

	// Unregister client
	close(c.closeCh)
	s.clientsMu.Lock()
	delete(s.clients, c)
	s.clientsMu.Unlock()
}

func (c *client) pump() {
	t := time.NewTicker(c.heartbeat)
	defer t.Stop()
	for {
		select {
		case <-c.closeCh:
			return
		case msg := <-c.ch:
			writeSSE(c.w, msg)
			c.flusher.Flush()
		case <-t.C:
			// heartbeat comment (keeps connections alive through proxies)
			fmt.Fprint(c.w, ": hb\n\n")
			c.flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, msg string) {
	// Split on lines; each needs its own "data:" field per the SSE spec
	lines := strings.Split(strings.TrimRight(msg, "\n"), "\n")
	for _, ln := range lines {
		fmt.Fprintf(w, "data: %s\n", ln)
	}
	fmt.Fprint(w, "\n")
}

// Minimal index page with live updates
func indexTemplate(title, streamPath string) string {
	if title == "" {
		title = "SSE Stream"
	}
	if streamPath == "" {
		streamPath = "/stream"
	}
	const tpl = `<!doctype html>
<html>
<head>
<meta charset="utf-8" />
<title>{{.Title}}</title>
<style>
  body { font-family: system-ui, sans-serif; margin: 2rem; }
  pre { background:#111; color:#eee; padding:1rem; border-radius:12px; white-space:pre-wrap;}
  .status { margin-bottom: 1rem; }
</style>
</head>
<body>
<h1>{{.Title}}</h1>
<div class="status">Connecting…</div>
<pre id="out"></pre>
<script>
  const statusEl = document.querySelector('.status');
  const out = document.getElementById('out');
  const es = new EventSource('{{.Stream}}');
  es.onmessage = (e) => {
    // Replace content with the latest full snapshot
    if (e.data === "") return;
    // We accumulate until a blank 'data:' terminator; simpler approach: reset on first line.
    // For this demo, server always sends full content in one event, so just overwrite.
    out.textContent = (out._acc ?? "") + e.data + "\n";
  };
  es.addEventListener('open', () => { statusEl.textContent = "Connected"; out._acc = ""; });
  es.addEventListener('error', () => { statusEl.textContent = "Disconnected (browser will retry)…"; out._acc = ""; });
  // Optional: keep the latest only per message
  es.onmessage = (e) => {
    out.textContent = e.data + "\n";
    statusEl.textContent = "Connected";
  };
</script>
</body>
</html>`
	page, _ := template.New("idx").Parse(tpl)
	var b strings.Builder
	_ = page.Execute(&b, map[string]any{
		"Title":  title,
		"Stream": streamPath,
	})
	return b.String()
}
