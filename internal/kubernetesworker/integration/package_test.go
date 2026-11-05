//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	review "k8s.io/api/admission/v1"
	ar "k8s.io/api/admissionregistration/v1"
	authv1 "k8s.io/api/authentication/v1"
	core "k8s.io/api/core/v1"
	ext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

var hrGVR = schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
var wcGVR = schema.GroupVersionResource{Group: "cozystack.io", Version: "v1alpha1", Resource: "workerclasses"}
var adGVR = schema.GroupVersionResource{Group: "cozystack.io", Version: "v1alpha1", Resource: "applicationdefinitions"}

const ns = "tenant-worker-integration"

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func eventually(t *testing.T, what string, fn func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if fn() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timeout: %s", what)
		case <-tick.C:
		}
	}
}
func crd(group, plural, kind, scope string) *ext.CustomResourceDefinition {
	return &ext.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: plural + "." + group}, Spec: ext.CustomResourceDefinitionSpec{Group: group, Names: ext.CustomResourceDefinitionNames{Plural: plural, Singular: strings.ToLower(kind), Kind: kind, ListKind: kind + "List"}, Scope: ext.ResourceScope(scope), Versions: []ext.CustomResourceDefinitionVersion{{Name: map[bool]string{true: "v2", false: "v1alpha1"}[group == "helm.toolkit.fluxcd.io"], Served: true, Storage: true, Schema: &ext.CustomResourceValidation{OpenAPIV3Schema: &ext.JSONSchemaProps{Type: "object", Properties: map[string]ext.JSONSchemaProps{"spec": {Type: "object", XPreserveUnknownFields: boolp(true)}, "status": {Type: "object", XPreserveUnknownFields: boolp(true)}}}}, Subresources: &ext.CustomResourceSubresources{Status: &ext.CustomResourceSubresourceStatus{}}}}}}
}
func boolp(b bool) *bool                              { return &b }
func u(obj map[string]any) *unstructured.Unstructured { return &unstructured.Unstructured{Object: obj} }
func className(o map[string]any) string {
	s, _, _ := unstructured.NestedString(o, "spec", "workerClassName")
	return s
}
func body(name string, spec map[string]any) map[string]any {
	return map[string]any{"apiVersion": "apps.cozystack.io/v1alpha1", "kind": "KubernetesNodes", "metadata": map[string]any{"name": name, "namespace": ns}, "spec": spec}
}

type harness struct {
	t              *testing.T
	client         *http.Client
	host           string
	dyn            dynamic.Interface
	kube           *kubernetes.Clientset
	ctx            context.Context
	outage         atomic.Bool
	injectConflict atomic.Bool
	conflictChecks atomic.Int32
	eventsMu       sync.Mutex
	events         []map[string]any
	calls          atomic.Int32
}

func (h *harness) request(method, path string, obj any, ct string) (int, map[string]any, string) {
	h.t.Helper()
	var data []byte
	if obj != nil {
		switch v := obj.(type) {
		case []byte:
			data = v
		default:
			data, _ = json.Marshal(obj)
		}
	}
	req, err := http.NewRequestWithContext(h.ctx, method, h.host+path, bytes.NewReader(data))
	check(h.t, err)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := h.client.Do(req)
	check(h.t, err)
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	h.t.Logf("HTTP %s %s -> %d", method, path, resp.StatusCode)
	return resp.StatusCode, out, string(b)
}
func (h *harness) ok(method, path string, obj any, ct string) map[string]any {
	h.t.Helper()
	code, o, b := h.request(method, path, obj, ct)
	if code < 200 || code >= 300 {
		h.t.Fatalf("HTTP %s %s %d: %s", method, path, code, b)
	}
	return o
}
func (h *harness) poolPath(name string) string {
	return "/apis/apps.cozystack.io/v1alpha1/namespaces/" + ns + "/kubernetesnodeses" + map[bool]string{true: "/" + name, false: ""}[name != ""]
}
func (h *harness) persisted(name, class string) {
	h.t.Helper()
	eventually(h.t, "stored HR "+name, func() bool {
		o, e := h.dyn.Resource(hrGVR).Namespace(ns).Get(h.ctx, "kubernetes-nodes-"+name, metav1.GetOptions{})
		if e != nil {
			return false
		}
		v, found, _ := unstructured.NestedString(o.Object, "spec", "values", "workerClassName")
		return v == class && (class != "" || !found)
	})
}
func TestProductionPackage(t *testing.T) {
	if os.Getenv("RUN_WORKERCLASS_INTEGRATION") != "1" {
		t.Skip("explicit opt-in required")
	}

	root, err := os.Getwd()
	check(t, err)
	for {
		if _, e := os.Stat(filepath.Join(root, "go.mod")); e == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("repository root not found")
		}
		root = parent
	}
	output := os.Getenv("WORKERCLASS_TEST_OUTPUT")
	if output == "" {
		output = t.TempDir()
	}
	check(t, os.MkdirAll(output, 0700))
	run, err := os.MkdirTemp(output, "package-run-")
	check(t, err)
	t.Logf("run artifacts: %s", run)
	t.Cleanup(func() {
		_ = filepath.WalkDir(run, func(path string, entry os.DirEntry, e error) error {
			if e == nil && !entry.IsDir() && (entry.Name() == "kubeconfig" || strings.HasSuffix(entry.Name(), ".key") || strings.HasSuffix(entry.Name(), "-key.pem") || strings.HasSuffix(entry.Name(), "-kubeconfig")) {
				return os.Remove(path)
			}
			return e
		})
	})
	apiBinary := os.Getenv("COZY_API_BINARY")
	apiRevision := os.Getenv("COZY_API_REVISION")
	expectStorageFix := os.Getenv("EXPECT_STORAGE_FIX") == "1"
	if apiBinary == "" || apiRevision == "" {
		t.Fatal("COZY_API_BINARY and COZY_API_REVISION must identify an independently built API variant")
	}
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		t.Fatal("KUBEBUILDER_ASSETS must name approved local control-plane binaries")
	}
	revision, e := exec.Command("git", "rev-parse", "HEAD").Output()
	check(t, e)
	t.Logf("package source revision=%s API variant=%s", strings.TrimSpace(string(revision)), apiRevision)
	frontCA, frontCert, frontKey := frontProxy(t, run)
	localIP := selfIP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	paths := []string{filepath.Join(root, "packages/system/application-definition-crd/definition/cozystack.io_applicationdefinitions.yaml"), filepath.Join(root, "packages/system/cozystack-controller/definitions/cozystack.io_workloadmonitors.yaml")}
	env := &envtest.Environment{BinaryAssetsDirectory: assets, CRDDirectoryPaths: paths, CRDs: []*ext.CustomResourceDefinition{crd(hrGVR.Group, hrGVR.Resource, "HelmRelease", "Namespaced")}, ControlPlaneStartTimeout: 45 * time.Second, ControlPlaneStopTimeout: 15 * time.Second}
	env.ControlPlane.GetAPIServer().Configure().Set("enable-aggregator-routing", "true").Set("proxy-client-cert-file", frontCert).Set("proxy-client-key-file", frontKey).Set("requestheader-client-ca-file", frontCA).Set("requestheader-allowed-names", "research-front-proxy")
	hostlog, err := os.Create(filepath.Join(run, "host.log"))
	check(t, err)
	defer func() { _ = hostlog.Close() }()
	env.ControlPlane.GetAPIServer().Out = hostlog
	env.ControlPlane.GetAPIServer().Err = hostlog
	cfg, err := env.Start()
	check(t, err)
	defer func() { check(t, env.Stop()) }()
	kube, err := kubernetes.NewForConfig(cfg)
	check(t, err)
	dyn, err := dynamic.NewForConfig(cfg)
	check(t, err)
	hostVersion, e := kubernetes.NewForConfigOrDie(cfg).Discovery().ServerVersion()
	check(t, e)
	t.Logf("STARTED host Kubernetes %s %s; API %s", hostVersion.GitVersion, cfg.Host, apiRevision)
	_, err = kube.CoreV1().Namespaces().Create(ctx, &core.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
	check(t, err)
	_, err = kube.CoreV1().Secrets(ns).Create(ctx, &core.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cozystack-values", Namespace: ns}, Data: map[string][]byte{"values.yaml": []byte("_cluster:\n  cluster-domain: cozy.local\n_namespace:\n  host: example.com\n")}}, metav1.CreateOptions{})
	check(t, err)

	crds := dyn.Resource(schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"})
	if _, e := crds.Get(ctx, "workerclasses.cozystack.io", metav1.GetOptions{}); !apierrors.IsNotFound(e) {
		t.Fatalf("worker CRD exists before optional package installation: %v", e)
	}
	crdRaw, e := os.ReadFile(filepath.Join(root, "packages/system/kubernetes-worker-classes-crds/definitions/cozystack.io_workerclasses.yaml"))
	check(t, e)
	var workerCRD ext.CustomResourceDefinition
	check(t, yaml.Unmarshal(crdRaw, &workerCRD))
	_, e = envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{CRDs: []*ext.CustomResourceDefinition{&workerCRD}, MaxTime: 25 * time.Second})
	check(t, e)
	rdRaw, e := os.ReadFile(filepath.Join(root, "packages/system/kubernetes-nodes-rd/cozyrds/kubernetes-nodes.yaml"))
	check(t, e)
	var adObject map[string]any
	check(t, yaml.Unmarshal(rdRaw, &adObject))
	adObject["metadata"].(map[string]any)["name"] = "package-integration-kubernetes-nodes"
	_, e = dyn.Resource(adGVR).Create(ctx, u(adObject), metav1.CreateOptions{})
	check(t, e)
	rawSchema, _, e := unstructured.NestedString(adObject, "spec", "application", "openAPISchema")
	check(t, e)
	var poolSchema map[string]any
	check(t, json.Unmarshal([]byte(rawSchema), &poolSchema))
	classSchema := poolSchema["properties"].(map[string]any)["workerClassName"].(map[string]any)
	if _, hasDefault := classSchema["default"]; hasDefault {
		t.Fatal("production class schema default invalidates legacy tests")
	}
	check(t, os.WriteFile(filepath.Join(run, "application-schema.json"), []byte(rawSchema), 0600))
	t.Log("PASS final workerClassName schema has no class default")
	ca := filepath.Join(run, "host-ca.pem")
	clientCA, err := os.ReadFile(filepath.Join(env.ControlPlane.GetAPIServer().CertDir, "client-cert-auth-ca.crt"))
	check(t, err)
	check(t, os.WriteFile(ca, clientCA, 0600))
	kc := filepath.Join(run, "kubeconfig")
	kconfig := clientapi.Config{Clusters: map[string]*clientapi.Cluster{"isolated": {Server: cfg.Host, CertificateAuthorityData: cfg.CAData}}, AuthInfos: map[string]*clientapi.AuthInfo{"admin": {ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData}}, Contexts: map[string]*clientapi.Context{"isolated": {Cluster: "isolated", AuthInfo: "admin"}}, CurrentContext: "isolated"}
	check(t, clientcmd.WriteToFile(kconfig, kc))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	check(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	binary := apiBinary
	cmd := exec.CommandContext(ctx, binary, "--bind-address="+localIP, "--requestheader-client-ca-file="+frontCA, "--requestheader-allowed-names=research-front-proxy", "--secure-port="+strconv.Itoa(port), "--kubeconfig="+kc, "--authentication-kubeconfig="+kc, "--authorization-kubeconfig="+kc, "--client-ca-file="+ca, "--cert-dir="+filepath.Join(run, "certs"))
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kc, "KUBE_FEATURE_WatchListClient=false")
	mainlog, err := os.Create(filepath.Join(run, "cozystack-api.log"))
	check(t, err)
	defer func() { _ = mainlog.Close() }()
	cmd.Stdout = mainlog
	cmd.Stderr = mainlog
	check(t, cmd.Start())
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	appcfg := rest.CopyConfig(cfg)
	appcfg.Host = fmt.Sprintf("https://%s:%d", localIP, port)
	appcfg.Insecure = true
	appcfg.CAData = nil
	appcfg.CAFile = ""
	hc, err := rest.HTTPClientFor(appcfg)
	check(t, err)
	hc.Timeout = 10 * time.Second
	h := &harness{t: t, client: hc, host: appcfg.Host, dyn: dyn, kube: kube, ctx: ctx}
	t.Cleanup(func() {
		h.eventsMu.Lock()
		defer h.eventsMu.Unlock()
		data, e := json.MarshalIndent(h.events, "", "  ")
		if e == nil {
			e = os.WriteFile(filepath.Join(run, "admission-events.json"), append(data, '\n'), 0600)
		}
		if e != nil {
			t.Error(e)
		}
	})

	eventually(t, "selected API ready", func() bool {
		req, _ := http.NewRequestWithContext(ctx, "GET", h.host+"/readyz", nil)
		r, e := hc.Do(req)
		if e != nil {
			return false
		}
		defer func() { _ = r.Body.Close() }()
		return r.StatusCode == 200
	})
	t.Logf("STARTED API %s HTTPS server with real HR storage", apiRevision)
	_, err = kube.CoreV1().Services(ns).Create(ctx, &core.Service{ObjectMeta: metav1.ObjectMeta{Name: "cozystack-api-research", Namespace: ns}, Spec: core.ServiceSpec{Ports: []core.ServicePort{{Port: 443}}}}, metav1.CreateOptions{})
	check(t, err)
	_, err = dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "endpoints"}).Namespace(ns).Create(ctx, u(map[string]any{"apiVersion": "v1", "kind": "Endpoints", "metadata": map[string]any{"name": "cozystack-api-research", "namespace": ns}, "subsets": []any{map[string]any{"addresses": []any{map[string]any{"ip": localIP}}, "ports": []any{map[string]any{"port": int64(port)}}}}}), metav1.CreateOptions{})
	check(t, err)
	apiServices := dyn.Resource(schema.GroupVersionResource{Group: "apiregistration.k8s.io", Version: "v1", Resource: "apiservices"})
	_, err = apiServices.Create(ctx, u(map[string]any{"apiVersion": "apiregistration.k8s.io/v1", "kind": "APIService", "metadata": map[string]any{"name": "v1alpha1.apps.cozystack.io"}, "spec": map[string]any{"group": "apps.cozystack.io", "version": "v1alpha1", "groupPriorityMinimum": int64(1000), "versionPriority": int64(100), "insecureSkipTLSVerify": true, "service": map[string]any{"namespace": ns, "name": "cozystack-api-research", "port": int64(443)}}}), metav1.CreateOptions{})
	check(t, err)
	proxyClient, err := rest.HTTPClientFor(cfg)
	check(t, err)
	proxyClient.Timeout = 10 * time.Second
	proxyHost := strings.TrimSuffix(cfg.Host, "/")
	eventually(t, "real APIService proxy", func() bool {
		req, _ := http.NewRequestWithContext(ctx, "GET", proxyHost+"/apis/apps.cozystack.io/v1alpha1", nil)
		resp, e := proxyClient.Do(req)
		if e != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode == 200
	})
	h.host = proxyHost
	h.client = proxyClient
	t.Log("PASS kube-aggregator APIService HTTPS proxy; subsequent app requests use host endpoint and real HR storage")

	packageProcess := startPackage(t, ctx, root, run, cfg, dyn, kube, localIP)
	defer packageProcess.stop()
	for _, name := range []string{"retiring", "baseline", "concurrent-talos"} {
		_, e := dyn.Resource(wcGVR).Create(ctx, u(map[string]any{"apiVersion": "cozystack.io/v1alpha1", "kind": "WorkerClass", "metadata": map[string]any{"name": name}, "spec": map[string]any{"bootstrap": "talos"}}), metav1.CreateOptions{})
		check(t, e)
	}
	eventually(t, "production finalizer", func() bool {
		wc, e := dyn.Resource(wcGVR).Get(ctx, "retiring", metav1.GetOptions{})
		return e == nil && len(wc.GetFinalizers()) > 0
	})
	relay := newRelay(t, h, packageProcess.backend, packageProcess.ca)
	defer relay.Close()
	fail := ar.Fail
	muturl, valurl := relay.URL+"/mutate-worker-pool", relay.URL+"/validate-worker-pool"
	cab := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: relay.Certificate().Raw})
	m, err := kube.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, packageProcess.registration, metav1.GetOptions{})
	check(t, err)
	m.Webhooks[0].ClientConfig = ar.WebhookClientConfig{URL: &muturl, CABundle: cab}
	_, err = kube.AdmissionregistrationV1().MutatingWebhookConfigurations().Update(ctx, m, metav1.UpdateOptions{})
	check(t, err)
	v, err := kube.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, packageProcess.registration, metav1.GetOptions{})
	check(t, err)
	v.Webhooks[0].ClientConfig = ar.WebhookClientConfig{URL: &valurl, CABundle: cab}
	_, err = kube.AdmissionregistrationV1().ValidatingWebhookConfigurations().Update(ctx, v, metav1.UpdateOptions{})
	check(t, err)
	// Positive control waits for the package configuration to reach API informers.
	eventually(t, "package webhook configured", func() bool {
		code, o, _ := h.request("POST", h.poolPath("")+"?dryRun=All", body("probe", map[string]any{"cluster": "k"}), "application/json")
		return code == 201 && className(o) == "talos" && h.calls.Load() >= 2
	})
	t.Run("CREATE-default", func(t *testing.T) {
		h.t = t
		o := h.ok("POST", h.poolPath(""), body("k-new", map[string]any{"cluster": "k"}), "application/json")
		if className(o) != "talos" {
			t.Fatalf("class=%s", className(o))
		}
		h.persisted("k-new", "talos")
	})
	t.Run("legacy-and-omission", func(t *testing.T) {
		h.t = t
		_, err := dyn.Resource(hrGVR).Namespace(ns).Create(ctx, u(map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease", "metadata": map[string]any{"name": "kubernetes-nodes-k-legacy", "namespace": ns, "labels": map[string]any{"apps.cozystack.io/application.kind": "KubernetesNodes", "apps.cozystack.io/application.group": "apps.cozystack.io", "apps.cozystack.io/application.name": "k-legacy"}}, "spec": map[string]any{"values": map[string]any{"cluster": "k", "maxReplicas": 1}, "chartRef": map[string]any{"kind": "ExternalArtifact", "name": "research", "namespace": ns}, "interval": "5m"}}), metav1.CreateOptions{})
		check(t, err)
		eventually(t, "legacy HR cached", func() bool { c, _, _ := h.request("GET", h.poolPath("k-legacy"), nil, ""); return c == 200 })
		for _, n := range []string{"k-legacy", "k-new"} {
			want := map[string]string{"k-legacy": "", "k-new": "talos"}[n]
			for _, m := range []string{"PUT", "MERGE", "JSON", "APPLY"} {
				var o map[string]any
				switch m {
				case "PUT":
					o = h.ok("PUT", h.poolPath(n), body(n, map[string]any{"cluster": "k", "maxReplicas": 2}), "application/json")
				case "MERGE":
					o = h.ok("PATCH", h.poolPath(n), map[string]any{"spec": map[string]any{"maxReplicas": 3}}, "application/merge-patch+json")
				case "JSON":
					o = h.ok("PATCH", h.poolPath(n), []any{map[string]any{"op": "add", "path": "/spec/replicas", "value": 4}}, "application/json-patch+json")
				case "APPLY":
					o = h.ok("PATCH", h.poolPath(n)+"?fieldManager=research&force=true", body(n, map[string]any{"cluster": "k", "maxReplicas": 5}), "application/apply-patch+yaml")
				}
				if className(o) != want {
					t.Fatalf("%s %s class=%q want %q", n, m, className(o), want)
				}
				h.persisted(n, want)
				t.Logf("PASS %s %s class=%q", n, m, want)
			}
		}
	})
	t.Run("explicit-empty-class", func(t *testing.T) {
		h.t = t
		h.ok("POST", h.poolPath(""), body("k-empty-classed", map[string]any{"cluster": "k"}), "application/json")
		h.persisted("k-empty-classed", "talos")
		_, e := dyn.Resource(hrGVR).Namespace(ns).Create(ctx, u(map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease", "metadata": map[string]any{"name": "kubernetes-nodes-k-empty-legacy", "namespace": ns, "labels": map[string]any{"apps.cozystack.io/application.kind": "KubernetesNodes", "apps.cozystack.io/application.group": "apps.cozystack.io", "apps.cozystack.io/application.name": "k-empty-legacy"}}, "spec": map[string]any{"values": map[string]any{"cluster": "k", "maxReplicas": 1}, "chartRef": map[string]any{"kind": "ExternalArtifact", "name": "research", "namespace": ns}, "interval": "5m"}}), metav1.CreateOptions{})
		check(t, e)
		eventually(t, "empty legacy cached", func() bool { code, _, _ := h.request("GET", h.poolPath("k-empty-legacy"), nil, ""); return code == 200 })
		for _, name := range []string{"k-empty-classed", "k-empty-legacy"} {
			want := map[string]string{"k-empty-classed": "talos", "k-empty-legacy": ""}[name]
			for _, method := range []string{"PUT", "MERGE", "JSON", "APPLY"} {
				h.eventsMu.Lock()
				before := len(h.events)
				h.eventsMu.Unlock()
				var obj map[string]any
				switch method {
				case "PUT":
					obj = h.ok("PUT", h.poolPath(name), body(name, map[string]any{"cluster": "k", "workerClassName": "", "maxReplicas": 2}), "application/json")
				case "MERGE":
					obj = h.ok("PATCH", h.poolPath(name), map[string]any{"spec": map[string]any{"workerClassName": "", "maxReplicas": 3}}, "application/merge-patch+json")
				case "JSON":
					obj = h.ok("PATCH", h.poolPath(name), []any{map[string]any{"op": "add", "path": "/spec/workerClassName", "value": ""}}, "application/json-patch+json")
				case "APPLY":
					obj = h.ok("PATCH", h.poolPath(name)+"?fieldManager=research-empty&force=true", body(name, map[string]any{"cluster": "k", "workerClassName": "", "maxReplicas": 4}), "application/apply-patch+yaml")
				}
				if className(obj) != want {
					t.Fatalf("%s %s explicit empty class=%q want %q", name, method, className(obj), want)
				}
				eventually(t, "explicit-empty stored "+name, func() bool {
					hr, e := dyn.Resource(hrGVR).Namespace(ns).Get(ctx, "kubernetes-nodes-"+name, metav1.GetOptions{})
					if e != nil {
						return false
					}
					v, found, _ := unstructured.NestedString(hr.Object, "spec", "values", "workerClassName")
					return found && v == want
				})
				sawEmpty, sawFinal := false, false
				h.eventsMu.Lock()
				for _, event := range h.events[before:] {
					if event["name"] == name && event["operation"] == "UPDATE" {
						if event["webhook"] == "/mutate-worker-pool" && event["present"] == true && event["value"] == "" {
							sawEmpty = true
						}
						if event["webhook"] == "/validate-worker-pool" && event["present"] == true && event["value"] == want {
							sawFinal = true
						}
					}
				}
				h.eventsMu.Unlock()
				if !sawEmpty || !sawFinal {
					t.Fatalf("%s %s AdmissionReview did not pin explicit-empty input and final class: input=%v final=%v", name, method, sawEmpty, sawFinal)
				}
				t.Logf("PASS %s %s explicit empty: mutating AdmissionReview sees present empty; validating AdmissionReview/stored HR class=%q", name, method, want)
			}
		}
	})
	t.Run("SSA-CREATE", func(t *testing.T) {
		h.t = t
		o := h.ok("PATCH", h.poolPath("k-ssa-create")+"?fieldManager=research&force=true", body("k-ssa-create", map[string]any{"cluster": "k"}), "application/apply-patch+yaml")
		if className(o) != "talos" {
			t.Fatalf("SSA CREATE class=%q", className(o))
		}
		h.persisted("k-ssa-create", "talos")
	})

	t.Run("baseline-and-fixed-contract", func(t *testing.T) {
		h.t = t
		o := h.ok("POST", h.poolPath(""), body("k-baseline", map[string]any{"cluster": "k", "workerClassName": "baseline", "talos": map[string]any{"version": "v1.13.7"}}), "application/json")
		if className(o) != "baseline" {
			t.Fatal("baseline class not preserved")
		}
		_, e := dyn.Resource(wcGVR).Create(ctx, u(map[string]any{"apiVersion": "cozystack.io/v1alpha1", "kind": "WorkerClass", "metadata": map[string]any{"name": "fixed-golden"}, "spec": map[string]any{"bootstrap": "talos", "substrates": map[string]any{"kubevirt": map[string]any{"osImage": map[string]any{"builtin": map[string]any{"version": "v1.13.6", "schematicID": "fixed123"}}}}}}), metav1.CreateOptions{})
		check(t, e)
		code, _, message := h.request("POST", h.poolPath(""), body("k-override", map[string]any{"cluster": "k", "workerClassName": "fixed-golden", "osImage": map[string]any{"factory": map[string]any{}}}), "application/json")
		if code < 400 || !strings.Contains(message, "fixed") {
			t.Fatalf("fixed contract override accepted: %d %s", code, message)
		}
	})
	t.Run("outage-Fail-Ignore", func(t *testing.T) {
		h.t = t
		h.outage.Store(true)
		code, _, _ := h.request("POST", h.poolPath(""), body("k-outage", map[string]any{"cluster": "k"}), "application/json")
		if code < 400 {
			t.Fatal("Fail allowed outage")
		}
		code, _, _ = h.request("PATCH", h.poolPath("k-new"), map[string]any{"spec": map[string]any{"maxReplicas": 7}}, "application/merge-patch+json")
		if code < 400 {
			t.Fatal("Fail allowed UPDATE during outage")
		}
		code, _, _ = h.request("PUT", h.poolPath("k-legacy"), body("k-legacy", map[string]any{"cluster": "k", "maxReplicas": 7}), "application/json")
		if code < 400 {
			t.Fatal("Fail unexpectedly bypassed classless legacy UPDATE")
		}
		t.Logf("PASS measured availability boundary: legacy UPDATE blocked during Fail outage, status=%d; broad resource selectors and no matchConditions", code)

		ignore := ar.Ignore
		m, err := kube.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, packageProcess.registration, metav1.GetOptions{})
		check(t, err)
		m.Webhooks[0].FailurePolicy = &ignore
		_, err = kube.AdmissionregistrationV1().MutatingWebhookConfigurations().Update(ctx, m, metav1.UpdateOptions{})
		check(t, err)
		v, err := kube.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, packageProcess.registration, metav1.GetOptions{})
		check(t, err)
		v.Webhooks[0].FailurePolicy = &ignore
		_, err = kube.AdmissionregistrationV1().ValidatingWebhookConfigurations().Update(ctx, v, metav1.UpdateOptions{})
		check(t, err)
		eventually(t, "Ignore configuration", func() bool {
			c, _, _ := h.request("POST", h.poolPath("")+"?dryRun=All", body("k-ignore-probe", map[string]any{"cluster": "k"}), "application/json")
			return c == 201
		})
		h.outage.Store(false)
		m.Webhooks[0].FailurePolicy = &fail
		m.ResourceVersion = ""
		m, err = kube.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, packageProcess.registration, metav1.GetOptions{})
		check(t, err)
		m.Webhooks[0].FailurePolicy = &fail
		_, err = kube.AdmissionregistrationV1().MutatingWebhookConfigurations().Update(ctx, m, metav1.UpdateOptions{})
		check(t, err)
		v, err = kube.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, packageProcess.registration, metav1.GetOptions{})
		check(t, err)
		v.Webhooks[0].FailurePolicy = &fail
		_, err = kube.AdmissionregistrationV1().ValidatingWebhookConfigurations().Update(ctx, v, metav1.UpdateOptions{})
		check(t, err)
	})
	t.Run("real-VAP", func(t *testing.T) {
		h.t = t
		exact := ar.Exact
		pol := &ar.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "research-local-rule"}, Spec: ar.ValidatingAdmissionPolicySpec{ParamKind: &ar.ParamKind{APIVersion: "cozystack.io/v1alpha1", Kind: "WorkerClass"}, FailurePolicy: &fail, MatchConstraints: &ar.MatchResources{MatchPolicy: &exact, ResourceRules: []ar.NamedRuleWithOperations{{RuleWithOperations: ar.RuleWithOperations{Operations: []ar.OperationType{ar.Create, ar.Update}, Rule: ar.Rule{APIGroups: []string{"apps.cozystack.io"}, APIVersions: []string{"v1alpha1"}, Resources: []string{"kubernetesnodeses"}}}}}}, Validations: []ar.Validation{{Expression: "params.metadata.name == 'talos' && (!has(object.spec.maxReplicas) || object.spec.maxReplicas >= 0)", Message: "package-local-rule"}}}}
		_, err := kube.AdmissionregistrationV1().ValidatingAdmissionPolicies().Create(ctx, pol, metav1.CreateOptions{})
		check(t, err)
		_, err = kube.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Create(ctx, &ar.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: "research-local-rule"}, Spec: ar.ValidatingAdmissionPolicyBindingSpec{ParamRef: &ar.ParamRef{Name: "talos", ParameterNotFoundAction: func() *ar.ParameterNotFoundActionType { v := ar.DenyAction; return &v }()}, PolicyName: pol.Name, ValidationActions: []ar.ValidationAction{ar.Deny}}}, metav1.CreateOptions{})
		check(t, err)
		eventually(t, "real VAP denial", func() bool {
			c, _, b := h.request("POST", h.poolPath("")+"?dryRun=All", body("k-vap-probe", map[string]any{"cluster": "k", "maxReplicas": -1}), "application/json")
			return c >= 400 && strings.Contains(b, "package-local-rule")
		})
		h.ok("POST", h.poolPath(""), body("k-vap-ok", map[string]any{"cluster": "k", "maxReplicas": 1}), "application/json")
	})
	t.Run("WorkerClass-finalizer", func(t *testing.T) {
		h.t = t
		h.ok("POST", h.poolPath(""), body("k-retiring", map[string]any{"cluster": "k", "workerClassName": "retiring"}), "application/json")
		h.persisted("k-retiring", "retiring")
		check(t, dyn.Resource(wcGVR).Delete(ctx, "retiring", metav1.DeleteOptions{}))
		c, e := dyn.Resource(wcGVR).Get(ctx, "retiring", metav1.GetOptions{})
		check(t, e)
		if c.GetDeletionTimestamp() == nil || len(c.GetFinalizers()) == 0 {
			t.Fatal("deleting class was not held")
		}
		h.ok("PATCH", h.poolPath("k-retiring"), map[string]any{"spec": map[string]any{"maxReplicas": 2}}, "application/merge-patch+json")
		code, _, b := h.request("POST", h.poolPath(""), body("k-rejected", map[string]any{"cluster": "k", "workerClassName": "retiring"}), "application/json")
		if code < 400 || !strings.Contains(b, "is deleting") {
			t.Fatalf("new reference admitted: %d %s", code, b)
		}
		code, _, b = h.request("PATCH", h.poolPath("k-new"), map[string]any{"spec": map[string]any{"workerClassName": "retiring"}}, "application/merge-patch+json")
		if code < 400 || !strings.Contains(b, "is deleting") {
			t.Fatalf("UPDATE onto deleting class admitted: %d %s", code, b)
		}
		h.ok("PATCH", h.poolPath("k-retiring"), map[string]any{"spec": map[string]any{"workerClassName": "baseline"}}, "application/merge-patch+json")
		eventually(t, "last reference moved off deleting class", func() bool { _, e := dyn.Resource(wcGVR).Get(ctx, "retiring", metav1.GetOptions{}); return e != nil })
	})
	t.Run("WorkerClass-last-pool-delete", func(t *testing.T) {
		h.t = t
		_, e := dyn.Resource(wcGVR).Create(ctx, u(map[string]any{"apiVersion": "cozystack.io/v1alpha1", "kind": "WorkerClass", "metadata": map[string]any{"name": "retiring-delete"}, "spec": map[string]any{"bootstrap": "talos"}}), metav1.CreateOptions{})
		check(t, e)
		eventually(t, "new class finalized", func() bool {
			c, e := dyn.Resource(wcGVR).Get(ctx, "retiring-delete", metav1.GetOptions{})
			return e == nil && len(c.GetFinalizers()) > 0
		})
		h.ok("POST", h.poolPath(""), body("k-last", map[string]any{"cluster": "k", "workerClassName": "retiring-delete"}), "application/json")
		h.persisted("k-last", "retiring-delete")
		check(t, dyn.Resource(wcGVR).Delete(ctx, "retiring-delete", metav1.DeleteOptions{}))
		c, e := dyn.Resource(wcGVR).Get(ctx, "retiring-delete", metav1.GetOptions{})
		check(t, e)
		if c.GetDeletionTimestamp() == nil {
			t.Fatal("expected Terminating class")
		}
		h.ok("DELETE", h.poolPath("k-last"), map[string]any{"apiVersion": "v1", "kind": "DeleteOptions"}, "application/json")
		eventually(t, "last pool deleted releases class", func() bool {
			_, e := dyn.Resource(wcGVR).Get(ctx, "retiring-delete", metav1.GetOptions{})
			return e != nil
		})
	})
	t.Run("conflicting-writes", func(t *testing.T) {
		h.t = t
		h.ok("POST", h.poolPath(""), body("k-race", map[string]any{"cluster": "k"}), "application/json")
		h.persisted("k-race", "talos")
		h.injectConflict.Store(true)
		checks := h.conflictChecks.Load()
		h.ok("PATCH", h.poolPath("k-race"), map[string]any{"spec": map[string]any{"workerClassName": "baseline"}}, "application/merge-patch+json")
		h.persisted("k-race", "baseline")
		delta := h.conflictChecks.Load() - checks
		if expectStorageFix {
			if delta < 2 {
				t.Fatalf("storage fix did not re-admit: calls=%d", delta)
			}
			t.Logf("PASS API %s: conflict re-admitted, validating calls=%d", apiRevision, delta)
		} else {
			if delta != 1 {
				t.Fatalf("baseline conflict expectation changed: calls=%d", delta)
			}
			t.Logf("KNOWN-FAIL API %s: conflicting spec overwritten without re-admission, calls=%d", apiRevision, delta)
		}
	})
	t.Run("UID-RV", func(t *testing.T) {
		h.t = t
		o := h.ok("GET", h.poolPath("k-new"), nil, "")
		o["metadata"].(map[string]any)["uid"] = "00000000-0000-0000-0000-000000000000"
		code, _, b := h.request("PUT", h.poolPath("k-new"), o, "application/json")
		t.Logf("UID mismatch status=%d: %s", code, b)
		if code < 400 {
			t.Error("UID mismatch accepted")
		}
		o = h.ok("GET", h.poolPath("k-new"), nil, "")
		old := o["metadata"].(map[string]any)["resourceVersion"]
		live, err := dyn.Resource(hrGVR).Namespace(ns).Get(ctx, "kubernetes-nodes-k-new", metav1.GetOptions{})
		check(t, err)
		live.SetAnnotations(map[string]string{"research": "conflict"})
		_, err = dyn.Resource(hrGVR).Namespace(ns).Update(ctx, live, metav1.UpdateOptions{})
		check(t, err)
		code, _, _ = h.request("PUT", h.poolPath("k-new"), o, "application/json")
		if expectStorageFix {
			if code != http.StatusConflict {
				t.Fatalf("stale explicit RV not refused: %d", code)
			}
			t.Logf("PASS API %s: stale RV=%v refused with %d", apiRevision, old, code)
		} else {
			if code != http.StatusOK {
				t.Fatalf("baseline stale-RV expectation changed: %d", code)
			}
			t.Logf("KNOWN-FAIL API %s: stale RV=%v accepted with %d", apiRevision, old, code)
		}
	})
	t.Run("DELETE-precondition", func(t *testing.T) {
		h.t = t
		o := h.ok("GET", h.poolPath("k-new"), nil, "")
		code, _, _ := h.request("DELETE", h.poolPath("k-new"), map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": map[string]any{"uid": "00000000-0000-0000-0000-000000000000"}}, "application/json")
		if code < 400 {
			t.Fatal("delete UID precondition ignored")
		}
		_ = o
	})

	t.Run("denied-class-read-is-fail-closed", func(t *testing.T) {
		h.t = t
		packageProcess.classReadPermission(t, ctx, kube, false)
		eventually(t, "denied custom-class lookup", func() bool {
			code, _, b := h.request("POST", h.poolPath("")+"?dryRun=All", body("k-denied", map[string]any{"cluster": "k", "workerClassName": "baseline"}), "application/json")
			return code >= 400 && strings.Contains(b, "forbidden")
		})
		h.ok("PUT", h.poolPath("k-legacy"), body("k-legacy", map[string]any{"cluster": "k", "maxReplicas": 2}), "application/json")
		packageProcess.classReadPermission(t, ctx, kube, true)
	})
	t.Run("denied-reference-list-retains-finalizer", func(t *testing.T) {
		h.t = t
		_, e := dyn.Resource(wcGVR).Create(ctx, u(map[string]any{"apiVersion": "cozystack.io/v1alpha1", "kind": "WorkerClass", "metadata": map[string]any{"name": "read-error-hold"}, "spec": map[string]any{"bootstrap": "talos"}}), metav1.CreateOptions{})
		check(t, e)
		eventually(t, "class finalized before read error", func() bool {
			c, e := dyn.Resource(wcGVR).Get(ctx, "read-error-hold", metav1.GetOptions{})
			return e == nil && len(c.GetFinalizers()) > 0
		})
		packageProcess.referenceListPermission(t, ctx, kube, false)
		accountCfg, e := clientcmd.BuildConfigFromFlags("", packageProcess.kubeconfig)
		check(t, e)
		accountReader, e := dynamic.NewForConfig(accountCfg)
		check(t, e)
		eventually(t, "reference List actually denied", func() bool {
			_, e := accountReader.Resource(hrGVR).List(ctx, metav1.ListOptions{})
			return apierrors.IsForbidden(e)
		})
		before, _ := os.ReadFile(packageProcess.logPath)
		check(t, dyn.Resource(wcGVR).Delete(ctx, "read-error-hold", metav1.DeleteOptions{}))
		eventually(t, "production controller observes reference error", func() bool {
			raw, _ := os.ReadFile(packageProcess.logPath)
			return len(raw) > len(before) && strings.Contains(string(raw[len(before):]), "listing KubernetesNodes releases")
		})
		held, e := dyn.Resource(wcGVR).Get(ctx, "read-error-hold", metav1.GetOptions{})
		check(t, e)
		if held.GetDeletionTimestamp() == nil || len(held.GetFinalizers()) == 0 {
			t.Fatal("List error released the class")
		}
		packageProcess.referenceListPermission(t, ctx, kube, true)
		eventually(t, "restored reference access releases class", func() bool {
			_, e := dyn.Resource(wcGVR).Get(ctx, "read-error-hold", metav1.GetOptions{})
			return apierrors.IsNotFound(e)
		})
	})
	t.Run("same-name-recreated-class-uid", func(t *testing.T) {
		h.t = t
		makeClass := func() *unstructured.Unstructured {
			return u(map[string]any{"apiVersion": "cozystack.io/v1alpha1", "kind": "WorkerClass", "metadata": map[string]any{"name": "uid-class"}, "spec": map[string]any{"bootstrap": "talos"}})
		}
		old, e := dyn.Resource(wcGVR).Create(ctx, makeClass(), metav1.CreateOptions{})
		check(t, e)
		eventually(t, "old class finalized", func() bool {
			c, e := dyn.Resource(wcGVR).Get(ctx, "uid-class", metav1.GetOptions{})
			return e == nil && len(c.GetFinalizers()) > 0
		})
		check(t, dyn.Resource(wcGVR).Delete(ctx, "uid-class", metav1.DeleteOptions{}))
		eventually(t, "old class removed", func() bool {
			_, e := dyn.Resource(wcGVR).Get(ctx, "uid-class", metav1.GetOptions{})
			return apierrors.IsNotFound(e)
		})
		current, e := dyn.Resource(wcGVR).Create(ctx, makeClass(), metav1.CreateOptions{})
		check(t, e)
		if old.GetUID() == current.GetUID() {
			t.Fatal("same-name recreation reused UID")
		}
		eventually(t, "recreated UID retains its own finalizer", func() bool {
			c, e := dyn.Resource(wcGVR).Get(ctx, "uid-class", metav1.GetOptions{})
			return e == nil && c.GetUID() == current.GetUID() && c.GetDeletionTimestamp() == nil && len(c.GetFinalizers()) > 0
		})
	})
	t.Run("certificate-error-fails-closed", func(t *testing.T) {
		h.t = t
		config, e := kube.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, packageProcess.registration, metav1.GetOptions{})
		check(t, e)
		good := config.Webhooks[0].ClientConfig.CABundle
		config.Webhooks[0].ClientConfig.CABundle = packageProcess.ca
		_, e = kube.AdmissionregistrationV1().MutatingWebhookConfigurations().Update(ctx, config, metav1.UpdateOptions{})
		check(t, e)
		eventually(t, "certificate rejection", func() bool {
			code, _, b := h.request("POST", h.poolPath("")+"?dryRun=All", body("k-cert", map[string]any{"cluster": "k"}), "application/json")
			return code >= 400 && strings.Contains(b, "certificate")
		})
		config, e = kube.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, packageProcess.registration, metav1.GetOptions{})
		check(t, e)
		config.Webhooks[0].ClientConfig.CABundle = good
		_, e = kube.AdmissionregistrationV1().MutatingWebhookConfigurations().Update(ctx, config, metav1.UpdateOptions{})
		check(t, e)
		eventually(t, "certificate trust restored", func() bool {
			code, _, _ := h.request("POST", h.poolPath("")+"?dryRun=All", body("k-cert-ok", map[string]any{"cluster": "k"}), "application/json")
			return code == 201
		})
	})
	t.Run("production-certificate-rotation", func(t *testing.T) {
		h.t = t
		packageProcess.rotateCertificate(t, ctx)
	})
	t.Run("same-name-recreated-pool-uid", func(t *testing.T) {
		h.t = t
		for _, name := range []string{"uid-a", "uid-b"} {
			_, e := dyn.Resource(wcGVR).Create(ctx, u(map[string]any{"apiVersion": "cozystack.io/v1alpha1", "kind": "WorkerClass", "metadata": map[string]any{"name": name}, "spec": map[string]any{"bootstrap": "talos"}}), metav1.CreateOptions{})
			check(t, e)
			eventually(t, "UID test class finalized", func() bool {
				c, e := dyn.Resource(wcGVR).Get(ctx, name, metav1.GetOptions{})
				return e == nil && len(c.GetFinalizers()) > 0
			})
		}
		h.ok("POST", h.poolPath(""), body("k-recreated", map[string]any{"cluster": "k", "workerClassName": "uid-a"}), "application/json")
		old, e := dyn.Resource(hrGVR).Namespace(ns).Get(ctx, "kubernetes-nodes-k-recreated", metav1.GetOptions{})
		check(t, e)
		check(t, dyn.Resource(wcGVR).Delete(ctx, "uid-a", metav1.DeleteOptions{}))
		h.ok("DELETE", h.poolPath("k-recreated"), map[string]any{"apiVersion": "v1", "kind": "DeleteOptions"}, "application/json")
		h.ok("POST", h.poolPath(""), body("k-recreated", map[string]any{"cluster": "k", "workerClassName": "uid-b"}), "application/json")
		current, e := dyn.Resource(hrGVR).Namespace(ns).Get(ctx, "kubernetes-nodes-k-recreated", metav1.GetOptions{})
		check(t, e)
		if current.GetUID() == old.GetUID() {
			t.Fatal("pool recreation reused UID")
		}
		check(t, dyn.Resource(wcGVR).Delete(ctx, "uid-b", metav1.DeleteOptions{}))
		eventually(t, "old pool UID releases its class", func() bool {
			_, e := dyn.Resource(wcGVR).Get(ctx, "uid-a", metav1.GetOptions{})
			return apierrors.IsNotFound(e)
		})
		held, e := dyn.Resource(wcGVR).Get(ctx, "uid-b", metav1.GetOptions{})
		check(t, e)
		if held.GetDeletionTimestamp() == nil || len(held.GetFinalizers()) == 0 {
			t.Fatal("recreated pool did not hold its current class")
		}
		h.ok("DELETE", h.poolPath("k-recreated"), map[string]any{"apiVersion": "v1", "kind": "DeleteOptions"}, "application/json")
		eventually(t, "new pool UID deletion releases its class", func() bool {
			_, e := dyn.Resource(wcGVR).Get(ctx, "uid-b", metav1.GetOptions{})
			return apierrors.IsNotFound(e)
		})
	})
	t.Run("restore-classified-Talos-for-rollback", func(t *testing.T) {
		h.t = t
		// Only discovery/list endpoints are needed here; no provider controllers
		// run in this storage/admission test.
		lookups := []struct{ group, version, plural, kind string }{
			{"cluster.x-k8s.io", "v1beta1", "machinedeployments", "MachineDeployment"},
			{"cluster.x-k8s.io", "v1beta1", "machinesets", "MachineSet"},
			{"cluster.x-k8s.io", "v1beta1", "clusters", "Cluster"},
			{"cluster.x-k8s.io", "v1beta1", "machinehealthchecks", "MachineHealthCheck"},
			{"cilium.io", "v2", "ciliumnetworkpolicies", "CiliumNetworkPolicy"},
			{"controlplane.cluster.x-k8s.io", "v1alpha1", "kamajicontrolplanes", "KamajiControlPlane"},
			{"infrastructure.cluster.x-k8s.io", "v1alpha1", "kubevirtmachinetemplates", "KubevirtMachineTemplate"},
		}
		var lookupCRDs []*ext.CustomResourceDefinition
		for _, kind := range lookups {
			def := crd(kind.group, kind.plural, kind.kind, "Namespaced")
			def.Spec.Versions[0].Name = kind.version
			lookupCRDs = append(lookupCRDs, def)
		}
		_, err := envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{CRDs: lookupCRDs, MaxTime: 25 * time.Second})
		check(t, err)
		oldChart := os.Getenv("WORKERCLASS_LEGACY_CHART")
		if oldChart == "" {
			t.Skip("set WORKERCLASS_LEGACY_CHART to independently extracted rollback chart")
		}
		h.ok("POST", h.poolPath(""), body("k-restore", map[string]any{"cluster": "k", "workerClassName": "baseline", "instanceType": "", "resources": map[string]any{"cpu": "2", "memory": "4Gi"}, "storageClass": "replicated"}), "application/json")
		binDir := filepath.Join(run, "restore-bin")
		check(t, os.Mkdir(binDir, 0700))
		build := exec.CommandContext(ctx, "go", "install", "-buildvcs=false", "./cmd/workerclass-restore")
		build.Dir = root
		build.Env = append(os.Environ(), "GOBIN="+binDir, "GOWORK=off")
		out, e := build.CombinedOutput()
		if e != nil {
			t.Fatalf("build restore command: %v %s", e, out)
		}
		binary := filepath.Join(binDir, "workerclass-restore")
		args := []string{"--kubeconfig=" + kc, "--context=isolated", "--namespace=" + ns, "--release=kubernetes-nodes-k-restore", "--current-chart=" + filepath.Join(root, "packages/apps/kubernetes-nodes"), "--old-chart=" + oldChart}
		command := exec.CommandContext(ctx, binary, args...)
		out, e = command.CombinedOutput()
		if e != nil {
			t.Fatalf("restore preview failed: %v %s", e, out)
		}
		h.persisted("k-restore", "baseline")
		command = exec.CommandContext(ctx, binary, append(args, "--apply")...)
		out, e = command.CombinedOutput()
		if e != nil {
			t.Fatalf("restore apply failed: %v %s", e, out)
		}
		h.persisted("k-restore", "")
	})
	t.Run("package-unregister-lifecycle", func(t *testing.T) {
		h.t = t
		pools, e := dyn.Resource(hrGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
		check(t, e)
		for _, pool := range pools.Items {
			class, _, _ := unstructured.NestedString(pool.Object, "spec", "values", "workerClassName")
			if class != "" {
				check(t, dyn.Resource(hrGVR).Namespace(ns).Delete(ctx, pool.GetName(), metav1.DeleteOptions{}))
			}
		}
		packageProcess.unregister(t, ctx)
		packageProcess.stop()
		check(t, kube.CoreV1().Services(ns).Delete(ctx, "worker-classes", metav1.DeleteOptions{}))
		check(t, kube.AppsV1().Deployments(ns).Delete(ctx, "worker-classes", metav1.DeleteOptions{}))
		if _, e := kube.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, packageProcess.registration, metav1.GetOptions{}); !apierrors.IsNotFound(e) {
			t.Fatalf("MWC outlived backend: %v", e)
		}
		if _, e := kube.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, packageProcess.registration, metav1.GetOptions{}); !apierrors.IsNotFound(e) {
			t.Fatalf("VWC outlived backend: %v", e)
		}
		kept, e := crds.Get(ctx, "workerclasses.cozystack.io", metav1.GetOptions{})
		check(t, e)
		if kept.GetAnnotations()["helm.sh/resource-policy"] != "keep" {
			t.Fatal("CRD retention lost")
		}
		h.ok("PUT", h.poolPath("k-legacy"), body("k-legacy", map[string]any{"cluster": "k", "maxReplicas": 3}), "application/json")
	})
	t.Logf("COMPLETE APIService-proxied selected-API HTTP/storage; webhook calls=%d", h.calls.Load())
	// Fixture-only host resources disappear when this test's etcd/control plane stops.
}

func selfIP(t *testing.T) string {
	t.Helper()
	interfaces, err := net.Interfaces()
	check(t, err)
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || !strings.HasPrefix(iface.Name, "en") {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ip, _, e := net.ParseCIDR(a.String())
			if e == nil && ip.To4() != nil {
				return ip.String()
			}
		}
	}
	t.Fatal("no local physical IPv4 for isolated APIService endpoint")
	return ""
}

func frontProxy(t *testing.T, dir string) (string, string, string) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	check(t, e)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "research-front-ca"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, e := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	check(t, e)
	caCert, e := x509.ParseCertificate(der)
	check(t, e)
	ck, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	check(t, e)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "research-front-proxy"}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	cert, e := x509.CreateCertificate(rand.Reader, leaf, caCert, &ck.PublicKey, key)
	check(t, e)
	kb, e := x509.MarshalECPrivateKey(ck)
	check(t, e)
	cp, lp, kp := filepath.Join(dir, "front-ca.pem"), filepath.Join(dir, "front-client.pem"), filepath.Join(dir, "front-client-key.pem")
	check(t, os.WriteFile(cp, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	check(t, os.WriteFile(lp, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}), 0600))
	check(t, os.WriteFile(kp, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0600))
	return cp, lp, kp
}

type packageRun struct {
	command      *exec.Cmd
	stopped      bool
	binary       string
	kubeconfig   string
	backend      string
	ca           []byte
	registration string
	cleanupArgs  []string
	logPath      string
	issuer       *certificateIssuer
}

func (p *packageRun) stop() {
	if p.stopped {
		return
	}
	p.stopped = true
	_ = p.command.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _ = p.command.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = p.command.Process.Kill()
		<-done
	}
}

func renderPackage(t *testing.T, root string) []*unstructured.Unstructured {
	t.Helper()
	cmd := exec.Command("helm", "template", "worker-classes", filepath.Join(root, "packages/system/kubernetes-worker-classes"), "--namespace", ns)
	data, e := cmd.Output()
	check(t, e)
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var objects []*unstructured.Unstructured
	for {
		var obj unstructured.Unstructured
		e := decoder.Decode(&obj)
		if e == io.EOF {
			break
		}
		check(t, e)
		if obj.GetKind() != "" {
			objects = append(objects, &obj)
		}
	}
	return objects
}

func startPackage(t *testing.T, ctx context.Context, root, run string, host *rest.Config, d dynamic.Interface, k *kubernetes.Clientset, ip string) *packageRun {
	t.Helper()
	objects := renderPackage(t, root)
	httpClient, e := rest.HTTPClientFor(host)
	check(t, e)
	mapper, e := apiutil.NewDynamicRESTMapper(host, httpClient)
	check(t, e)
	var args, cleanup []string
	registration := ""
	certName := ""
	serviceAccount := ""
	for _, obj := range objects {
		switch obj.GetKind() {
		case "Deployment":
			list, _, e := unstructured.NestedStringSlice(obj.Object, "spec", "template", "spec", "containers")
			_ = list
			_ = e
			containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
			for _, raw := range containers {
				for _, a := range raw.(map[string]any)["args"].([]any) {
					args = append(args, strings.ReplaceAll(a.(string), "$(POD_NAMESPACE)", ns))
				}
			}
			serviceAccount, _, _ = unstructured.NestedString(obj.Object, "spec", "template", "spec", "serviceAccountName")
		case "Job":
			containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
			for _, a := range containers[0].(map[string]any)["args"].([]any) {
				cleanup = append(cleanup, a.(string))
			}
			continue
		case "Certificate":
			if obj.GetName() == "worker-classes" {
				certName, _, _ = unstructured.NestedString(obj.Object, "spec", "secretName")
			}
			continue
		case "Issuer":
			continue
		case "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration":
			registration = obj.GetName()
			continue
		}
		mapping, e := mapper.RESTMapping(obj.GroupVersionKind().GroupKind(), obj.GetAPIVersion()[strings.LastIndex(obj.GetAPIVersion(), "/")+1:])
		check(t, e)
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			_, e = d.Resource(mapping.Resource).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{})
		} else {
			_, e = d.Resource(mapping.Resource).Create(ctx, obj, metav1.CreateOptions{})
		}
		check(t, e)
	}
	ca, cert, key, issuer := servingCertificate(t, run, ip)
	_, e = k.CoreV1().Secrets(ns).Create(ctx, &core.Secret{ObjectMeta: metav1.ObjectMeta{Name: certName, Namespace: ns}, Type: core.SecretTypeTLS, Data: map[string][]byte{"tls.crt": cert, "tls.key": key}}, metav1.CreateOptions{})
	check(t, e)
	token, e := k.CoreV1().ServiceAccounts(ns).CreateToken(ctx, serviceAccount, &authv1.TokenRequest{}, metav1.CreateOptions{})
	check(t, e)
	accountConfig := filepath.Join(run, "worker-kubeconfig")
	config := clientapi.Config{Clusters: map[string]*clientapi.Cluster{"isolated": {Server: host.Host, CertificateAuthorityData: host.CAData}}, AuthInfos: map[string]*clientapi.AuthInfo{"worker": {Token: token.Status.Token}}, Contexts: map[string]*clientapi.Context{"isolated": {Cluster: "isolated", AuthInfo: "worker"}}, CurrentContext: "isolated"}
	check(t, clientcmd.WriteToFile(config, accountConfig))
	bind, e := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	check(t, e)
	port := bind.Addr().(*net.TCPAddr).Port
	_ = bind.Close()
	probe, e := net.Listen("tcp", "127.0.0.1:0")
	check(t, e)
	probeAddr := probe.Addr().String()
	_ = probe.Close()
	binDir := filepath.Join(run, "bin")
	check(t, os.Mkdir(binDir, 0700))
	build := exec.CommandContext(ctx, "go", "install", "-buildvcs=false", "./cmd/kubernetes-worker-controller")
	build.Dir = root
	build.Env = append(os.Environ(), "GOBIN="+binDir, "GOWORK=off")
	out, e := build.CombinedOutput()
	if e != nil {
		t.Fatalf("build production worker binary: %v\n%s", e, out)
	}
	binary := filepath.Join(binDir, "kubernetes-worker-controller")
	args = append(args, "--kubeconfig="+accountConfig, "--webhook-bind-address="+ip, "--webhook-port="+strconv.Itoa(port), "--health-probe-bind-address="+probeAddr)
	// These two paths map the chart's Secret volume into the host process.
	for i, a := range args {
		if strings.HasPrefix(a, "--cert-dir=") {
			args[i] = "--cert-dir=" + filepath.Join(run, "serving")
		}
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = append(os.Environ(), "KUBE_FEATURE_WatchListClient=false")
	logPath := filepath.Join(run, "worker-package.log")
	log, e := os.Create(logPath)
	check(t, e)
	t.Cleanup(func() { _ = log.Close() })
	cmd.Stdout = log
	cmd.Stderr = log
	check(t, cmd.Start())
	p := &packageRun{command: cmd, binary: binary, kubeconfig: accountConfig, backend: "https://" + net.JoinHostPort(ip, strconv.Itoa(port)), ca: ca, registration: registration, cleanupArgs: cleanup, logPath: logPath, issuer: issuer}
	t.Cleanup(p.stop)
	eventually(t, "production webhook readiness", func() bool {
		req, e := http.NewRequestWithContext(ctx, "GET", "http://"+probeAddr+"/readyz", nil)
		if e != nil {
			return false
		}
		resp, e := (&http.Client{Timeout: 2 * time.Second}).Do(req)
		if e != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode == 200
	})
	// URL routing is a documented local topology override. The business handlers,
	// Service/RBAC definitions and unregister command remain the shipped ones.
	for _, obj := range objects {
		if obj.GetKind() != "MutatingWebhookConfiguration" && obj.GetKind() != "ValidatingWebhookConfiguration" {
			continue
		}
		hooks, _, _ := unstructured.NestedSlice(obj.Object, "webhooks")
		for _, raw := range hooks {
			hook := raw.(map[string]any)
			cc := hook["clientConfig"].(map[string]any)
			path := cc["service"].(map[string]any)["path"].(string)
			delete(cc, "service")
			cc["url"] = p.backend + path
			cc["caBundle"] = base64.StdEncoding.EncodeToString(ca)
		}
		check(t, unstructured.SetNestedSlice(obj.Object, hooks, "webhooks"))
		mapping, e := mapper.RESTMapping(obj.GroupVersionKind().GroupKind(), "v1")
		check(t, e)
		_, e = d.Resource(mapping.Resource).Create(ctx, obj, metav1.CreateOptions{})
		check(t, e)
	}
	return p
}

type certificateIssuer struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func servingCertificate(t *testing.T, run, ip string) ([]byte, []byte, []byte, *certificateIssuer) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	check(t, e)
	root := &x509.Certificate{SerialNumber: big.NewInt(100), Subject: pkix.Name{CommonName: "worker-package-integration-ca"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, e := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	check(t, e)
	caCert, e := x509.ParseCertificate(der)
	check(t, e)
	leafKey, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	check(t, e)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(101), Subject: pkix.Name{CommonName: "worker-classes"}, NotBefore: root.NotBefore, NotAfter: root.NotAfter, IPAddresses: []net.IP{net.ParseIP(ip)}, DNSNames: []string{"worker-classes." + ns + ".svc"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	cert, e := x509.CreateCertificate(rand.Reader, leaf, caCert, &leafKey.PublicKey, key)
	check(t, e)
	private, e := x509.MarshalECPrivateKey(leafKey)
	check(t, e)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private})
	dir := filepath.Join(run, "serving")
	check(t, os.Mkdir(dir, 0700))
	check(t, os.WriteFile(filepath.Join(dir, "tls.crt"), certPEM, 0600))
	check(t, os.WriteFile(filepath.Join(dir, "tls.key"), keyPEM, 0600))
	return caPEM, certPEM, keyPEM, &certificateIssuer{cert: caCert, key: key}
}

func newRelay(t *testing.T, h *harness, backend string, ca []byte) *httptest.Server {
	target, e := url.Parse(backend)
	check(t, e)
	trust := x509.NewCertPool()
	trust.AppendCertsFromPEM(ca)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: trust, MinVersion: tls.VersionTLS12}}
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.calls.Add(1)
		if h.outage.Load() {
			http.Error(w, "transport outage", http.StatusServiceUnavailable)
			return
		}
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var in review.AdmissionReview
		if e = json.Unmarshal(raw, &in); e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		a := in.Request
		var obj, old map[string]any
		_ = json.Unmarshal(a.Object.Raw, &obj)
		_ = json.Unmarshal(a.OldObject.Raw, &old)
		value, present, _ := unstructured.NestedFieldNoCopy(obj, "spec", "workerClassName")
		oldValue, oldPresent, _ := unstructured.NestedFieldNoCopy(old, "spec", "workerClassName")
		h.eventsMu.Lock()
		h.events = append(h.events, map[string]any{"name": a.Name, "operation": string(a.Operation), "webhook": r.URL.Path, "classField": "workerClassName", "present": present, "value": value, "oldPresent": oldPresent, "oldValue": oldValue})
		h.eventsMu.Unlock()
		if r.URL.Path == "/validate-worker-pool" && a.Operation == review.Update && a.Name == "k-race" {
			h.conflictChecks.Add(1)
			if h.injectConflict.CompareAndSwap(true, false) {
				live, e := h.dyn.Resource(hrGVR).Namespace(ns).Get(r.Context(), "kubernetes-nodes-k-race", metav1.GetOptions{})
				if e == nil {
					e = unstructured.SetNestedField(live.Object, "concurrent-talos", "spec", "values", "workerClassName")
				}
				if e == nil {
					_, e = h.dyn.Resource(hrGVR).Namespace(ns).Update(r.Context(), live, metav1.UpdateOptions{})
				}
				if e != nil {
					http.Error(w, e.Error(), 500)
					return
				}
			}
		}
		proxy.ServeHTTP(w, r)
	}))
}

func (p *packageRun) classReadPermission(t *testing.T, ctx context.Context, k *kubernetes.Clientset, allow bool) {
	role, e := k.RbacV1().ClusterRoles().Get(ctx, p.registration, metav1.GetOptions{})
	check(t, e)
	for i := range role.Rules {
		rule := &role.Rules[i]
		if slices.Contains(rule.Resources, "workerclasses") {
			rule.Verbs = slices.DeleteFunc(rule.Verbs, func(v string) bool { return v == "get" })
			if allow {
				rule.Verbs = append(rule.Verbs, "get")
			}
		}
	}
	_, e = k.RbacV1().ClusterRoles().Update(ctx, role, metav1.UpdateOptions{})
	check(t, e)
}
func (p *packageRun) unregister(t *testing.T, ctx context.Context) {
	t.Helper()
	args := append(slices.Clone(p.cleanupArgs), "--kubeconfig="+p.kubeconfig)
	cmd := exec.CommandContext(ctx, p.binary, args...)
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("production unregister command: %v\n%s", e, out)
	}
}

func (p *packageRun) referenceListPermission(t *testing.T, ctx context.Context, k *kubernetes.Clientset, allow bool) {
	role, e := k.RbacV1().ClusterRoles().Get(ctx, p.registration, metav1.GetOptions{})
	check(t, e)
	for i := range role.Rules {
		rule := &role.Rules[i]
		if slices.Contains(rule.Resources, "helmreleases") {
			rule.Verbs = slices.DeleteFunc(rule.Verbs, func(v string) bool { return v == "list" })
			if allow {
				rule.Verbs = append(rule.Verbs, "list")
			}
		}
	}
	_, e = k.RbacV1().ClusterRoles().Update(ctx, role, metav1.UpdateOptions{})
	check(t, e)
}

func (p *packageRun) rotateCertificate(t *testing.T, ctx context.Context) {
	t.Helper()
	target, e := url.Parse(p.backend)
	check(t, e)
	ip, _, e := net.SplitHostPort(target.Host)
	check(t, e)
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	check(t, e)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(102), Subject: pkix.Name{CommonName: "worker-classes"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP(ip)}, DNSNames: []string{"worker-classes." + ns + ".svc"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, e := x509.CreateCertificate(rand.Reader, leaf, p.issuer.cert, &key.PublicKey, p.issuer.key)
	check(t, e)
	private, e := x509.MarshalECPrivateKey(key)
	check(t, e)
	dir := filepath.Join(filepath.Dir(p.logPath), "serving")
	check(t, os.WriteFile(filepath.Join(dir, "tls.crt.new"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	check(t, os.WriteFile(filepath.Join(dir, "tls.key.new"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}), 0600))
	check(t, os.Rename(filepath.Join(dir, "tls.key.new"), filepath.Join(dir, "tls.key")))
	check(t, os.Rename(filepath.Join(dir, "tls.crt.new"), filepath.Join(dir, "tls.crt")))
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(p.ca)
	eventually(t, "production TLS watcher serves rotated identity", func() bool {
		dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: time.Second}, Config: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
		conn, e := dialer.DialContext(ctx, "tcp", target.Host)
		if e != nil {
			return false
		}
		defer func() { _ = conn.Close() }()
		return conn.(*tls.Conn).ConnectionState().PeerCertificates[0].SerialNumber.Int64() == 102
	})
}
