package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// The Proxmox VE API, as far as a console needs it: a VNC or terminal ticket
// for a machine, the websocket that ticket opens, and its power.

const (
	pveDefaultPort    = "8006"
	pveRequestTimeout = 30 * time.Second
	// How long a resolved connection (endpoint and token) is reused before
	// the ProxmoxConnection and its Secret are read again.
	pveConnectionTTL = 5 * time.Minute
)

// pveError is a non-2xx answer from the hypervisor. The body is kept out of
// it on purpose: it is what gets logged and shown, and must carry no ticket.
type pveError struct {
	Op     string
	Status int
}

func (e *pveError) Error() string { return fmt.Sprintf("hypervisor %s: HTTP %d", e.Op, e.Status) }

// pveTaskError is a task the hypervisor accepted and then failed. Exit is
// Proxmox's own text and names its files, so it stays in the log.
type pveTaskError struct {
	Op   string
	Exit string
}

func (e *pveTaskError) Error() string {
	return fmt.Sprintf("hypervisor %s: task ended with %q", e.Op, e.Exit)
}

// pveClient talks to one Proxmox VE API endpoint with one API token.
type pveClient struct {
	base   string // https://host:port
	token  string // PVEAPIToken=<id>=<secret>
	http   *http.Client
	dialer *websocket.Dialer
}

func newPVEClient(endpoint, tokenID, secret string, insecure bool) *pveClient {
	host := endpoint
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, pveDefaultPort)
	}
	tlsConfig := &tls.Config{InsecureSkipVerify: insecure, MinVersion: tls.VersionTLS12} //nolint:gosec // the platform's own ProxmoxConnection opts in, as kubemox does.
	return &pveClient{
		base:  "https://" + host,
		token: "PVEAPIToken=" + tokenID + "=" + secret,
		http: &http.Client{
			Timeout:   pveRequestTimeout,
			Transport: &http.Transport{TLSClientConfig: tlsConfig},
		},
		dialer: &websocket.Dialer{
			TLSClientConfig:  tlsConfig,
			HandshakeTimeout: pveRequestTimeout,
			Subprotocols:     []string{"binary"},
		},
	}
}

// flexString decodes a JSON string or number; Proxmox returns ports both ways.
type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		*s = flexString(str)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*s = flexString(n.String())
	return nil
}

// consoleTicket is what vncproxy and termproxy hand out.
type consoleTicket struct {
	Port   flexString `json:"port"`
	Ticket string     `json:"ticket"`
	User   string     `json:"user"`
	// With generate-password the VNC password is this instead of the ticket.
	Password string `json:"password"`
}

func (c *pveClient) post(ctx context.Context, op, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api2/json"+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req, op, out)
}

func (c *pveClient) get(ctx context.Context, op, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api2/json"+path, nil)
	if err != nil {
		return err
	}
	return c.do(req, op, out)
}

func (c *pveClient) do(req *http.Request, op string, out any) error {
	req.Header.Set("Authorization", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("hypervisor %s: %w", op, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return &pveError{Op: op, Status: resp.StatusCode}
	}
	if out == nil {
		return nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("hypervisor %s: decoding answer: %w", op, err)
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("hypervisor %s: decoding answer: %w", op, err)
	}
	return nil
}

func qemuPath(node string, vmid int64) string {
	return "/nodes/" + url.PathEscape(node) + "/qemu/" + strconv.FormatInt(vmid, 10)
}

// vncProxy asks for a VNC session on the machine's display, websocket-wrapped.
func (c *pveClient) vncProxy(ctx context.Context, node string, vmid int64) (*consoleTicket, error) {
	form := url.Values{"websocket": {"1"}, "generate-password": {"0"}}
	var t consoleTicket
	if err := c.post(ctx, "vncproxy", qemuPath(node, vmid)+"/vncproxy", form, &t); err != nil {
		return nil, err
	}
	if t.Port == "" || t.Ticket == "" {
		return nil, fmt.Errorf("hypervisor vncproxy: answer without port or ticket")
	}
	return &t, nil
}

// vncPassword is what the VNC server expects in the authentication.
func (t *consoleTicket) vncPassword() string {
	if t.Password != "" {
		return t.Password
	}
	return t.Ticket
}

// termProxy asks for a session on the machine's serial terminal.
func (c *pveClient) termProxy(ctx context.Context, node string, vmid int64) (*consoleTicket, error) {
	var t consoleTicket
	if err := c.post(ctx, "termproxy", qemuPath(node, vmid)+"/termproxy", nil, &t); err != nil {
		return nil, err
	}
	if t.Port == "" || t.Ticket == "" || t.User == "" {
		return nil, fmt.Errorf("hypervisor termproxy: answer without port, ticket or user")
	}
	return &t, nil
}

// reboot asks the hypervisor to reboot the machine, as the PVE UI does.
func (c *pveClient) reboot(ctx context.Context, node string, vmid int64) error {
	return c.post(ctx, "reboot", qemuPath(node, vmid)+"/status/reboot", nil, nil)
}

const (
	// shutdownTimeout is how many seconds a guest gets to power off before
	// the hypervisor stops it.
	shutdownTimeout = "180"
	// taskPoll is how often a task the proxy waits for is looked at.
	taskPoll = 500 * time.Millisecond
)

// running asks the hypervisor whether the machine runs. kubemox's status is up
// to a poll old, and older once it stops watching a machine.
func (c *pveClient) running(ctx context.Context, node string, vmid int64) (bool, error) {
	var status struct {
		Status string `json:"status"`
	}
	if err := c.get(ctx, "status", qemuPath(node, vmid)+"/status/current", &status); err != nil {
		return false, err
	}
	return status.Status == "running", nil
}

// shutdown asks the guest to power off and has the hypervisor stop it if it
// does not. It returns the task, which is only queued at this point.
func (c *pveClient) shutdown(ctx context.Context, node string, vmid int64) (string, error) {
	form := url.Values{"timeout": {shutdownTimeout}, "forceStop": {"1"}}
	var upid string
	err := c.post(ctx, "shutdown", qemuPath(node, vmid)+"/status/shutdown", form, &upid)
	return upid, err
}

// waitTask follows a task until it ends and reports one that failed. It
// returns nil when ctx ends first: the task is then still going.
func (c *pveClient) waitTask(ctx context.Context, op, node, upid string) error {
	path := "/nodes/" + url.PathEscape(node) + "/tasks/" + url.PathEscape(upid) + "/status"
	for {
		var task struct {
			Status     string `json:"status"`
			ExitStatus string `json:"exitstatus"`
		}
		if err := c.get(ctx, op, path, &task); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if task.Status == "stopped" {
			if task.ExitStatus == "OK" || strings.HasPrefix(task.ExitStatus, "WARNINGS") {
				return nil
			}
			return &pveTaskError{Op: op, Exit: task.ExitStatus}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(taskPoll):
		}
	}
}

// dialConsole opens the websocket a vncproxy or termproxy ticket is for.
func (c *pveClient) dialConsole(ctx context.Context, node string, vmid int64, t *consoleTicket) (*websocket.Conn, error) {
	q := url.Values{"port": {string(t.Port)}, "vncticket": {t.Ticket}}
	wsURL := "wss://" + strings.TrimPrefix(c.base, "https://") + "/api2/json" + qemuPath(node, vmid) + "/vncwebsocket?" + q.Encode()
	header := http.Header{"Authorization": {c.token}}
	conn, resp, err := c.dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
			return nil, &pveError{Op: "vncwebsocket", Status: resp.StatusCode}
		}
		return nil, fmt.Errorf("hypervisor vncwebsocket: %w", err)
	}
	return conn, nil
}

// connectionResolver turns the name a VirtualMachine references into a
// client, reading the ProxmoxConnection and the Secret it points at. The
// token stays in this process: nothing of it reaches a tenant or a log.
type connectionResolver struct {
	dyn  dynamic.Interface
	core kubernetes.Interface

	mu    sync.Mutex
	cache map[string]cachedConnection
}

type cachedConnection struct {
	client  *pveClient
	expires time.Time
}

func newConnectionResolver(dyn dynamic.Interface, core kubernetes.Interface) *connectionResolver {
	return &connectionResolver{dyn: dyn, core: core, cache: map[string]cachedConnection{}}
}

func (r *connectionResolver) get(ctx context.Context, name string) (*pveClient, error) {
	r.mu.Lock()
	entry, ok := r.cache[name]
	r.mu.Unlock()
	if ok && time.Now().Before(entry.expires) {
		return entry.client, nil
	}

	spec, err := lookupConnection(ctx, r.dyn, name)
	if err != nil {
		return nil, fmt.Errorf("resolving ProxmoxConnection %q: %w", name, err)
	}
	secret, err := r.core.CoreV1().Secrets(spec.SecretNamespace).Get(ctx, spec.SecretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("reading token of ProxmoxConnection %q: %w", name, err)
	}
	token, ok := secret.Data[spec.SecretKey]
	if !ok || len(token) == 0 {
		return nil, fmt.Errorf("token secret %s/%s has no key %q", spec.SecretNamespace, spec.SecretName, spec.SecretKey)
	}
	client := newPVEClient(spec.Endpoint, spec.TokenID, strings.TrimSpace(string(token)), spec.InsecureSkipVerify)

	r.mu.Lock()
	r.cache[name] = cachedConnection{client: client, expires: time.Now().Add(pveConnectionTTL)}
	r.mu.Unlock()
	return client, nil
}

// forget drops a cached client, so that the next request rereads the
// connection after the hypervisor rejected the token.
func (r *connectionResolver) forget(name string) {
	r.mu.Lock()
	delete(r.cache, name)
	r.mu.Unlock()
}
