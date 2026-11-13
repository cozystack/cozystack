package kubeovnplunger

import (
	"context"
	"io"
	"log"
	"net"
	"testing"
	"time"

	"github.com/cozystack/cozystack/internal/sse"
)

func runStatusServer(ctx context.Context, t *testing.T, addr string) (bool, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	s := sse.New(sse.Options{Addr: addr, Logger: log.New(io.Discard, "", 0)})
	done := make(chan error, 1)
	go func() { done <- serveStatus(s)(ctx) }()
	select {
	case err := <-done:
		return true, err
	case <-time.After(2 * time.Second):
		return false, nil
	}
}

// A status server that cannot listen fails its runnable, and with it the
// manager, rather than leaving the plunger running without it.
func TestStatusServerReportsListenFailure(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = taken.Close() })
	returned, err := runStatusServer(context.Background(), t, taken.Addr().String())
	if !returned || err == nil {
		t.Fatalf("serveStatus on an occupied address = %v (returned %v), want an error", err, returned)
	}
}

// Stopping the manager stops the server, and that is not an error.
func TestStatusServerStopsWithoutError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	returned, err := runStatusServer(ctx, t, "127.0.0.1:0")
	if !returned || err != nil {
		t.Fatalf("serveStatus after cancel = %v (returned %v), want nil", err, returned)
	}
}
