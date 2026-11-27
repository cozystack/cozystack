package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	workers "github.com/cozystack/cozystack/api/workers/v1alpha1"
	"github.com/cozystack/cozystack/internal/kubernetesworker/config"
	"github.com/cozystack/cozystack/internal/kubernetesworker/controller"
	av1 "k8s.io/api/admission/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	ca "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func req(op av1.Operation, pool, old map[string]any) ca.Request {
	raw, _ := json.Marshal(map[string]any{"apiVersion": "apps.cozystack.io/v1alpha1", "kind": "KubernetesNodes", "metadata": map[string]any{"name": "k-md0", "namespace": "tenant-test"}, "spec": pool})
	var oldRaw []byte
	if old != nil {
		oldRaw, _ = json.Marshal(map[string]any{"spec": old})
	}
	return ca.Request{AdmissionRequest: av1.AdmissionRequest{Operation: op, Resource: metav1.GroupVersionResource{Group: "apps.cozystack.io", Version: "v1alpha1", Resource: config.PoolResource().Resource}, Kind: metav1.GroupVersionKind{Group: "apps.cozystack.io", Version: "v1alpha1", Kind: "KubernetesNodes"}, Namespace: "tenant-test", Name: "k-md0", Object: runtime.RawExtension{Raw: raw}, OldObject: runtime.RawExtension{Raw: oldRaw}}}
}
func reader(t *testing.T, objects ...client.Object) client.WithWatch {
	t.Helper()
	s := runtime.NewScheme()
	if err := workers.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).Build()
}
func wc(name, bootstrap string) *workers.WorkerClass {
	return &workers.WorkerClass{ObjectMeta: metav1.ObjectMeta{Name: name, Finalizers: []string{controller.Finalizer}}, Spec: workers.WorkerClassSpec{Bootstrap: workers.Bootstrap(bootstrap)}}
}

func TestDefaultMutation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		op        av1.Operation
		pool, old map[string]any
		want      string
		patch     bool
	}{
		{"create default", av1.Create, map[string]any{}, nil, "talos", true},
		{"create explicit", av1.Create, map[string]any{"workerClassName": "custom"}, nil, "custom", false},
		{"create empty", av1.Create, map[string]any{"workerClassName": ""}, nil, "talos", true},
		{"legacy omission", av1.Update, map[string]any{}, map[string]any{}, "", false},
		{"legacy empty", av1.Update, map[string]any{"workerClassName": ""}, map[string]any{}, "", false},
		{"classified omission", av1.Update, map[string]any{}, map[string]any{"workerClassName": "custom"}, "custom", true},
		{"classified empty", av1.Update, map[string]any{"workerClassName": ""}, map[string]any{"workerClassName": "custom"}, "custom", true},
		{"new selection", av1.Update, map[string]any{"workerClassName": "other"}, map[string]any{"workerClassName": "custom"}, "other", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := (&Mutator{DefaultWorkerClass: "talos"}).Handle(context.Background(), req(tc.op, tc.pool, tc.old))
			if !resp.Allowed {
				t.Fatalf("mutation denied: %v", resp.Result)
			}
			if (len(resp.Patches) > 0) != tc.patch {
				t.Fatalf("patches=%v want patch=%v", resp.Patches, tc.patch)
			}
			if tc.patch {
				if len(resp.Patches) != 1 || resp.Patches[0].Path != "/spec/workerClassName" || fmt.Sprint(resp.Patches[0].Value) != tc.want {
					t.Fatalf("patch=%v want %s", resp.Patches, tc.want)
				}
			}
		})
	}
}

func TestPlatformDefaultOnlyAffectsCreates(t *testing.T) {
	m := &Mutator{DefaultWorkerClass: "kairos"}
	resp := m.Handle(context.Background(), req(av1.Create, map[string]any{}, nil))
	if !resp.Allowed || len(resp.Patches) != 1 || fmt.Sprint(resp.Patches[0].Value) != "kairos" {
		t.Fatalf("create=%v", resp)
	}
	resp = m.Handle(context.Background(), req(av1.Update, map[string]any{}, map[string]any{"workerClassName": "talos"}))
	if !resp.Allowed || len(resp.Patches) != 1 || fmt.Sprint(resp.Patches[0].Value) != "talos" {
		t.Fatalf("update=%v", resp)
	}
}

func TestValidation(t *testing.T) {
	deleting := wc("deleting", "talos")
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	unprotected := wc("unprotected", "talos")
	unprotected.Finalizers = nil
	r := reader(t, wc("talos-custom", "talos"), wc("kairos-custom", "kairos"), deleting, unprotected)
	v := &Validator{Reader: r}
	for _, tc := range []struct {
		name      string
		op        av1.Operation
		pool, old map[string]any
		allow     bool
		message   string
	}{
		{"create built-in", av1.Create, map[string]any{"workerClassName": "talos"}, nil, true, ""},
		{"create missing class field", av1.Create, map[string]any{}, nil, false, "workerClassName"},
		{"custom missing", av1.Create, map[string]any{"workerClassName": "missing"}, nil, false, "missing"},
		{"custom deleting create", av1.Create, map[string]any{"workerClassName": "deleting"}, nil, false, "deleting"},
		{"custom deleting target", av1.Update, map[string]any{"workerClassName": "deleting"}, map[string]any{"workerClassName": "talos"}, false, "deleting"},
		{"class not yet protected from deletion", av1.Create, map[string]any{"workerClassName": "unprotected"}, nil, false, "not yet protected"},
		{"stay on a class not yet protected", av1.Update, map[string]any{"workerClassName": "unprotected"}, map[string]any{"workerClassName": "unprotected"}, true, ""},
		{"move onto a class not yet protected", av1.Update, map[string]any{"workerClassName": "unprotected"}, map[string]any{"workerClassName": "talos"}, false, "not yet protected"},
		{"stay on deleting", av1.Update, map[string]any{"workerClassName": "deleting"}, map[string]any{"workerClassName": "deleting"}, true, ""},
		{"legacy update", av1.Update, map[string]any{}, map[string]any{}, true, ""},
		{"cannot unclassify", av1.Update, map[string]any{}, map[string]any{"workerClassName": "talos-custom"}, false, "workerClassName"},
		{"same family", av1.Update, map[string]any{"workerClassName": "talos-custom"}, map[string]any{"workerClassName": "talos"}, true, ""},
		{"different family", av1.Update, map[string]any{"workerClassName": "kairos-custom"}, map[string]any{"workerClassName": "talos-custom"}, false, "bootstrap"},
		{"legacy cannot become Kairos", av1.Update, map[string]any{"workerClassName": "kairos"}, map[string]any{}, false, "bootstrap"},
		{"Talos SSH rejected", av1.Create, map[string]any{"workerClassName": "talos", "sshKeys": []any{"key"}}, nil, false, "sshKeys"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := v.Handle(context.Background(), req(tc.op, tc.pool, tc.old))
			if resp.Allowed != tc.allow {
				t.Fatalf("allowed=%v result=%v", resp.Allowed, resp.Result)
			}
			if tc.message != "" && (resp.Result == nil || !strings.Contains(resp.Result.Message, tc.message)) {
				t.Fatalf("message=%v want %q", resp.Result, tc.message)
			}
		})
	}
}

func TestMovingOffAGoneClassIsDeniedWithItsReason(t *testing.T) {
	v := &Validator{Reader: reader(t, wc("talos-custom", "talos"))}
	resp := v.Handle(context.Background(), req(av1.Update, map[string]any{"workerClassName": "talos-custom"}, map[string]any{"workerClassName": "gone"}))
	if resp.Allowed || resp.Result == nil || resp.Result.Code != http.StatusForbidden {
		t.Fatalf("want a denial, got %v", resp.Result)
	}
	if !strings.Contains(resp.Result.Message, `previous WorkerClass "gone" no longer exists`) {
		t.Fatalf("message=%q", resp.Result.Message)
	}
}

// The validating webhook sees a write after the API's null normalisation,
// which leaves these optional members in place: a null from the typed client
// or a stored legacy null must be admitted as not set.
func TestNullTalosMembersAreAdmittedAsUnset(t *testing.T) {
	v := &Validator{Reader: reader(t)}
	for _, tc := range []struct {
		name      string
		op        av1.Operation
		pool, old map[string]any
	}{
		{"typed client create on the talos class", av1.Create, map[string]any{"workerClassName": "talos", "version": "v1.35", "talos": map[string]any{"registryMirrors": nil}}, nil},
		{"typed client create on the kairos class", av1.Create, map[string]any{"workerClassName": "kairos", "version": "v1.35", "talos": map[string]any{"registryMirrors": nil}}, nil},
		{"legacy update keeping a stored null CA", av1.Update, map[string]any{"version": "v1.35", "maxReplicas": 3, "talos": map[string]any{"imageFactoryCA": nil}}, map[string]any{"version": "v1.35", "talos": map[string]any{"imageFactoryCA": nil}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if response := v.Handle(context.Background(), req(tc.op, tc.pool, tc.old)); !response.Allowed {
				t.Fatalf("denied: %v", response.Result)
			}
		})
	}
}

func TestLegacyHTTPAliasSurvivesScalingUpdate(t *testing.T) {
	pool := func(schematic string, replicas int) map[string]any {
		return map[string]any{
			"version":     "v1.35",
			"maxReplicas": replicas,
			"talos": map[string]any{
				"version":             "v1.13.6",
				"schematicID":         schematic,
				"imageFactoryURL":     "https://factory.example.com",
				"installerRepository": "registry.example.com/installer",
			},
		}
	}
	v := &Validator{Reader: reader(t)}
	for _, tc := range []struct {
		name, schematic string
		allow           bool
	}{
		{"static HTTP alias", "custom-id", true},
		{"alphanumeric identifier", "custom123", true},
		{"leading hyphen", "-custom", false},
		{"trailing hyphen", "custom-", false},
		{"path separator", "custom/id", false},
		{"whitespace", "custom id", false},
		{"uppercase", "Custom", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := v.Handle(context.Background(), req(av1.Update, pool(tc.schematic, 3), pool("custom-id", 2)))
			if response.Allowed != tc.allow {
				t.Fatalf("allowed=%v result=%v, want %v", response.Allowed, response.Result, tc.allow)
			}
			if !tc.allow && (response.Result == nil || !strings.Contains(response.Result.Message, "schematicID")) {
				t.Fatalf("invalid schematic was not rejected: %v", response.Result)
			}
		})
	}
}

func TestLookupFailureIsClosedButLegacyNeedsNoLookup(t *testing.T) {
	r := interceptor.NewClient(reader(t), interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
		return apierrors.NewForbidden(schema.GroupResource{Group: "cozystack.io", Resource: "workerclasses"}, "custom", fmt.Errorf("read denied"))
	}})
	v := &Validator{Reader: r}
	resp := v.Handle(context.Background(), req(av1.Create, map[string]any{"workerClassName": "custom"}, nil))
	if resp.Allowed || resp.Result == nil || resp.Result.Code != http.StatusInternalServerError {
		t.Fatalf("lookup failed open: %v", resp)
	}
	resp = v.Handle(context.Background(), req(av1.Update, map[string]any{}, map[string]any{}))
	if !resp.Allowed {
		t.Fatalf("legacy should not read class: %v", resp)
	}
}

func TestANullWorkerClassNameIsRefused(t *testing.T) {
	handlers := []ca.Handler{&Mutator{DefaultWorkerClass: "talos"}, &Validator{Reader: reader(t)}}
	null := map[string]any{"workerClassName": nil, "version": "v1.35"}
	for _, r := range []ca.Request{req(av1.Create, null, nil), req(av1.Update, null, map[string]any{"version": "v1.35"}), req(av1.Update, null, map[string]any{"workerClassName": "talos", "version": "v1.35"})} {
		for _, h := range handlers {
			response := h.Handle(context.Background(), r)
			if response.Allowed || !strings.Contains(response.Result.Message, "workerClassName: got null, want string") {
				t.Errorf("%s by %T: allowed=%v %v", r.Operation, h, response.Allowed, response.Result)
			}
		}
	}
}

func TestANullClassStoredOnThePoolIsNamedAsStored(t *testing.T) {
	stored := map[string]any{"workerClassName": nil, "version": "v1.35"}
	for _, tc := range []struct {
		h   ca.Handler
		new map[string]any
	}{
		{&Mutator{DefaultWorkerClass: "talos"}, map[string]any{"version": "v1.35"}},
		{&Validator{Reader: reader(t)}, map[string]any{"workerClassName": "talos", "version": "v1.35"}},
	} {
		response := tc.h.Handle(context.Background(), req(av1.Update, tc.new, stored))
		if response.Allowed || !strings.Contains(response.Result.Message, "stored pool: workerClassName: got null, want string") {
			t.Errorf("%T: allowed=%v %v", tc.h, response.Allowed, response.Result)
		}
	}
}

func TestAnInvalidTalosSettingNamesWhereItWasSet(t *testing.T) {
	empty := ""
	classed := &workers.WorkerClass{ObjectMeta: metav1.ObjectMeta{Name: "empty-version", Finalizers: []string{controller.Finalizer}}, Spec: workers.WorkerClassSpec{Bootstrap: workers.BootstrapTalos, Talos: &workers.TalosSettings{Version: &empty}}}
	factory := &workers.WorkerClass{ObjectMeta: metav1.ObjectMeta{Name: "factory-version", Finalizers: []string{controller.Finalizer}}}
	if err := json.Unmarshal([]byte(`{"bootstrap":"talos","substrates":{"kubevirt":{"osImage":{"factory":{"version":"v1.13"}}}}}`), &factory.Spec); err != nil {
		t.Fatal(err)
	}
	v := &Validator{Reader: reader(t, classed, factory)}
	proxmox := func(pool map[string]any) map[string]any {
		pool["version"], pool["substrate"], pool["proxmox"] = "v1.35", "proxmox", map[string]any{"templateTags": []any{"talos-worker"}}
		return pool
	}
	kubevirt := func(pool map[string]any) map[string]any {
		pool["workerClassName"] = "talos"
		if _, set := pool["version"]; !set {
			pool["version"] = "v1.35"
		}
		return pool
	}
	for _, tc := range []struct {
		name string
		pool map[string]any
		want string
	}{
		{"set on the pool", proxmox(map[string]any{"workerClassName": "talos", "talos": map[string]any{"version": ""}}), `pool: talos.version "" must be a full Talos release; it overrides WorkerClass "talos"`},
		{"set on the class", proxmox(map[string]any{"workerClassName": "empty-version"}), `WorkerClass "empty-version": talos.version "" must be a full release`},
		// The pool still stores the version the pre-WorkerClass schema wrote; on
		// a pool with a class that counts as not set, so the class's value fails.
		{"stored legacy default under an invalid class", proxmox(map[string]any{"workerClassName": "empty-version", "talos": map[string]any{"version": "v1.13.6"}}), `WorkerClass "empty-version": talos.version "" must be a full release`},
		{"schematic set on the pool", kubevirt(map[string]any{"talos": map[string]any{"schematicID": "Bad_ID"}}), `pool: talos.schematicID "Bad_ID" is invalid; it overrides WorkerClass "talos"`},
		{"factory URL set on the pool", kubevirt(map[string]any{"talos": map[string]any{"imageFactoryURL": "ftp://factory.example.com"}}), `pool: talos.imageFactoryURL "ftp://factory.example.com" must be an HTTP(S) URL; it overrides WorkerClass "talos"`},
		{"selected image member set on the pool", kubevirt(map[string]any{"osImage": map[string]any{"factory": map[string]any{"version": "v1.13"}}}), `pool: osImage.factory.version "v1.13" must be a full Talos release; it overrides WorkerClass "talos"`},
		{"the class's selected image member equal to the pool's talos setting", map[string]any{"workerClassName": "factory-version", "version": "v1.35", "talos": map[string]any{"version": "v1.13"}}, `WorkerClass "factory-version": substrates.kubevirt.osImage.factory.version "v1.13" must be a full release`},
		{"the class's selected image member alone", map[string]any{"workerClassName": "factory-version", "version": "v1.35"}, `WorkerClass "factory-version": substrates.kubevirt.osImage.factory.version "v1.13" must be a full release`},
		{"unsupported Kubernetes for the pool's version", kubevirt(map[string]any{"version": "v1.30", "talos": map[string]any{"version": "v1.13.7"}}), `pool: talos.version v1.13.7 does not support Kubernetes v1.30; it overrides WorkerClass "talos"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := v.Handle(context.Background(), req(av1.Create, tc.pool, nil))
			if response.Allowed || response.Result.Message != tc.want {
				t.Fatalf("allowed=%v message=%q, want %q", response.Allowed, response.Result.Message, tc.want)
			}
		})
	}
}

func TestALegacyPoolNamesItsOwnImageMember(t *testing.T) {
	v := &Validator{Reader: reader(t)}
	stored := map[string]any{"version": "v1.35"}
	pool := map[string]any{"version": "v1.35", "osImage": map[string]any{"factory": map[string]any{"version": "v1.13"}}}
	want := `legacy pool: osImage.factory.version "v1.13" must be a full release`
	if response := v.Handle(context.Background(), req(av1.Update, pool, stored)); response.Allowed || response.Result.Message != want {
		t.Fatalf("allowed=%v message=%q, want %q", response.Allowed, response.Result.Message, want)
	}
}

func TestMalformedRequestsFailClosed(t *testing.T) {
	handlers := []ca.Handler{&Mutator{DefaultWorkerClass: "talos"}, &Validator{Reader: reader(t)}}
	for _, h := range handlers {
		for _, r := range []ca.Request{req(av1.Update, map[string]any{}, nil), req(av1.Create, map[string]any{"workerClassName": 123}, nil)} {
			if response := h.Handle(context.Background(), r); response.Allowed {
				t.Errorf("malformed request allowed by %T", h)
			}
		}
	}
}

func TestLegacyPoolsAreNamedAsLegacyInDenials(t *testing.T) {
	v := &Validator{Reader: reader(t, wc("talos-custom", "talos"))}
	resp := v.Handle(context.Background(), req(av1.Update, map[string]any{"sshKeys": []any{"key"}}, map[string]any{}))
	if resp.Allowed || resp.Result == nil || !strings.HasPrefix(resp.Result.Message, "legacy pool: ") {
		t.Fatalf("legacy update denial = %v", resp.Result)
	}
	resp = v.Handle(context.Background(), req(av1.Update, map[string]any{"workerClassName": "talos-custom"}, map[string]any{"sshKeys": []any{"key"}}))
	if resp.Allowed || resp.Result == nil || !strings.HasPrefix(resp.Result.Message, "legacy pool: ") {
		t.Fatalf("adoption from an invalid legacy pool = %v", resp.Result)
	}
}

func TestMovingBetweenClassesWithDifferentTalosSettingsIsAllowed(t *testing.T) {
	newer := wc("talos-newer", "talos")
	version := "v1.13.7"
	newer.Spec.Talos = &workers.TalosSettings{Version: &version}
	v := &Validator{Reader: reader(t, wc("talos-custom", "talos"), newer)}
	resp := v.Handle(context.Background(), req(av1.Update, map[string]any{"workerClassName": "talos-newer"}, map[string]any{"workerClassName": "talos-custom"}))
	if !resp.Allowed {
		t.Fatalf("class move denied: %v", resp.Result)
	}
}

// Existing pools carry every default of the previous schema. Adoption compares
// what the pool renders, so those copies neither block an equivalent class nor
// hide a class that changes the Talos release.
func TestALegacyPoolWithStoredDefaultsAdoptsOnlyAnEquivalentClass(t *testing.T) {
	storedAt := func(class, release string) map[string]any {
		pool := map[string]any{"osImage": map[string]any{}, "talos": map[string]any{
			"version":             release,
			"schematicID":         "ce4c980550dd2ab1b17bbf2b08801c7eb59418eafe8f279833297925d67c7515",
			"imageFactoryURL":     "https://factory.talos.dev",
			"installerRepository": "factory.talos.dev/installer",
			"registryMirrors":     map[string]any{},
			"imageFactoryCA":      "",
		}}
		if class != "" {
			pool["workerClassName"] = class
		}
		return pool
	}
	stored := func(class string) map[string]any { return storedAt(class, "v1.13.12") }
	newer := wc("talos-newer", "talos")
	version := "v1.13.7"
	newer.Spec.Talos = &workers.TalosSettings{Version: &version}
	v := &Validator{Reader: reader(t, newer)}
	if resp := v.Handle(context.Background(), req(av1.Update, stored("talos"), stored(""))); !resp.Allowed {
		t.Fatalf("adoption of the equivalent class denied: %v", resp.Result)
	}
	// A pool stored before the platform default moved keeps the older release,
	// so the built-in class would reimage it.
	if resp := v.Handle(context.Background(), req(av1.Update, storedAt("talos", "v1.13.6"), storedAt("", "v1.13.6"))); resp.Allowed || resp.Result == nil || !strings.Contains(resp.Result.Message, `adopting WorkerClass "talos" changes legacy configuration`) {
		t.Fatalf("adoption of the built-in class from the older stored release = %v", resp.Result)
	}
	resp := v.Handle(context.Background(), req(av1.Update, stored("talos-newer"), stored("")))
	if resp.Allowed || resp.Result == nil || !strings.Contains(resp.Result.Message, `adopting WorkerClass "talos-newer" changes legacy configuration`) || !strings.Contains(resp.Result.Message, "move the pool to the class's Talos settings without a class first") {
		t.Fatalf("adoption of a class with another Talos release = %v", resp.Result)
	}
	moved := stored("")
	moved["talos"].(map[string]any)["version"] = version
	if resp := v.Handle(context.Background(), req(av1.Update, moved, stored(""))); !resp.Allowed {
		t.Fatalf("moving the classless pool to the class's release denied: %v", resp.Result)
	}
	adopted := stored("talos-newer")
	adopted["talos"].(map[string]any)["version"] = version
	if resp := v.Handle(context.Background(), req(av1.Update, adopted, moved)); !resp.Allowed {
		t.Fatalf("adoption after moving to the class's release denied: %v", resp.Result)
	}
}

// A pool leaving a class is checked against that class's bootstrap family
// only: values it can no longer satisfy there must not keep it on the class.
func TestLeavingAClassChecksOnlyItsBootstrapFamily(t *testing.T) {
	fixed := wc("talos-fixed", "talos")
	fixed.Spec.Substrates = &workers.Substrates{KubeVirt: &workers.KubeVirtSource{OSImage: &workers.TalosImageSource{Builtin: &workers.BuiltinSource{Version: "v1.13.6", SchematicID: "fixed123"}}}}
	v := &Validator{Reader: reader(t, fixed)}
	old := map[string]any{"workerClassName": "talos-fixed", "osImage": map[string]any{"factory": map[string]any{}}}
	moved := map[string]any{"workerClassName": "talos", "osImage": map[string]any{"factory": map[string]any{}}}
	if resp := v.Handle(context.Background(), req(av1.Update, moved, old)); !resp.Allowed {
		t.Fatalf("leaving the class denied: %v", resp.Result)
	}
	moved["workerClassName"] = "kairos"
	delete(moved, "osImage")
	resp := v.Handle(context.Background(), req(av1.Update, moved, old))
	if resp.Allowed || resp.Result == nil || !strings.Contains(resp.Result.Message, "cannot switch a running pool's bootstrap family") {
		t.Fatalf("family switch on leaving a class = %v", resp.Result)
	}
}

func TestAFailedReadOfThePreviousClassFailsClosed(t *testing.T) {
	r := interceptor.NewClient(reader(t), interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
		return apierrors.NewForbidden(schema.GroupResource{Group: "cozystack.io", Resource: "workerclasses"}, "custom", fmt.Errorf("read denied"))
	}})
	v := &Validator{Reader: r}
	resp := v.Handle(context.Background(), req(av1.Update, map[string]any{"workerClassName": "talos"}, map[string]any{"workerClassName": "custom"}))
	if resp.Allowed || resp.Result == nil || resp.Result.Code != http.StatusInternalServerError || !strings.Contains(resp.Result.Message, `reading previous WorkerClass "custom"`) {
		t.Fatalf("previous class read failure = %v", resp.Result)
	}
}
