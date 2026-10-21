package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	testNode   = "pve1"
	testVMID   = 100
	testTicket = "PVEVNC:ticket-with-more-than-eight-bytes"
	testTermTk = "PVETERM:ticket"
)

// fakePVE is the Proxmox API as the proxy uses it.
type fakePVE struct {
	t       *testing.T
	srv     *httptest.Server
	reboots atomic.Int32
	// generatedPassword, when set, is returned by vncproxy as the password
	// the VNC server expects instead of the ticket.
	generatedPassword string
}

func newFakePVE(t *testing.T) *fakePVE {
	t.Helper()
	f := &fakePVE{t: t}
	up := websocket.Upgrader{Subprotocols: []string{"binary"}}
	mux := http.NewServeMux()
	base := "/api2/json/nodes/" + testNode + "/qemu/100"
	auth := func(r *http.Request) bool {
		return r.Header.Get("Authorization") == "PVEAPIToken=kubemox@pve!cozy=s3cr3t"
	}
	mux.HandleFunc("POST "+base+"/vncproxy", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("websocket") != "1" {
			http.Error(w, "websocket=1 expected", http.StatusBadRequest)
			return
		}
		data := map[string]any{"port": "5900", "ticket": testTicket, "user": "kubemox@pve!cozy"}
		if f.generatedPassword != "" {
			data["password"] = f.generatedPassword
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	mux.HandleFunc("POST "+base+"/termproxy", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"port": 5901, "ticket": testTermTk, "user": "kubemox@pve!cozy",
		}})
	})
	mux.HandleFunc("POST "+base+"/status/reboot", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		f.reboots.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": "UPID:pve1:reboot"})
	})
	mux.HandleFunc("GET "+base+"/vncwebsocket", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		port, ticket := r.URL.Query().Get("port"), r.URL.Query().Get("vncticket")
		switch {
		case port == "5900" && ticket == testTicket:
			conn, err := up.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			password := testTicket
			if f.generatedPassword != "" {
				password = f.generatedPassword
			}
			fakeVNCServer(t, &wsStream{conn: conn}, rfbVersion{3, 8}, password, false)
		case port == "5901" && ticket == testTermTk:
			fakeTermproxy(t, "kubemox@pve!cozy", testTermTk, "login: ")(w, r)
		default:
			http.Error(w, "bad ticket", http.StatusUnauthorized)
		}
	})
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// endpoint is host:port, as a ProxmoxConnection names it.
func (f *fakePVE) endpoint() string { return strings.TrimPrefix(f.srv.URL, "https://") }

type testEnv struct {
	pve    *fakePVE
	pki    *testPKI
	proxy  *httptest.Server
	dyn    *dynamicfake.FakeDynamicClient
	sars   []authorizationv1.SubjectAccessReviewSpec
	client *http.Client
}

func vmObject(state string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "proxmox.alperen.cloud/v1alpha1",
		"kind":       "VirtualMachine",
		"metadata":   map[string]any{"name": "vm-instance-demo", "namespace": "tenant-a"},
		"spec": map[string]any{
			"name": "vm-instance-demo", "nodeName": testNode,
			"connectionRef": map[string]any{"name": "pve"},
			"powerState":    "Running",
		},
		"status": map[string]any{"status": map[string]any{
			"id": int64(testVMID), "node": testNode, "state": state,
		}},
	}}
}

func newTestEnv(t *testing.T, vmState string, allowed func(spec authorizationv1.SubjectAccessReviewSpec) bool) *testEnv {
	t.Helper()
	env := &testEnv{pve: newFakePVE(t), pki: newTestPKI(t, "front-proxy-client")}

	connection := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "proxmox.alperen.cloud/v1alpha1",
		"kind":       "ProxmoxConnection",
		"metadata":   map[string]any{"name": "pve"},
		"spec": map[string]any{
			"endpoint": env.pve.endpoint(), "tokenID": "kubemox@pve!cozy", "insecureSkipVerify": true,
			"secretFrom": map[string]any{"name": "pve-token", "namespace": "cozy-proxmox-vm"},
		},
	}}
	scheme := runtime.NewScheme()
	env.dyn = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		vmGVR: "VirtualMachineList", connectionGVR: "ProxmoxConnectionList",
	}, vmObject(vmState), connection)

	core := kubefake.NewClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "pve-token", Namespace: "cozy-proxmox-vm"},
			Data:       map[string][]byte{"token": []byte("s3cr3t\n")},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: authConfigMapName, Namespace: authConfigMapNamespace},
			Data:       env.pki.authData(),
		},
	)
	core.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		sar := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		env.sars = append(env.sars, sar.Spec)
		sar.Status.Allowed = allowed(sar.Spec)
		return true, sar, nil
	})

	auth := &frontProxyAuth{}
	if err := auth.load(context.Background(), core); err != nil {
		t.Fatal(err)
	}
	env.proxy = httptest.NewUnstartedServer(newServer(auth, core, env.dyn).handler())
	env.proxy.TLS = &tls.Config{ClientAuth: tls.RequestClientCert, MinVersion: tls.VersionTLS12}
	env.proxy.StartTLS()
	t.Cleanup(env.proxy.Close)

	env.client = env.proxy.Client()
	env.client.Transport.(*http.Transport).TLSClientConfig.Certificates = []tls.Certificate{env.pki.client}
	return env
}

func (env *testEnv) do(t *testing.T, method, path string, user string) (*http.Response, metav1.Status) {
	t.Helper()
	req, err := http.NewRequest(method, env.proxy.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if user != "" {
		req.Header.Set("X-Remote-User", user)
		req.Header.Set("X-Remote-Group", "system:authenticated")
	}
	resp, err := env.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	var status metav1.Status
	_ = json.Unmarshal(body, &status)
	return resp, status
}

func (env *testEnv) dialWS(t *testing.T, path, subprotocol string, user string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	tlsConfig := env.client.Transport.(*http.Transport).TLSClientConfig
	d := websocket.Dialer{TLSClientConfig: tlsConfig, Subprotocols: []string{subprotocol}}
	header := http.Header{}
	if user != "" {
		header.Set("X-Remote-User", user)
	}
	return d.Dial("wss"+strings.TrimPrefix(env.proxy.URL, "https")+path, header)
}

const vmPath = apiPrefix + "/namespaces/tenant-a/virtualmachines/vm-instance-demo"

func allowAlice(spec authorizationv1.SubjectAccessReviewSpec) bool { return spec.User == "alice" }

func TestDiscovery(t *testing.T) {
	env := newTestEnv(t, "running", allowAlice)
	resp, _ := env.do(t, http.MethodGet, apiPrefix, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("discovery: %d", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, env.proxy.URL+apiPrefix, nil)
	resp, err := env.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var list metav1.APIResourceList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	names := map[string][]string{}
	for _, r := range list.APIResources {
		names[r.Name] = r.Verbs
		if !r.Namespaced {
			t.Errorf("%s is not namespaced", r.Name)
		}
	}
	for sub, verb := range map[string]string{"vnc": "get", "console": "get", "start": "update", "stop": "update", "restart": "update"} {
		if got := names["virtualmachines/"+sub]; len(got) != 1 || got[0] != verb {
			t.Errorf("virtualmachines/%s verbs %v, want %s", sub, got, verb)
		}
	}

	// Without the front-proxy certificate nothing but health is served.
	plain := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // test server
	r, err := plain.Get(env.proxy.URL + apiPrefix)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no client cert: %d", r.StatusCode)
	}
	r, err = plain.Get(env.proxy.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("healthz: %d", r.StatusCode)
	}
}

func TestPowerSubresources(t *testing.T) {
	env := newTestEnv(t, "running", allowAlice)

	resp, status := env.do(t, http.MethodPut, vmPath+"/stop", "alice")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("stop: %d %s", resp.StatusCode, status.Message)
	}
	vm, err := env.dyn.Resource(vmGVR).Namespace("tenant-a").Get(context.Background(), "vm-instance-demo", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, _, _ := unstructured.NestedString(vm.Object, "spec", "powerState"); got != "Stopped" {
		t.Fatalf("powerState after stop: %q", got)
	}
	if resp, _ := env.do(t, http.MethodPost, vmPath+"/start", "alice"); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d", resp.StatusCode)
	}
	vm, _ = env.dyn.Resource(vmGVR).Namespace("tenant-a").Get(context.Background(), "vm-instance-demo", metav1.GetOptions{})
	if got, _, _ := unstructured.NestedString(vm.Object, "spec", "powerState"); got != "Running" {
		t.Fatalf("powerState after start: %q", got)
	}

	if resp, status := env.do(t, http.MethodPut, vmPath+"/restart", "alice"); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("restart: %d %s", resp.StatusCode, status.Message)
	}
	if env.pve.reboots.Load() != 1 {
		t.Fatalf("reboots: %d", env.pve.reboots.Load())
	}

	// The SubjectAccessReview asked for update on the kubemox object.
	last := env.sars[len(env.sars)-1]
	if ra := last.ResourceAttributes; ra.Verb != "update" || ra.Group != vmGroup || ra.Resource != vmResource || ra.Name != "vm-instance-demo" || ra.Namespace != "tenant-a" {
		t.Fatalf("SAR %+v", ra)
	}

	for name, tc := range map[string]struct {
		method, path, user string
		want               int
	}{
		"forbidden user":   {http.MethodPut, vmPath + "/start", "mallory", http.StatusForbidden},
		"no user":          {http.MethodPut, vmPath + "/start", "", http.StatusUnauthorized},
		"wrong method":     {http.MethodGet, vmPath + "/start", "alice", http.StatusMethodNotAllowed},
		"unknown":          {http.MethodPut, vmPath + "/migrate", "alice", http.StatusNotFound},
		"missing machine":  {http.MethodPut, apiPrefix + "/namespaces/tenant-a/virtualmachines/ghost/start", "alice", http.StatusNotFound},
		"vnc not upgraded": {http.MethodGet, vmPath + "/vnc", "alice", http.StatusBadRequest},
	} {
		resp, _ := env.do(t, tc.method, tc.path, tc.user)
		if resp.StatusCode != tc.want {
			t.Errorf("%s: %d, want %d", name, resp.StatusCode, tc.want)
		}
	}
}

func TestConsoleRefusedWhileStopped(t *testing.T) {
	env := newTestEnv(t, "stopped", allowAlice)
	for _, sub := range []string{"vnc", "console", "restart"} {
		method := http.MethodGet
		if sub == "restart" {
			method = http.MethodPut
		}
		resp, status := env.do(t, method, vmPath+"/"+sub, "alice")
		if resp.StatusCode != http.StatusConflict || !strings.Contains(status.Message, "stopped") {
			t.Errorf("%s: %d %q", sub, resp.StatusCode, status.Message)
		}
	}
}

func TestVNCThroughProxy(t *testing.T) {
	for _, generated := range []string{"", "Ab3dEf9h"} {
		t.Run("generatedPassword="+generated, func(t *testing.T) {
			env := newTestEnv(t, "running", allowAlice)
			env.pve.generatedPassword = generated

			conn, resp, err := env.dialWS(t, vmPath+"/vnc", "binary", "alice")
			if err != nil {
				t.Fatalf("dial: %v (%v)", err, resp)
			}
			defer func() { _ = conn.Close() }()
			if conn.Subprotocol() != "binary" {
				t.Fatalf("subprotocol %q", conn.Subprotocol())
			}
			stream := &wsStream{conn: conn}
			if err := fakeNoVNC(t, stream, rfbVersion{3, 8}, rfbSecurityNone); err != nil {
				t.Fatal(err)
			}
			// Past both handshakes the fake VNC server echoes.
			if _, err := stream.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			var got [1]byte
			if _, err := io.ReadFull(stream, got[:]); err != nil || got[0] != 1 {
				t.Fatalf("echo %v %v", got, err)
			}
			// The SubjectAccessReview asked for get.
			if ra := env.sars[len(env.sars)-1].ResourceAttributes; ra.Verb != "get" {
				t.Fatalf("SAR verb %q", ra.Verb)
			}
		})
	}
}

func TestVNCForbidden(t *testing.T) {
	env := newTestEnv(t, "running", allowAlice)
	_, resp, err := env.dialWS(t, vmPath+"/vnc", "binary", "mallory")
	if err == nil {
		t.Fatal("dial succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("resp %v", resp)
	}
}

func TestSerialConsoleThroughProxy(t *testing.T) {
	env := newTestEnv(t, "running", allowAlice)
	conn, resp, err := env.dialWS(t, vmPath+"/console", consoleSubprotocol, "alice")
	if err != nil {
		t.Fatalf("dial: %v (%v)", err, resp)
	}
	defer func() { _ = conn.Close() }()
	if conn.Subprotocol() != consoleSubprotocol {
		t.Fatalf("subprotocol %q", conn.Subprotocol())
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil || string(data) != "login: " {
		t.Fatalf("greeting %q %v", data, err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("uname\n")); err != nil {
		t.Fatal(err)
	}
	_, data, err = conn.ReadMessage()
	if err != nil || string(data) != "UNAME\n" {
		t.Fatalf("echo %q %v", data, err)
	}
}

func TestHypervisorRefusalIsNotRelayed(t *testing.T) {
	env := newTestEnv(t, "running", allowAlice)
	// A wrong token: the hypervisor answers 401, the tenant sees 502 and a
	// message without the token or the hypervisor's words.
	secret, _ := env.dyn.Resource(connectionGVR).Get(context.Background(), "pve", metav1.GetOptions{})
	_ = unstructured.SetNestedField(secret.Object, "kubemox@pve!wrong", "spec", "tokenID")
	if _, err := env.dyn.Resource(connectionGVR).Update(context.Background(), secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	resp, status := env.do(t, http.MethodPut, vmPath+"/restart", "alice")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("restart with a refused token: %d", resp.StatusCode)
	}
	if strings.Contains(status.Message, "wrong") || !strings.Contains(status.Message, "refused") {
		t.Fatalf("message %q", status.Message)
	}
}
