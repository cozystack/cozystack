package integration

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func metricsPortFree(port int) error {
	l, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err == nil {
		return l.Close()
	}
	owner := ""
	if out, e := exec.Command("lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-t").Output(); e == nil && len(bytes.TrimSpace(out)) > 0 {
		owner = " (owner pid " + strings.Join(strings.Fields(string(out)), ", ") + ")"
	}
	return fmt.Errorf("port %d is in use; cozystack-api binds its metrics server there%s: %w", port, owner, err)
}

type apiProcess struct {
	exited chan struct{}
	err    error
	log    string
}

func startAPI(cmd *exec.Cmd, log string) (*apiProcess, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &apiProcess{exited: make(chan struct{}), log: log}
	go func() { p.err = cmd.Wait(); close(p.exited) }()
	return p, nil
}

// waitReady reports a process that exits before ready holds by its own log
// rather than by the timeout it would otherwise run into.
func (p *apiProcess) waitReady(ctx context.Context, ready func() bool) error {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-p.exited:
			return fmt.Errorf("cozystack-api exited before readiness (%v); last lines of %s:\n%s", p.err, p.log, tail(p.log, 20))
		default:
		}
		if ready() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout: selected API ready")
		case <-p.exited:
		case <-tick.C:
		}
	}
}

func (p *apiProcess) stop(cmd *exec.Cmd) {
	_ = cmd.Process.Signal(os.Interrupt)
	select {
	case <-p.exited:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-p.exited
	}
}

func tail(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

func TestABusyMetricsPortNamesItsReason(t *testing.T) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	port := l.Addr().(*net.TCPAddr).Port
	err = metricsPortFree(port)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("port %d is in use; cozystack-api binds its metrics server there", port)) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "owner pid") && !strings.Contains(err.Error(), strconv.Itoa(os.Getpid())) {
		t.Fatalf("owner is not this test process: %v", err)
	}
}

func TestAnAPIThatExitsIsReportedWithItsLog(t *testing.T) {
	log := filepath.Join(t.TempDir(), "cozystack-api.log")
	out, err := os.Create(log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	cmd := exec.Command("sh", "-c", "echo 'panic: failed to create listener: bind: address already in use'; exit 2")
	cmd.Stdout, cmd.Stderr = out, out
	p, err := startAPI(cmd, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	err = p.waitReady(ctx, func() bool { return false })
	if err == nil || !strings.Contains(err.Error(), "exited before readiness") || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("err = %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("reported only after the readiness timeout")
	}
}

func TestAnAPIThatNeverGetsReadyTimesOut(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	p, err := startAPI(cmd, filepath.Join(t.TempDir(), "absent.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.stop(cmd)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err = p.waitReady(ctx, func() bool { return false }); err == nil || err.Error() != "timeout: selected API ready" {
		t.Fatalf("err = %v", err)
	}
}
