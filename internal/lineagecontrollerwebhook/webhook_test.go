package lineagecontrollerwebhook

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	schedulerapi "github.com/cozystack/cozystack-scheduler/pkg/apis/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const podWithNumericFields = `{
  "apiVersion": "v1",
  "kind": "Pod",
  "metadata": {
    "name": "harbor-demo-core-0",
    "namespace": "tenant-demo",
    "labels": {"helm.toolkit.fluxcd.io/name": "harbor-demo"},
    "annotations": {"existing": "kept"}
  },
  "spec": {
    "terminationGracePeriodSeconds": 30,
    "activeDeadlineSeconds": 9007199254740993,
    "containers": [{
      "name": "core",
      "image": "goharbor/harbor-core:v2.11.0",
      "ports": [{"containerPort": 8080, "protocol": "TCP"}],
      "resources": {
        "limits": {"cpu": "500m", "memory": "512Mi"},
        "requests": {"cpu": "0.1", "memory": "128Mi"}
      }
    }]
  }
}`

func newHandleTestWebhook(t *testing.T) *LineageControllerWebhook {
	t.Helper()
	scheme := newWebhookScheme(t)
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "tenant-demo",
		Labels: map[string]string{schedulerapi.SchedulingClassLabel: "gpu"},
	}}

	hrGVK := schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}
	appGVK := schema.GroupVersionKind{Group: "apps.cozystack.io", Version: "v1alpha1", Kind: "Harbor"}
	scGVK := schema.GroupVersionKind{Group: schedulerapi.Group, Version: schedulerapi.Version, Kind: "SchedulingClass"}
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(hrGVK, meta.RESTScopeNamespace)
	mapper.Add(appGVK, meta.RESTScopeNamespace)

	obj := func(gvk schema.GroupVersionKind, namespace, name string, labels map[string]string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		u.SetNamespace(namespace)
		u.SetName(name)
		u.SetLabels(labels)
		return u
	}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(),
		obj(hrGVK, "tenant-demo", "harbor-demo", map[string]string{
			ManagerKindKey:  "Harbor",
			ManagerGroupKey: "apps.cozystack.io",
			ManagerNameKey:  "demo",
		}),
		obj(appGVK, "tenant-demo", "demo", nil),
		obj(scGVK, "", "gpu", nil),
	)

	w := &LineageControllerWebhook{
		Client:    fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns).Build(),
		Scheme:    scheme,
		dynClient: dyn,
		mapper:    mapper,
	}
	w.initConfig()
	return w
}

// TestHandle_PatchTouchesOnlyLineageAndScheduling pins that the JSON
// patch computed from the re-marshalled Pod contains the lineage labels
// and scheduler fields and nothing else: numbers the webhook never
// touched, including an int64 beyond float64 precision, must not show
// up as replace operations.
func TestHandle_PatchTouchesOnlyLineageAndScheduling(t *testing.T) {
	w := newHandleTestWebhook(t)

	resp := w.Handle(context.Background(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Kind:      metav1.GroupVersionKind{Version: "v1", Kind: "Pod"},
		Namespace: "tenant-demo",
		Name:      "harbor-demo-core-0",
		Operation: admissionv1.Create,
		Object:    runtime.RawExtension{Raw: []byte(podWithNumericFields)},
	}})

	if !resp.Allowed {
		t.Fatalf("expected allowed response, got %+v", resp.Result)
	}

	want := map[string]any{
		"add /metadata/labels/internal.cozystack.io~1managed-by-cozystack":   "true",
		"add /metadata/labels/apps.cozystack.io~1application.group":          "apps.cozystack.io",
		"add /metadata/labels/apps.cozystack.io~1application.kind":           "Harbor",
		"add /metadata/labels/apps.cozystack.io~1application.name":           "demo",
		"add /metadata/labels/internal.cozystack.io~1tenantresource":         "false",
		"add /metadata/annotations/scheduler.cozystack.io~1scheduling-class": "gpu",
		"add /metadata/labels/scheduler.cozystack.io~1scheduling-class":      "gpu",
		"add /spec/schedulerName":                                            schedulerapi.SchedulerName,
	}

	got := map[string]any{}
	var keys []string
	for _, p := range resp.Patches {
		k := p.Operation + " " + p.Path
		got[k] = p.Value
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(resp.Patches) != len(want) {
		t.Errorf("expected %d patch operations, got %d: %v", len(want), len(resp.Patches), keys)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("patch %q = %#v, want %#v", k, got[k], v)
		}
	}
	for _, p := range resp.Patches {
		if _, ok := want[p.Operation+" "+p.Path]; !ok {
			t.Errorf("unexpected patch operation %s %s = %#v", p.Operation, p.Path, p.Value)
		}
	}
}

// TestHandle_ReadmissionShortCircuit pins which UPDATEs skip the owner walk.
// Every object points at the harbor-demo HelmRelease but names a stale
// application, so an object that goes through the walk gets its labels
// rewritten, and one that skips it gets no patch at all.
func TestHandle_ReadmissionShortCircuit(t *testing.T) {
	fullLabels := func() map[string]string {
		return map[string]string{
			"helm.toolkit.fluxcd.io/name":       "harbor-demo",
			ManagedObjectKey:                    "true",
			ManagerGroupKey:                     "apps.cozystack.io",
			ManagerKindKey:                      "Harbor",
			ManagerNameKey:                      "stale",
			corev1alpha1.TenantResourceLabelKey: "false",
		}
	}
	escape := func(k string) string {
		return "/metadata/labels/" + strings.ReplaceAll(k, "/", "~1")
	}

	tests := []struct {
		name   string
		kind   string
		labels func() map[string]string
		// wantPatches lists "op path" entries that must appear; nil means the
		// response must carry no patch at all.
		wantPatches []string
	}{
		{
			name:   "secret with every lineage label skips the walk",
			kind:   "Secret",
			labels: fullLabels,
		},
		{
			name:        "pod with every lineage label still gets the scheduling fields",
			kind:        "Pod",
			labels:      fullLabels,
			wantPatches: []string{"add /spec/schedulerName", "replace " + escape(ManagerNameKey)},
		},
		{
			name: "secret marked unmanaged goes through the walk",
			kind: "Secret",
			labels: func() map[string]string {
				l := fullLabels()
				l[ManagedObjectKey] = "false"
				return l
			},
			wantPatches: []string{"replace " + escape(ManagedObjectKey), "replace " + escape(ManagerNameKey)},
		},
		{
			name: "secret missing the application kind goes through the walk",
			kind: "Secret",
			labels: func() map[string]string {
				l := fullLabels()
				delete(l, ManagerKindKey)
				return l
			},
			wantPatches: []string{"add " + escape(ManagerKindKey), "replace " + escape(ManagerNameKey)},
		},
		{
			name: "secret missing the tenantresource label goes through the walk",
			kind: "Secret",
			labels: func() map[string]string {
				l := fullLabels()
				delete(l, corev1alpha1.TenantResourceLabelKey)
				return l
			},
			wantPatches: []string{"add " + escape(corev1alpha1.TenantResourceLabelKey), "replace " + escape(ManagerNameKey)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newHandleTestWebhook(t)
			w.config.Store(&runtimeConfig{appCRDMap: map[appRef]*cozyv1alpha1.ApplicationDefinition{
				{"apps.cozystack.io", "Harbor"}: {Spec: cozyv1alpha1.ApplicationDefinitionSpec{
					Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: "Harbor"},
				}},
			}})

			obj := &unstructured.Unstructured{}
			obj.SetAPIVersion("v1")
			obj.SetKind(tt.kind)
			obj.SetNamespace("tenant-demo")
			obj.SetName("harbor-demo-core")
			obj.SetLabels(tt.labels())
			if tt.kind == "Pod" {
				obj.Object["spec"] = map[string]any{"containers": []any{map[string]any{"name": "core", "image": "core"}}}
			}
			raw, err := json.Marshal(obj)
			if err != nil {
				t.Fatalf("marshal object: %v", err)
			}

			resp := w.Handle(context.Background(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
				Kind:      metav1.GroupVersionKind{Version: "v1", Kind: tt.kind},
				Namespace: "tenant-demo",
				Name:      "harbor-demo-core",
				Operation: admissionv1.Update,
				Object:    runtime.RawExtension{Raw: raw},
				OldObject: runtime.RawExtension{Raw: raw},
			}})
			if !resp.Allowed {
				t.Fatalf("expected allowed response, got %+v", resp.Result)
			}

			got := map[string]bool{}
			var keys []string
			for _, p := range resp.Patches {
				k := p.Operation + " " + p.Path
				got[k] = true
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if tt.wantPatches == nil {
				if len(resp.Patches) != 0 {
					t.Errorf("expected no patch, got %v", keys)
				}
				return
			}
			for _, k := range tt.wantPatches {
				if !got[k] {
					t.Errorf("missing patch %q, got %v", k, keys)
				}
			}
		})
	}
}

const testSchedulingClass = "co-region-test"

// newSchedulingClassCR builds an unstructured SchedulingClass CR the fake
// dynamic client can serve.
func newSchedulingClassCR(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   schedulerapi.Group,
		Version: schedulerapi.Version,
		Kind:    "SchedulingClass",
	})
	u.SetName(name)
	return u
}

// newWebhookTestEnv builds a LineageControllerWebhook wired with fakes,
// pre-populated with a Namespace carrying the scheduling-class label and a
// SchedulingClass CR by that name.
func newWebhookTestEnv(t *testing.T, namespace string, classOnNamespace string, classExists bool) *LineageControllerWebhook {
	t.Helper()

	testScheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(testScheme)

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	if classOnNamespace != "" {
		ns.Labels = map[string]string{
			schedulerapi.SchedulingClassLabel: classOnNamespace,
		}
	}

	c := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(ns).
		Build()

	gvr := schema.GroupVersionResource{
		Group:    schedulerapi.Group,
		Version:  schedulerapi.Version,
		Resource: schedulerapi.Resource,
	}

	dynObjs := []runtime.Object{}
	if classExists {
		dynObjs = append(dynObjs, newSchedulingClassCR(classOnNamespace))
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		testScheme,
		map[schema.GroupVersionResource]string{gvr: "SchedulingClassList"},
		dynObjs...,
	)

	return &LineageControllerWebhook{
		Client:    c,
		Scheme:    testScheme,
		dynClient: dyn,
	}
}

// newPod returns a minimal unstructured Pod for mutation tests.
func newPod(namespace, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Pod"})
	u.SetNamespace(namespace)
	u.SetName(name)
	return u
}

func TestApplySchedulingClass_SetsLabelAnnotationAndSchedulerName(t *testing.T) {
	ctx := context.Background()
	h := newWebhookTestEnv(t, "tenant-acme", testSchedulingClass, true)
	pod := newPod("tenant-acme", "db-0")

	if err := h.applySchedulingClass(ctx, pod, nil, "tenant-acme"); err != nil {
		t.Fatalf("applySchedulingClass returned error: %v", err)
	}

	gotLabel := pod.GetLabels()[schedulerapi.SchedulingClassLabel]
	if gotLabel != testSchedulingClass {
		t.Errorf("expected pod label %s=%s, got %q", schedulerapi.SchedulingClassLabel, testSchedulingClass, gotLabel)
	}

	gotAnnotation := pod.GetAnnotations()[schedulerapi.SchedulingClassAnnotation]
	if gotAnnotation != testSchedulingClass {
		t.Errorf("expected pod annotation %s=%s, got %q", schedulerapi.SchedulingClassAnnotation, testSchedulingClass, gotAnnotation)
	}

	gotSched, _, _ := unstructured.NestedString(pod.Object, "spec", "schedulerName")
	if gotSched != schedulerapi.SchedulerName {
		t.Errorf("expected pod spec.schedulerName=%s, got %q", schedulerapi.SchedulerName, gotSched)
	}
}

func TestApplySchedulingClass_PreservesExistingLabels(t *testing.T) {
	ctx := context.Background()
	h := newWebhookTestEnv(t, "tenant-acme", testSchedulingClass, true)
	pod := newPod("tenant-acme", "db-0")
	pod.SetLabels(map[string]string{
		"app.kubernetes.io/name": "postgres",
		"role":                   "primary",
	})

	if err := h.applySchedulingClass(ctx, pod, nil, "tenant-acme"); err != nil {
		t.Fatalf("applySchedulingClass returned error: %v", err)
	}

	labels := pod.GetLabels()
	if labels[schedulerapi.SchedulingClassLabel] != testSchedulingClass {
		t.Errorf("scheduling-class label not set")
	}
	if labels["app.kubernetes.io/name"] != "postgres" {
		t.Errorf("existing label app.kubernetes.io/name was clobbered: %q", labels["app.kubernetes.io/name"])
	}
	if labels["role"] != "primary" {
		t.Errorf("existing label role was clobbered: %q", labels["role"])
	}
}

func TestApplySchedulingClass_NonPodIsNoOp(t *testing.T) {
	ctx := context.Background()
	h := newWebhookTestEnv(t, "tenant-acme", testSchedulingClass, true)

	svc := &unstructured.Unstructured{}
	svc.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Service"})
	svc.SetNamespace("tenant-acme")
	svc.SetName("svc-0")

	if err := h.applySchedulingClass(ctx, svc, nil, "tenant-acme"); err != nil {
		t.Fatalf("applySchedulingClass returned error: %v", err)
	}

	if _, ok := svc.GetLabels()[schedulerapi.SchedulingClassLabel]; ok {
		t.Errorf("non-Pod object should not be mutated")
	}
	if _, ok := svc.GetAnnotations()[schedulerapi.SchedulingClassAnnotation]; ok {
		t.Errorf("non-Pod object should not be mutated")
	}
}

func TestApplySchedulingClass_NoClassOnNamespaceIsNoOp(t *testing.T) {
	ctx := context.Background()
	h := newWebhookTestEnv(t, "tenant-acme", "", false)
	pod := newPod("tenant-acme", "db-0")

	if err := h.applySchedulingClass(ctx, pod, nil, "tenant-acme"); err != nil {
		t.Fatalf("applySchedulingClass returned error: %v", err)
	}

	if _, ok := pod.GetLabels()[schedulerapi.SchedulingClassLabel]; ok {
		t.Errorf("pod label should not be set when namespace has no scheduling-class label")
	}
	if _, ok := pod.GetAnnotations()[schedulerapi.SchedulingClassAnnotation]; ok {
		t.Errorf("pod annotation should not be set when namespace has no scheduling-class label")
	}
	if sched, _, _ := unstructured.NestedString(pod.Object, "spec", "schedulerName"); sched != "" {
		t.Errorf("pod spec.schedulerName should not be set when namespace has no scheduling-class label, got %q", sched)
	}
}

func TestApplySchedulingClass_MissingClassCRSkipsInjection(t *testing.T) {
	ctx := context.Background()
	// Namespace carries the label, but the SchedulingClass CR is NOT created.
	h := newWebhookTestEnv(t, "tenant-acme", testSchedulingClass, false)
	pod := newPod("tenant-acme", "db-0")

	if err := h.applySchedulingClass(ctx, pod, nil, "tenant-acme"); err != nil {
		t.Fatalf("applySchedulingClass returned error: %v", err)
	}

	// Defensive behavior: pod must not be left referencing a non-existent
	// scheduler, otherwise it would stay Pending forever.
	if _, ok := pod.GetLabels()[schedulerapi.SchedulingClassLabel]; ok {
		t.Errorf("pod label should not be set when SchedulingClass CR is missing")
	}
	if _, ok := pod.GetAnnotations()[schedulerapi.SchedulingClassAnnotation]; ok {
		t.Errorf("pod annotation should not be set when SchedulingClass CR is missing")
	}
	if sched, _, _ := unstructured.NestedString(pod.Object, "spec", "schedulerName"); sched != "" {
		t.Errorf("pod spec.schedulerName should not be set when SchedulingClass CR is missing, got %q", sched)
	}
}
