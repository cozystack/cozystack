package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const (
	apiGroup   = "subresources.proxmox.cozystack.io"
	apiVersion = "v1alpha1"
	apiPrefix  = "/apis/" + apiGroup + "/" + apiVersion

	// The group the SubjectAccessReview asks about: a caller who may read the
	// kubemox VirtualMachine of a tenant may look at its console and, with
	// update, change its power. This is a second check behind the apiserver's
	// own RBAC on this group, against a mistake in that RBAC.
	vmGroup    = "proxmox.alperen.cloud"
	vmResource = "virtualmachines"

	// The subprotocol KubeVirt's serial console negotiates; the dashboard
	// asks for it and reads binary frames only.
	consoleSubprotocol = "plain.kubevirt.io"

	handshakeTimeout = 30 * time.Second
)

// server is the aggregated API.
type server struct {
	auth        *frontProxyAuth
	core        kubernetes.Interface
	dyn         dynamic.Interface
	connections *connectionResolver
	// dial is how a console reaches the hypervisor; tests replace it.
	upgrader websocket.Upgrader
}

func newServer(auth *frontProxyAuth, core kubernetes.Interface, dyn dynamic.Interface) *server {
	return &server{
		auth:        auth,
		core:        core,
		dyn:         dyn,
		connections: newConnectionResolver(dyn, core),
		upgrader: websocket.Upgrader{
			HandshakeTimeout: handshakeTimeout,
			Subprotocols:     []string{"binary", consoleSubprotocol},
			// The apiserver, not a browser, is the peer; it has done its own
			// origin check.
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	for _, p := range []string{"/healthz", "/readyz", "/livez"} {
		mux.HandleFunc("GET "+p, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("ok"))
		})
	}
	mux.Handle("GET /apis/"+apiGroup, s.authenticated(s.discoverGroup))
	mux.Handle("GET "+apiPrefix, s.authenticated(s.discoverResources))
	mux.Handle("GET /openapi/v2", s.authenticated(s.openAPIv2))
	mux.Handle("GET /openapi/v3", s.authenticated(s.openAPIv3))
	mux.Handle("GET /openapi/v3/", s.authenticated(s.openAPIv3))
	mux.Handle(apiPrefix+"/namespaces/{namespace}/virtualmachines/{name}/{subresource}", s.authenticated(s.subresource))
	return mux
}

// authenticated admits requests that came through the front proxy only.
func (s *server) authenticated(next func(http.ResponseWriter, *http.Request, *userInfo)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := s.auth.verify(r)
		if err != nil {
			slog.Info("request refused", "path", r.URL.Path, "error", err)
			writeStatus(w, http.StatusUnauthorized, metav1.StatusReasonUnauthorized, "not from the front proxy")
			return
		}
		next(w, r, user)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeStatus(w http.ResponseWriter, code int, reason metav1.StatusReason, message string) {
	writeJSON(w, code, metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusFailure,
		Code:     int32(code), //nolint:gosec // an HTTP status code
		Reason:   reason,
		Message:  message,
	})
}

func (s *server) discoverGroup(w http.ResponseWriter, _ *http.Request, _ *userInfo) {
	gv := metav1.GroupVersionForDiscovery{GroupVersion: apiGroup + "/" + apiVersion, Version: apiVersion}
	writeJSON(w, http.StatusOK, metav1.APIGroup{
		TypeMeta:         metav1.TypeMeta{Kind: "APIGroup", APIVersion: "v1"},
		Name:             apiGroup,
		Versions:         []metav1.GroupVersionForDiscovery{gv},
		PreferredVersion: gv,
	})
}

func (s *server) discoverResources(w http.ResponseWriter, _ *http.Request, _ *userInfo) {
	resource := func(sub string, verbs ...string) metav1.APIResource {
		return metav1.APIResource{
			Name: vmResource + "/" + sub, SingularName: "", Namespaced: true,
			Group: apiGroup, Version: apiVersion, Kind: "VirtualMachine", Verbs: verbs,
		}
	}
	writeJSON(w, http.StatusOK, metav1.APIResourceList{
		TypeMeta:     metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"},
		GroupVersion: apiGroup + "/" + apiVersion,
		APIResources: []metav1.APIResource{
			resource("vnc", "get"),
			resource("console", "get"),
			resource("start", "update"),
			resource("stop", "update"),
			resource("restart", "update"),
		},
	})
}

// The aggregator asks for an OpenAPI document and logs its absence on every
// sync; an empty one is cheaper than those logs.
func (s *server) openAPIv2(w http.ResponseWriter, _ *http.Request, _ *userInfo) {
	writeJSON(w, http.StatusOK, map[string]any{
		"swagger": "2.0",
		"info":    map[string]string{"title": apiGroup, "version": apiVersion},
		"paths":   map[string]any{},
	})
}

func (s *server) openAPIv3(w http.ResponseWriter, _ *http.Request, _ *userInfo) {
	writeJSON(w, http.StatusOK, map[string]any{
		"openapi": "3.0.0",
		"info":    map[string]string{"title": apiGroup, "version": apiVersion},
		"paths":   map[string]any{},
	})
}

// subresource dispatches vnc, console, start, stop and restart.
func (s *server) subresource(w http.ResponseWriter, r *http.Request, user *userInfo) {
	namespace, name, sub := r.PathValue("namespace"), r.PathValue("name"), r.PathValue("subresource")
	log := slog.With("namespace", namespace, "vm", name, "subresource", sub, "user", user.Name)

	var verb string
	switch sub {
	case "vnc", "console":
		if r.Method != http.MethodGet {
			writeStatus(w, http.StatusMethodNotAllowed, metav1.StatusReasonMethodNotAllowed, "use GET")
			return
		}
		verb = "get"
	case "start", "stop", "restart":
		if r.Method != http.MethodPut && r.Method != http.MethodPost {
			writeStatus(w, http.StatusMethodNotAllowed, metav1.StatusReasonMethodNotAllowed, "use PUT")
			return
		}
		verb = "update"
	default:
		writeStatus(w, http.StatusNotFound, metav1.StatusReasonNotFound, fmt.Sprintf("no subresource %q", sub))
		return
	}

	if user.Name == "" {
		writeStatus(w, http.StatusUnauthorized, metav1.StatusReasonUnauthorized, "the front proxy named no user")
		return
	}
	allowed, err := s.authorize(r.Context(), user, verb, namespace, name)
	if err != nil {
		log.Error("authorization check failed", "error", err)
		writeStatus(w, http.StatusInternalServerError, metav1.StatusReasonInternalError, "authorization check failed")
		return
	}
	if !allowed {
		log.Info("forbidden")
		writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden,
			fmt.Sprintf("%s %s.%s %q is forbidden for %s", verb, vmResource, vmGroup, name, user.Name))
		return
	}

	vm, err := lookupVM(r.Context(), s.dyn, namespace, name)
	if err != nil {
		if isNotFound(err) {
			writeStatus(w, http.StatusNotFound, metav1.StatusReasonNotFound, fmt.Sprintf("virtualmachines.%s %q not found", vmGroup, name))
			return
		}
		log.Error("reading VirtualMachine failed", "error", err)
		writeStatus(w, http.StatusInternalServerError, metav1.StatusReasonInternalError, "reading the VirtualMachine failed")
		return
	}

	switch sub {
	case "start":
		s.start(w, log, vm)
	case "stop":
		s.stop(w, r, log, vm)
	case "restart":
		s.restart(w, r, log, vm)
	case "vnc":
		s.vnc(w, r, log, vm)
	case "console":
		s.console(w, r, log, vm)
	}
}

func (s *server) authorize(ctx context.Context, user *userInfo, verb, namespace, name string) (bool, error) {
	extra := make(map[string]authorizationv1.ExtraValue, len(user.Extra))
	for k, v := range user.Extra {
		extra[k] = v
	}
	sar := &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   user.Name,
			Groups: user.Groups,
			Extra:  extra,
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Group: vmGroup, Resource: vmResource, Verb: verb, Namespace: namespace, Name: name,
			},
		},
	}
	result, err := s.core.AuthorizationV1().SubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return result.Status.Allowed && !result.Status.Denied, nil
}

// hypervisorStatus maps a hypervisor failure to what the caller sees. A
// refused token is the platform's problem, not the tenant's, and the
// answer says no more than that.
func hypervisorStatus(err error) (int, string) {
	var pe *pveError
	if errors.As(err, &pe) {
		switch pe.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return http.StatusBadGateway, "the hypervisor refused the platform's credentials"
		default:
			return http.StatusBadGateway, fmt.Sprintf("the hypervisor answered HTTP %d to %s", pe.Status, pe.Op)
		}
	}
	return http.StatusBadGateway, "the hypervisor is not reachable"
}

func (s *server) client(ctx context.Context, vm *vmInfo) (*pveClient, error) {
	if vm.Connection == "" {
		return nil, errors.New("the VirtualMachine names no ProxmoxConnection")
	}
	return s.connections.get(ctx, vm.Connection)
}

func (s *server) setAutoStart(w http.ResponseWriter, log *slog.Logger, vm *vmInfo, on bool) bool {
	if err := setAutoStart(context.Background(), s.dyn, vm.Namespace, vm.Name, on); err != nil {
		log.Error("setting enableAutoStart failed", "error", err)
		writeStatus(w, http.StatusInternalServerError, metav1.StatusReasonInternalError, "updating the VirtualMachine failed")
		return false
	}
	return true
}

func accepted(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusAccepted, metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusSuccess,
		Code:     http.StatusAccepted,
		Message:  message,
	})
}

// start leaves the starting to kubemox, which starts a machine it finds
// stopped while enableAutoStart is on.
func (s *server) start(w http.ResponseWriter, log *slog.Logger, vm *vmInfo) {
	if !s.setAutoStart(w, log, vm, true) {
		return
	}
	log.Info("start requested")
	accepted(w, "start requested")
}

// stop turns enableAutoStart off first, or kubemox would start the machine
// again, and then shuts it down: kubemox never powers a machine off.
//
// shortcut: the state is kubemox's last poll, so a machine started under a
// minute ago reads stopped and takes a second stop; read status/current from
// the hypervisor if that shows up in practice.
func (s *server) stop(w http.ResponseWriter, r *http.Request, log *slog.Logger, vm *vmInfo) {
	if !s.setAutoStart(w, log, vm, false) {
		return
	}
	if vm.placed() && vm.State == "running" {
		client, err := s.client(r.Context(), vm)
		if err != nil {
			log.Error("resolving the hypervisor failed", "error", err)
			writeStatus(w, http.StatusInternalServerError, metav1.StatusReasonInternalError, "resolving the hypervisor connection failed")
			return
		}
		if err := client.shutdown(r.Context(), vm.Node, vm.VMID); err != nil {
			s.hypervisorFailed(log, vm, err)
			code, msg := hypervisorStatus(err)
			writeStatus(w, code, metav1.StatusReasonInternalError, msg)
			return
		}
	}
	log.Info("stop requested", "node", vm.Node, "vmid", vm.VMID)
	accepted(w, "stop requested")
}

func (s *server) restart(w http.ResponseWriter, r *http.Request, log *slog.Logger, vm *vmInfo) {
	if !vm.placed() {
		writeStatus(w, http.StatusNotFound, metav1.StatusReasonNotFound, "the VirtualMachine has no machine on the hypervisor yet")
		return
	}
	if vm.State != "running" {
		writeStatus(w, http.StatusConflict, metav1.StatusReasonConflict, fmt.Sprintf("the machine is %s, not running", vm.State))
		return
	}
	client, err := s.client(r.Context(), vm)
	if err != nil {
		log.Error("resolving the hypervisor failed", "error", err)
		writeStatus(w, http.StatusInternalServerError, metav1.StatusReasonInternalError, "resolving the hypervisor connection failed")
		return
	}
	if err := client.reboot(r.Context(), vm.Node, vm.VMID); err != nil {
		s.hypervisorFailed(log, vm, err)
		code, msg := hypervisorStatus(err)
		writeStatus(w, code, metav1.StatusReasonInternalError, msg)
		return
	}
	log.Info("reboot requested", "node", vm.Node, "vmid", vm.VMID)
	accepted(w, "reboot requested")
}

func (s *server) hypervisorFailed(log *slog.Logger, vm *vmInfo, err error) {
	var pe *pveError
	if errors.As(err, &pe) && (pe.Status == http.StatusUnauthorized || pe.Status == http.StatusForbidden) {
		s.connections.forget(vm.Connection)
	}
	log.Error("hypervisor request failed", "node", vm.Node, "vmid", vm.VMID, "error", err)
}

// consoleSession is the part shared by vnc and console: refuse what cannot
// work before the upgrade, so the browser sees a status and a message.
func (s *server) consoleSession(w http.ResponseWriter, r *http.Request, log *slog.Logger, vm *vmInfo) (*pveClient, bool) {
	if !vm.placed() {
		writeStatus(w, http.StatusNotFound, metav1.StatusReasonNotFound, "the VirtualMachine has no machine on the hypervisor yet")
		return nil, false
	}
	if vm.State != "running" {
		writeStatus(w, http.StatusConflict, metav1.StatusReasonConflict,
			fmt.Sprintf("the machine is %s, not running; start it to use the console", vm.State))
		return nil, false
	}
	if !websocket.IsWebSocketUpgrade(r) {
		writeStatus(w, http.StatusBadRequest, metav1.StatusReasonBadRequest, "this subresource is a websocket")
		return nil, false
	}
	client, err := s.client(r.Context(), vm)
	if err != nil {
		log.Error("resolving the hypervisor failed", "error", err)
		writeStatus(w, http.StatusInternalServerError, metav1.StatusReasonInternalError, "resolving the hypervisor connection failed")
		return nil, false
	}
	return client, true
}

func (s *server) vnc(w http.ResponseWriter, r *http.Request, log *slog.Logger, vm *vmInfo) {
	client, ok := s.consoleSession(w, r, log, vm)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handshakeTimeout)
	defer cancel()
	ticket, err := client.vncProxy(ctx, vm.Node, vm.VMID)
	if err != nil {
		s.hypervisorFailed(log, vm, err)
		code, msg := hypervisorStatus(err)
		writeStatus(w, code, metav1.StatusReasonInternalError, msg)
		return
	}
	pve, err := client.dialConsole(ctx, vm.Node, vm.VMID, ticket)
	if err != nil {
		s.hypervisorFailed(log, vm, err)
		code, msg := hypervisorStatus(err)
		writeStatus(w, code, metav1.StatusReasonInternalError, msg)
		return
	}
	defer func() { _ = pve.Close() }()

	upstream := &wsStream{conn: pve}
	if err := authenticateRFB(upstream, ticket.vncPassword()); err != nil {
		log.Error("VNC handshake with the hypervisor failed", "error", err)
		writeStatus(w, http.StatusBadGateway, metav1.StatusReasonInternalError, "the VNC handshake with the hypervisor failed")
		return
	}

	browser, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Info("websocket upgrade failed", "error", err)
		return
	}
	defer func() { _ = browser.Close() }()
	if err := serveRFBNone(&wsStream{conn: browser}); err != nil {
		log.Info("VNC handshake with the browser failed", "error", err)
		return
	}
	log.Info("vnc session open", "node", vm.Node, "vmid", vm.VMID)
	err = relayVNC(browser, pve, upstream.pending())
	closeAfterRelay(browser, pve, err)
	log.Info("vnc session closed", "reason", err)
}

func (s *server) console(w http.ResponseWriter, r *http.Request, log *slog.Logger, vm *vmInfo) {
	client, ok := s.consoleSession(w, r, log, vm)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handshakeTimeout)
	defer cancel()
	ticket, err := client.termProxy(ctx, vm.Node, vm.VMID)
	if err != nil {
		s.hypervisorFailed(log, vm, err)
		code, msg := hypervisorStatus(err)
		writeStatus(w, code, metav1.StatusReasonInternalError, msg)
		return
	}
	pve, err := client.dialConsole(ctx, vm.Node, vm.VMID, ticket)
	if err != nil {
		s.hypervisorFailed(log, vm, err)
		code, msg := hypervisorStatus(err)
		writeStatus(w, code, metav1.StatusReasonInternalError, msg)
		return
	}
	defer func() { _ = pve.Close() }()
	leftover, err := termproxyLogin(pve, ticket.User, ticket.Ticket)
	if err != nil {
		log.Error("termproxy login failed", "error", err)
		writeStatus(w, http.StatusBadGateway, metav1.StatusReasonInternalError, "the terminal handshake with the hypervisor failed")
		return
	}

	browser, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Info("websocket upgrade failed", "error", err)
		return
	}
	defer func() { _ = browser.Close() }()
	log.Info("console session open", "node", vm.Node, "vmid", vm.VMID)
	err = relayConsole(browser, pve, leftover)
	closeAfterRelay(browser, pve, err)
	log.Info("console session closed", "reason", err)
}

// closeAfterRelay tells the side that is still there why the other went.
func closeAfterRelay(browser, pve *websocket.Conn, err error) {
	deadline := time.Now().Add(time.Second)
	switch {
	case err == nil:
	case strings.HasPrefix(err.Error(), "hypervisor:"):
		_ = browser.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "hypervisor connection closed"), deadline)
	default:
		_ = pve.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), deadline)
	}
}
