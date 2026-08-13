/*
Copyright 2025 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/kustomize"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/yaml"
)

func TestParseCRDPolicy(t *testing.T) {
	tests := []struct {
		name    string
		install *cozyv1alpha1.ComponentInstall
		want    helmv2.CRDsPolicy
	}{
		{
			name:    "nil install leaves flux default",
			install: nil,
			want:    "",
		},
		{
			name:    "empty upgradeCRDs leaves flux default",
			install: &cozyv1alpha1.ComponentInstall{},
			want:    "",
		},
		{
			name:    "Skip is passed through",
			install: &cozyv1alpha1.ComponentInstall{UpgradeCRDs: "Skip"},
			want:    helmv2.Skip,
		},
		{
			name:    "Create is passed through",
			install: &cozyv1alpha1.ComponentInstall{UpgradeCRDs: "Create"},
			want:    helmv2.Create,
		},
		{
			name:    "CreateReplace is passed through",
			install: &cozyv1alpha1.ComponentInstall{UpgradeCRDs: "CreateReplace"},
			want:    helmv2.CreateReplace,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseCRDPolicy(tc.install)
			if got != tc.want {
				t.Errorf("parseCRDPolicy() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildHelmReleaseSpec(t *testing.T) {
	r := &PackageReconciler{
		HelmReleaseInterval:       42 * time.Second,
		HelmReleaseRetryInterval:  17 * time.Second,
		HelmReleaseInstallTimeout: 11 * time.Minute,
		HelmReleaseUpgradeTimeout: 13 * time.Minute,
		HelmReleaseMaxHistory:     7,
	}
	componentInstall := &cozyv1alpha1.ComponentInstall{UpgradeCRDs: "Skip"}

	spec := r.buildHelmReleaseSpec(componentInstall, "ps-variant-component")

	if spec.Interval.Duration != 42*time.Second {
		t.Errorf("Interval = %v, want 42s", spec.Interval.Duration)
	}
	if spec.MaxHistory == nil {
		t.Fatal("MaxHistory is nil, want pointer to 7")
	}
	if *spec.MaxHistory != 7 {
		t.Errorf("MaxHistory = %d, want 7", *spec.MaxHistory)
	}

	if spec.ChartRef == nil {
		t.Fatal("ChartRef is nil")
	}
	if spec.ChartRef.Kind != "ExternalArtifact" {
		t.Errorf("ChartRef.Kind = %q, want ExternalArtifact", spec.ChartRef.Kind)
	}
	if spec.ChartRef.Name != "ps-variant-component" {
		t.Errorf("ChartRef.Name = %q, want ps-variant-component", spec.ChartRef.Name)
	}
	if spec.ChartRef.Namespace != "cozy-system" {
		t.Errorf("ChartRef.Namespace = %q, want cozy-system", spec.ChartRef.Namespace)
	}

	if spec.Install == nil {
		t.Fatal("Install is nil")
	}
	if spec.Install.Timeout == nil || spec.Install.Timeout.Duration != 11*time.Minute {
		t.Errorf("Install.Timeout = %v, want 11m", spec.Install.Timeout)
	}
	if spec.Install.Strategy == nil {
		t.Fatal("Install.Strategy is nil")
	}
	if spec.Install.Strategy.Name != string(helmv2.ActionStrategyRetryOnFailure) {
		t.Errorf("Install.Strategy.Name = %q, want %q", spec.Install.Strategy.Name, helmv2.ActionStrategyRetryOnFailure)
	}
	if spec.Install.Strategy.RetryInterval == nil || spec.Install.Strategy.RetryInterval.Duration != 17*time.Second {
		t.Errorf("Install.Strategy.RetryInterval = %v, want 17s", spec.Install.Strategy.RetryInterval)
	}
	// Remediation must remain nil: retries are driven solely by
	// Strategy.Name=RetryOnFailure with an explicit RetryInterval.
	// Re-introducing Remediation{Retries: -1} "for safety" would add a
	// second, conflicting retry mechanism alongside the strategy retry
	// path.
	if spec.Install.Remediation != nil {
		t.Errorf("Install.Remediation = %+v, want nil", spec.Install.Remediation)
	}

	if spec.Upgrade == nil {
		t.Fatal("Upgrade is nil")
	}
	if spec.Upgrade.Timeout == nil || spec.Upgrade.Timeout.Duration != 13*time.Minute {
		t.Errorf("Upgrade.Timeout = %v, want 13m", spec.Upgrade.Timeout)
	}
	if spec.Upgrade.Strategy == nil {
		t.Fatal("Upgrade.Strategy is nil")
	}
	if spec.Upgrade.Strategy.Name != string(helmv2.ActionStrategyRetryOnFailure) {
		t.Errorf("Upgrade.Strategy.Name = %q, want %q", spec.Upgrade.Strategy.Name, helmv2.ActionStrategyRetryOnFailure)
	}
	if spec.Upgrade.Strategy.RetryInterval == nil || spec.Upgrade.Strategy.RetryInterval.Duration != 17*time.Second {
		t.Errorf("Upgrade.Strategy.RetryInterval = %v, want 17s", spec.Upgrade.Strategy.RetryInterval)
	}
	if spec.Upgrade.Remediation != nil {
		t.Errorf("Upgrade.Remediation = %+v, want nil", spec.Upgrade.Remediation)
	}
	if spec.Upgrade.CRDs != helmv2.Skip {
		t.Errorf("Upgrade.CRDs = %q, want Skip", spec.Upgrade.CRDs)
	}
}

// TestBuildHelmReleaseSpecZeroMaxHistory pins that MaxHistory=0 (unlimited
// history per Helm semantics) survives the spec build — i.e. is set as a
// non-nil pointer to 0 rather than dropped or replaced with a default.
// buildHelmReleaseSpec threads the kstatus readiness knobs from ComponentInstall
// (issue #2642) onto the generated HelmRelease, coupling the poller default when
// healthCheckExprs are present but no wait strategy was set.
func TestBuildHelmReleaseSpecHealthCheckExprs(t *testing.T) {
	r := &PackageReconciler{HelmReleaseMaxHistory: 7}
	exprs := []kustomize.CustomHealthCheck{{
		APIVersion: "postgresql.cnpg.io/v1",
		Kind:       "Cluster",
	}}

	// exprs present, no explicit strategy -> poller (coupled default)
	spec := r.buildHelmReleaseSpec(&cozyv1alpha1.ComponentInstall{HealthCheckExprs: exprs}, "x")
	if len(spec.HealthCheckExprs) != 1 || spec.HealthCheckExprs[0].Kind != "Cluster" {
		t.Fatalf("HealthCheckExprs not threaded: %+v", spec.HealthCheckExprs)
	}
	if spec.WaitStrategy == nil || spec.WaitStrategy.Name != helmv2.WaitStrategyPoller {
		t.Errorf("WaitStrategy = %+v, want poller (coupled default)", spec.WaitStrategy)
	}

	// explicit legacy honored even with exprs
	spec = r.buildHelmReleaseSpec(&cozyv1alpha1.ComponentInstall{HealthCheckExprs: exprs, WaitStrategy: "legacy"}, "x")
	if spec.WaitStrategy == nil || spec.WaitStrategy.Name != helmv2.WaitStrategyLegacy {
		t.Errorf("WaitStrategy = %+v, want legacy", spec.WaitStrategy)
	}

	// no exprs, no strategy -> unset (flux default)
	spec = r.buildHelmReleaseSpec(&cozyv1alpha1.ComponentInstall{}, "x")
	if spec.WaitStrategy != nil {
		t.Errorf("WaitStrategy = %+v, want nil", spec.WaitStrategy)
	}
	if spec.HealthCheckExprs != nil {
		t.Errorf("HealthCheckExprs = %+v, want nil", spec.HealthCheckExprs)
	}
}

func TestBuildHelmReleaseSpecZeroMaxHistory(t *testing.T) {
	r := &PackageReconciler{HelmReleaseMaxHistory: 0}
	spec := r.buildHelmReleaseSpec(nil, "x")
	if spec.MaxHistory == nil {
		t.Fatal("MaxHistory is nil for HelmReleaseMaxHistory=0; want pointer to 0")
	}
	if *spec.MaxHistory != 0 {
		t.Errorf("MaxHistory = %d, want 0", *spec.MaxHistory)
	}
}

// TestPackageSourceCRDHasUpgradeCRDsEnum guards the generated CRD schema: the
// invalid-value case from the spec is enforced at the API server via a
// kubebuilder enum marker, not in the reconciler. If someone drops the marker
// and forgets to regenerate, this test catches it.
func TestPackageSourceCRDHasUpgradeCRDsEnum(t *testing.T) {
	path := filepath.Join("..", "crdinstall", "manifests", "cozystack.io_packagesources.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(data, &crd); err != nil {
		t.Fatalf("unmarshal CRD: %v", err)
	}

	var field *apiextensionsv1.JSONSchemaProps
	for i := range crd.Spec.Versions {
		v := &crd.Spec.Versions[i]
		if v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
			continue
		}
		spec, ok := v.Schema.OpenAPIV3Schema.Properties["spec"]
		if !ok {
			continue
		}
		variants, ok := spec.Properties["variants"]
		if !ok || variants.Items == nil || variants.Items.Schema == nil {
			continue
		}
		components, ok := variants.Items.Schema.Properties["components"]
		if !ok || components.Items == nil || components.Items.Schema == nil {
			continue
		}
		install, ok := components.Items.Schema.Properties["install"]
		if !ok {
			continue
		}
		f, ok := install.Properties["upgradeCRDs"]
		if !ok {
			continue
		}
		field = &f
		break
	}

	if field == nil {
		t.Fatal("upgradeCRDs field not found in PackageSource CRD schema")
	}

	got := map[string]bool{}
	for _, e := range field.Enum {
		var s string
		if err := json.Unmarshal(e.Raw, &s); err != nil {
			t.Fatalf("unmarshal enum value %q: %v", e.Raw, err)
		}
		got[s] = true
	}

	for _, want := range []string{"Skip", "Create", "CreateReplace"} {
		if !got[want] {
			t.Errorf("enum value %q missing from upgradeCRDs; got %v", want, got)
		}
	}
}

func TestSystemDefaultsLimitRange(t *testing.T) {
	r := &PackageReconciler{
		SystemNamespaceMemoryLimit:   resource.MustParse("4Gi"),
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}

	lr := r.systemDefaultsLimitRange("cozy-metallb")

	if lr.Name != SystemDefaultsLimitRangeName {
		t.Errorf("name = %q, want %q", lr.Name, SystemDefaultsLimitRangeName)
	}
	if lr.Namespace != "cozy-metallb" {
		t.Errorf("namespace = %q, want cozy-metallb", lr.Namespace)
	}
	if len(lr.Spec.Limits) != 1 {
		t.Fatalf("len(limits) = %d, want 1", len(lr.Spec.Limits))
	}

	item := lr.Spec.Limits[0]
	if item.Type != corev1.LimitTypeContainer {
		t.Errorf("type = %q, want Container", item.Type)
	}

	// The default limit is what puts memory.max on the pod cgroup, which is the only
	// thing that removes a pod from the Talos OOM handler's victim set.
	if got := item.Default[corev1.ResourceMemory]; got.Cmp(resource.MustParse("4Gi")) != 0 {
		t.Errorf("default memory = %s, want 4Gi", got.String())
	}
	// defaultRequest must be present and small. If it were omitted, Kubernetes would
	// default each request to the limit and reserve 4Gi per system container.
	got := item.DefaultRequest[corev1.ResourceMemory]
	if got.Cmp(resource.MustParse("32Mi")) != 0 {
		t.Errorf("defaultRequest memory = %s, want 32Mi", got.String())
	}
	if wantMax := item.Default[corev1.ResourceMemory]; got.Cmp(wantMax) > 0 {
		t.Errorf("defaultRequest %s exceeds default %s; the API server rejects such a LimitRange",
			got.String(), wantMax.String())
	}

	// Only memory is defaulted. A default CPU limit would throttle system components.
	if _, ok := item.Default[corev1.ResourceCPU]; ok {
		t.Error("default sets a CPU limit; only memory should be defaulted")
	}
	if _, ok := item.DefaultRequest[corev1.ResourceCPU]; ok {
		t.Error("defaultRequest sets a CPU request; only memory should be defaulted")
	}
}

func TestReconcileSystemDefaultsLimitRangeDisabledRemovesStale(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}

	existing := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SystemDefaultsLimitRangeName,
			Namespace: "cozy-metallb",
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()

	// Zero limit means the knob is disabled; a LimitRange from an earlier
	// configuration must be removed so the setting is reversible.
	r := &PackageReconciler{Client: cl, Scheme: scheme}

	if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-metallb"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	err := cl.Get(t.Context(), types.NamespacedName{
		Name:      SystemDefaultsLimitRangeName,
		Namespace: "cozy-metallb",
	}, &corev1.LimitRange{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("LimitRange still present after disable (err = %v)", err)
	}

	// Reconciling again with nothing to delete must stay a no-op, not surface NotFound.
	if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-metallb"); err != nil {
		t.Fatalf("reconcile on absent LimitRange: %v", err)
	}
}

// limitRangeScheme builds the scheme the LimitRange reconcile tests share.
func limitRangeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	return scheme
}

// systemPod builds a pod with a single container carrying the given memory request and,
// when limit is non-empty, its own memory limit.
func systemPod(name, request, limit string) *corev1.Pod {
	requirements := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(request)},
	}
	if limit != "" {
		requirements.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(limit)}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cozy-monitoring"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Resources: requirements}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func limitRangeExists(t *testing.T, cl client.Client, ns string) bool {
	t.Helper()
	err := cl.Get(t.Context(), types.NamespacedName{
		Name:      SystemDefaultsLimitRangeName,
		Namespace: ns,
	}, &corev1.LimitRange{})
	if err == nil {
		return true
	}
	if apierrors.IsNotFound(err) {
		return false
	}
	t.Fatalf("get LimitRange: %v", err)
	return false
}

// A container requesting more memory than the default limit, with no limit of its own,
// is precisely what LimitRanger would default into an unadmittable pod: the API server
// rejects a request above its limit. The LimitRange must not be applied there, and one
// applied earlier — when the request was still small — must be retracted, or the
// workload can never roll again.
func TestReconcileSystemDefaultsLimitRangeRetractsWhenRequestExceedsDefault(t *testing.T) {
	scheme := limitRangeScheme(t)

	existing := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SystemDefaultsLimitRangeName,
			Namespace: "cozy-monitoring",
		},
	}
	// 8Gi is not an arbitrary number: it is the maxAllowed.memory that
	// packages/system/monitoring ships for vmselect/vmstorage, which the VPA
	// admission webhook can write into requests under updateMode: Initial.
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(existing, systemPod("vmstorage-0", "8Gi", "")).Build()

	r := &PackageReconciler{
		Client:                       cl,
		APIReader:                    cl,
		Scheme:                       scheme,
		SystemNamespaceMemoryLimit:   resource.MustParse("4Gi"),
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}

	if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if limitRangeExists(t, cl, "cozy-monitoring") {
		t.Fatal("LimitRange left in place beside an 8Gi request under a 4Gi default; " +
			"every new pod of that workload would be rejected at admission")
	}
}

// The mirror case, and the one that keeps the guard from being a blanket opt-out:
// LimitRanger never defaults a container that already declares a memory limit, so a
// large request paired with its own limit cannot be broken by the LimitRange and must
// not stop the rest of the namespace being protected.
func TestReconcileSystemDefaultsLimitRangeAppliesWhenLargeRequestCarriesItsOwnLimit(t *testing.T) {
	scheme := limitRangeScheme(t)

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(systemPod("vmstorage-0", "8Gi", "8Gi")).Build()

	r := &PackageReconciler{
		Client:                       cl,
		APIReader:                    cl,
		Scheme:                       scheme,
		SystemNamespaceMemoryLimit:   resource.MustParse("4Gi"),
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}

	if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if !limitRangeExists(t, cl, "cozy-monitoring") {
		t.Fatal("LimitRange withheld from a namespace whose only large request declares its own limit")
	}
}

// A namespace whose pods cannot be read is a namespace where neither applying nor
// retracting is a decision the operator is entitled to make, so it must do neither —
// and it must not report failure either, or a transient API error would fail the whole
// Package reconcile.
func TestReconcileSystemDefaultsLimitRangeSkipsWhenPodsCannotBeRead(t *testing.T) {
	scheme := limitRangeScheme(t)

	existing := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SystemDefaultsLimitRangeName,
			Namespace: "cozy-monitoring",
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(_ context.Context, _ client.WithWatch, list client.ObjectList, _ ...client.ListOption) error {
				if _, ok := list.(*corev1.PodList); ok {
					return apierrors.NewServiceUnavailable("etcd leader changed")
				}
				return nil
			},
		}).Build()

	r := &PackageReconciler{
		Client:                       cl,
		APIReader:                    cl,
		Scheme:                       scheme,
		SystemNamespaceMemoryLimit:   resource.MustParse("4Gi"),
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}

	if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring"); err != nil {
		t.Fatalf("an unreadable pod list must not fail the reconcile: %v", err)
	}
	if !limitRangeExists(t, cl, "cozy-monitoring") {
		t.Fatal("existing LimitRange retracted on the strength of a failed read")
	}
}

// The load-bearing containment property: this feature is opportunistic hardening, so no
// failure inside it may stop a system namespace being reconciled. A namespace that never
// appears leaves its components uninstalled, which is worse than the unbounded memory the
// LimitRange is there to bound.
func TestReconcileNamespacesSurvivesLimitRangeFailure(t *testing.T) {
	scheme := limitRangeScheme(t)
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cozyv1alpha1 to scheme: %v", err)
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*corev1.LimitRange); ok {
					return apierrors.NewForbidden(
						corev1.Resource("limitranges"), SystemDefaultsLimitRangeName,
						fmt.Errorf("denied by policy"))
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
		}).Build()

	r := &PackageReconciler{
		Client:                       cl,
		APIReader:                    cl,
		Scheme:                       scheme,
		SystemNamespaceMemoryLimit:   resource.MustParse("4Gi"),
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}

	pkg := &cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{Name: "platform"}}
	variant := &cozyv1alpha1.Variant{
		Name: "default",
		Components: []cozyv1alpha1.Component{{
			Name:    "metallb",
			Install: &cozyv1alpha1.ComponentInstall{Namespace: "cozy-metallb"},
		}},
	}

	if err := r.reconcileNamespaces(t.Context(), pkg, variant); err != nil {
		t.Fatalf("a rejected LimitRange must not fail namespace reconciliation: %v", err)
	}

	ns := &corev1.Namespace{}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: "cozy-metallb"}, ns); err != nil {
		t.Fatalf("namespace was not created: %v", err)
	}
	if ns.Labels["cozystack.io/system"] != "true" {
		t.Errorf("system label = %q, want true", ns.Labels["cozystack.io/system"])
	}
}

func TestFindRequestAboveDefaultLimit(t *testing.T) {
	scheme := limitRangeScheme(t)

	terminated := systemPod("migrate-once", "8Gi", "")
	terminated.Status.Phase = corev1.PodSucceeded

	initHeavy := systemPod("with-init", "16Mi", "")
	initHeavy.Spec.InitContainers = []corev1.Container{{
		Name: "prepare",
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("6Gi")},
		},
	}}

	tests := []struct {
		name          string
		pods          []client.Object
		wantContainer string
	}{
		{
			name: "no request above the limit",
			pods: []client.Object{systemPod("metallb-speaker", "512Mi", "")},
		},
		{
			name: "a request equal to the limit is admissible",
			pods: []client.Object{systemPod("exactly-at", "4Gi", "")},
		},
		{
			// A finished pod is never recreated from this spec, so it cannot
			// block admission of anything.
			name: "a terminated pod does not count",
			pods: []client.Object{terminated},
		},
		{
			// LimitRanger defaults init containers on the same terms as
			// regular ones, so the scan has to look at both.
			name:          "an init container counts",
			pods:          []client.Object{initHeavy},
			wantContainer: "prepare",
		},
		{
			name:          "the largest offender is reported",
			pods:          []client.Object{systemPod("small", "5Gi", ""), systemPod("big", "9Gi", "")},
			wantContainer: "app",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.pods...).Build()
			r := &PackageReconciler{
				Client:                     cl,
				APIReader:                  cl,
				Scheme:                     scheme,
				SystemNamespaceMemoryLimit: resource.MustParse("4Gi"),
			}

			got, err := r.findRequestAboveDefaultLimit(t.Context(), "cozy-monitoring")
			if err != nil {
				t.Fatalf("findRequestAboveDefaultLimit: %v", err)
			}

			if tt.wantContainer == "" {
				if got != nil {
					t.Fatalf("reported %s/%s (%s) as blocking; nothing should block here",
						got.pod, got.container, got.request.String())
				}
				return
			}
			if got == nil {
				t.Fatal("no blocker reported; a pod that cannot be admitted under the default was missed")
			}
			if got.container != tt.wantContainer {
				t.Errorf("container = %q, want %q", got.container, tt.wantContainer)
			}
		})
	}
}
