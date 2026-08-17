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
	"reflect"
	"strings"
	"testing"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/kustomize"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
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

	lr := r.systemDefaultsLimitRange("cozy-metallb", r.SystemNamespaceMemoryLimit)

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

// limitRangeScheme builds the scheme the LimitRange reconcile tests share. The scan reads
// workload pod templates as well as pods, so apps/v1 and batch/v1 have to be registered —
// the operator gets them from clientgoscheme.AddToScheme in main.go.
func limitRangeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add appsv1 to scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add batchv1 to scheme: %v", err)
	}
	return scheme
}

// systemPodSpec builds the pod spec every fixture in these tests shares: one container
// carrying the given memory request and, when limit is non-empty, its own memory limit.
//
// The container is named after the object that carries it, so a test asserting which
// container was reported cannot pass by accident when every fixture carries an
// identically named container.
func systemPodSpec(name, request, limit string) corev1.PodSpec {
	requirements := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(request)},
	}
	if limit != "" {
		requirements.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(limit)}
	}
	return corev1.PodSpec{
		Containers: []corev1.Container{{Name: name + "-app", Resources: requirements}},
	}
}

// systemPod builds a running pod with a single container carrying the given memory request
// and, when limit is non-empty, its own memory limit.
func systemPod(name, request, limit string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cozy-monitoring"},
		Spec:       systemPodSpec(name, request, limit),
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

// systemDeployment builds a Deployment whose pod template carries the given request.
// replicas is set for real rather than left nil, so a case named for a scaled-to-zero
// workload is actually scaled to zero.
func systemDeployment(name string, replicas int32, request, limit string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cozy-monitoring"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       systemPodSpec(name, request, limit),
			},
		},
	}
}

// systemStatefulSet builds a StatefulSet whose pod template carries the given request.
func systemStatefulSet(name string, replicas int32, request, limit string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cozy-monitoring"},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       systemPodSpec(name, request, limit),
			},
		},
	}
}

// systemDaemonSet builds a DaemonSet whose pod template carries the given request.
func systemDaemonSet(name, request, limit string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cozy-monitoring"},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       systemPodSpec(name, request, limit),
			},
		},
	}
}

// systemCronJob builds a CronJob whose job template carries the given request. A CronJob
// between runs has no pods at all, which is the state the pod scan cannot see.
func systemCronJob(name, request, limit string) *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cozy-monitoring"},
		Spec: batchv1.CronJobSpec{
			Schedule: "0 * * * *",
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{Spec: systemPodSpec(name, request, limit)},
				},
			},
		},
	}
}

// systemJob builds a Job whose pod template carries the given request.
func systemJob(name, request, limit string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cozy-monitoring"},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{Spec: systemPodSpec(name, request, limit)},
		},
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

// limitRangeDefaultMemory returns the default container memory limit the operator has
// applied in ns.
func limitRangeDefaultMemory(t *testing.T, cl client.Client, ns string) resource.Quantity {
	t.Helper()
	lr := &corev1.LimitRange{}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      SystemDefaultsLimitRangeName,
		Namespace: ns,
	}, lr); err != nil {
		t.Fatalf("get LimitRange in %s: %v", ns, err)
	}
	if len(lr.Spec.Limits) != 1 {
		t.Fatalf("len(limits) = %d, want 1", len(lr.Spec.Limits))
	}
	return lr.Spec.Limits[0].Default[corev1.ResourceMemory]
}

// A container requesting more memory than the configured default, with no limit of its
// own, would be defaulted by LimitRanger into a pod the API server rejects. The namespace
// gets a raised ceiling rather than no ceiling: a LimitRange defaulting 8Gi still puts
// memory.max on the cgroup, which is the only thing that takes the pod out of the Talos OOM
// handler's victim set, while withholding the LimitRange leaves it in that set — the exact
// failure this feature exists to remove.
func TestReconcileSystemDefaultsLimitRangeRaisesCeilingWhenRequestExceedsDefault(t *testing.T) {
	scheme := limitRangeScheme(t)

	existing := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SystemDefaultsLimitRangeName,
			Namespace: "cozy-monitoring",
		},
	}
	// 8Gi is not an arbitrary number: it is the maxAllowed.memory that
	// packages/system/monitoring ships for vmselect/vmstorage, and VPA caps a
	// recommendation at maxAllowed, so it is also the largest request VPA can hold
	// there under updateMode: Initial.
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

	if !limitRangeExists(t, cl, "cozy-monitoring") {
		t.Fatal("LimitRange withheld from a namespace holding an 8Gi request; the pod is left " +
			"with no memory.max at all, which is worse than a ceiling above the configured one")
	}
	if got := limitRangeDefaultMemory(t, cl, "cozy-monitoring"); got.Cmp(resource.MustParse("8Gi")) != 0 {
		t.Errorf("default memory = %s, want 8Gi raised to clear the request", got.String())
	}
}

// The ceiling is raised only for as long as the request is there. Nothing else lowers it,
// so if this regressed a namespace would keep an 8Gi default forever after one oversized
// pod, and the configured limit would silently stop meaning anything.
func TestReconcileSystemDefaultsLimitRangeDropsBackWhenRequestIsGone(t *testing.T) {
	scheme := limitRangeScheme(t)

	raised := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SystemDefaultsLimitRangeName,
			Namespace: "cozy-monitoring",
		},
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{{
				Type:           corev1.LimitTypeContainer,
				Default:        corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("8Gi")},
				DefaultRequest: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Mi")},
			}},
		},
	}
	// The 8Gi pod that caused the raise is gone; only a small one is left.
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(raised, systemPod("vmstorage-0", "512Mi", "")).Build()

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

	if got := limitRangeDefaultMemory(t, cl, "cozy-monitoring"); got.Cmp(resource.MustParse("4Gi")) != 0 {
		t.Errorf("default memory = %s, want the configured 4Gi back", got.String())
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
// A read the scan needs and cannot get is not evidence that applying is safe: every list
// the scan makes has to fail closed, or the guard becomes a coin toss the first time one
// kind is unreadable. Each kind gets its own case, because a single unreadable kind is the
// realistic failure — an RBAC rule that was never extended, a CRD-less API surface, one
// request that times out — and a scan that swallowed the error for that one kind would
// still look green on every other.
func TestReconcileSystemDefaultsLimitRangeSkipsWhenTheScanCannotRead(t *testing.T) {
	scheme := limitRangeScheme(t)

	unreadable := []struct {
		name string
		list client.ObjectList
		// settled says whether the namespace starts with a memory default already in
		// force. The template lists are read either way, so they get the sentinel
		// below and the case asserts it survived. The pod list is only read while no
		// default holds — a settled namespace deliberately skips it — so its case has
		// to start from an empty namespace, and asserts nothing was created at all.
		settled bool
	}{
		{name: "pods", list: &corev1.PodList{}},
		{name: "deployments", list: &appsv1.DeploymentList{}, settled: true},
		{name: "statefulsets", list: &appsv1.StatefulSetList{}, settled: true},
		{name: "daemonsets", list: &appsv1.DaemonSetList{}, settled: true},
		{name: "cronjobs", list: &batchv1.CronJobList{}, settled: true},
		{name: "jobs", list: &batchv1.JobList{}, settled: true},
	}

	// A sentinel the operator's own apply would overwrite. Asserting the LimitRange still
	// exists is not enough on its own: a scan that swallowed the read error would find no
	// offender, apply the LimitRange, and leave an object that exists just the same. The
	// sentinel is what separates "left alone" from "applied blind".
	sentinel := resource.MustParse("1Mi")

	for _, tt := range unreadable {
		t.Run(tt.name, func(t *testing.T) {
			var seed []client.Object
			if tt.settled {
				seed = append(seed, &corev1.LimitRange{
					ObjectMeta: metav1.ObjectMeta{
						Name:      SystemDefaultsLimitRangeName,
						Namespace: "cozy-monitoring",
					},
					Spec: corev1.LimitRangeSpec{
						Limits: []corev1.LimitRangeItem{{
							Type:    corev1.LimitTypeContainer,
							Default: corev1.ResourceList{corev1.ResourceMemory: sentinel},
						}},
					},
				})
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(seed...).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(_ context.Context, _ client.WithWatch, list client.ObjectList, _ ...client.ListOption) error {
						if reflect.TypeOf(list) == reflect.TypeOf(tt.list) {
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
				t.Fatalf("an unreadable %s list must not fail the reconcile: %v", tt.name, err)
			}

			got := &corev1.LimitRange{}
			err := cl.Get(t.Context(), types.NamespacedName{
				Name:      SystemDefaultsLimitRangeName,
				Namespace: "cozy-monitoring",
			}, got)

			if !tt.settled {
				if err == nil {
					t.Fatalf("LimitRange applied on the strength of a failed %s read: spec = %+v, want none created",
						tt.name, got.Spec.Limits)
				}
				if !apierrors.IsNotFound(err) {
					t.Fatalf("get LimitRange after a failed %s read: %v", tt.name, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("existing LimitRange retracted on the strength of a failed %s read: %v", tt.name, err)
			}
			if len(got.Spec.Limits) != 1 || got.Spec.Limits[0].Default.Memory().Cmp(sentinel) != 0 {
				t.Errorf("LimitRange applied on the strength of a failed %s read: spec = %+v, want the untouched sentinel %s",
					tt.name, got.Spec.Limits, sentinel.String())
			}
		})
	}
}

// The failure a pod-only scan cannot see. A workload at replicas: 0 has no pods, so
// nothing running in the namespace shows the request; defaulting the configured ceiling
// there arms the trap and it springs at the next scale-up, when the pod is rejected with
// "must be less than or equal to memory limit" and the workload cannot come back at all.
// The raised ceiling clears the request, so the scale-up is admitted with memory.max set.
func TestReconcileSystemDefaultsLimitRangeRaisesCeilingForWorkloadWithNoPods(t *testing.T) {
	scheme := limitRangeScheme(t)

	existing := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SystemDefaultsLimitRangeName,
			Namespace: "cozy-monitoring",
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(existing, systemDeployment("vmalert", 0, "8Gi", "")).Build()

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

	if got := limitRangeDefaultMemory(t, cl, "cozy-monitoring"); got.Cmp(resource.MustParse("8Gi")) != 0 {
		t.Errorf("default memory = %s, want 8Gi raised to clear the scaled-to-zero workload's request; "+
			"at 4Gi the workload could never be scaled back up", got.String())
	}
}

// scanRecordingClient builds a fake client that records which kinds a reconcile listed and
// counts the writes it made, so a test can prove which half of the scan ran rather than
// merely that it reached the same answer.
func scanRecordingClient(scheme *runtime.Scheme, listed *[]string, writes *int, objects ...client.Object) client.WithWatch {
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				*listed = append(*listed, fmt.Sprintf("%T", list))
				return c.List(ctx, list, opts...)
			},
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				*writes++
				return c.Patch(ctx, obj, patch, opts...)
			},
			Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
				*writes++
				return c.Apply(ctx, obj, opts...)
			},
		}).Build()
}

func listedKind(listed []string, kind string) bool {
	for _, l := range listed {
		if strings.Contains(l, kind) {
			return true
		}
	}
	return false
}

// The case a settled namespace exists to keep working. LimitRanger gates pods, not
// Deployments, so a workload added after the namespace settled is admitted exactly as
// written however large its request; only the pods it goes on to create are rejected. If
// the reconcile stops scanning once the LimitRange matches, nothing ever raises the ceiling
// and the rollout has no pods and no log line — a chart bump becomes a component that
// cannot start, discovered by whoever needed it.
//
// This is the reachable shape of the problem, and the one the earlier fast path skipped: an
// oversized template is the thing that can still appear in a settled namespace, where an
// oversized pod is the thing that cannot.
func TestReconcileSystemDefaultsLimitRangeRaisesCeilingForTemplateAddedAfterSettling(t *testing.T) {
	scheme := limitRangeScheme(t)

	settled := (&PackageReconciler{
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}).systemDefaultsLimitRange("cozy-monitoring", resource.MustParse("4Gi"))

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(settled, systemDeployment("vmalert", 1, "8Gi", "")).Build()

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

	if got := limitRangeDefaultMemory(t, cl, "cozy-monitoring"); got.Cmp(resource.MustParse("8Gi")) != 0 {
		t.Errorf("default memory = %s, want 8Gi raised to clear a workload added after the namespace settled at 4Gi; "+
			"at 4Gi every pod it creates is rejected and nothing rescans to notice", got.String())
	}
}

// The steady-state path, and what it is allowed to cost. This runs for every system
// namespace on every Package reconcile, and Package reconciles follow HelmRelease status
// churn, so a settled namespace must not write and must not read the one list that dwarfs
// the others.
//
// Skipping the pod list is safe exactly where a memory default is already in force: from
// then on LimitRanger fills in a limit for every container admitted without one, so a
// container with a request and no limit — the only thing the scan looks for — cannot be
// admitted. The 8Gi pod in the fixture could not exist on a real cluster beside a settled
// 4Gi LimitRange for that reason; it is here as a tripwire, so that a reconcile which did
// list pods would raise the ceiling and fail the assertion below rather than pass quietly.
func TestReconcileSystemDefaultsLimitRangeSkipsThePodListWhenTheDefaultHolds(t *testing.T) {
	scheme := limitRangeScheme(t)

	settled := (&PackageReconciler{
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}).systemDefaultsLimitRange("cozy-monitoring", resource.MustParse("4Gi"))

	var listed []string
	var writes int
	cl := scanRecordingClient(scheme, &listed, &writes, settled, systemPod("vmstorage-0", "8Gi", ""))

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

	if listedKind(listed, "PodList") {
		t.Errorf("listed pods in a namespace whose memory default already holds; no pod the scan looks for can be admitted there")
	}
	if !listedKind(listed, "DeploymentList") {
		t.Errorf("did not list deployments; templates are not gated by LimitRanger and have to be rescanned every time")
	}
	if writes != 0 {
		t.Errorf("%d writes against a LimitRange that already matches; the settled path must not re-apply", writes)
	}
	if got := limitRangeDefaultMemory(t, cl, "cozy-monitoring"); got.Cmp(resource.MustParse("4Gi")) != 0 {
		t.Errorf("default memory = %s, want the settled 4Gi left alone", got.String())
	}
}

// The comparison guarding that write has to be semantic rather than structural, or a
// LimitRange written in a different but equivalent notation would re-apply on every
// reconcile — invisible in a test that only looked at the resulting object, since every one
// of those applies produces the same object.
//
// The fixture writes the same two quantities in plain bytes: 4294967296 is 4Gi and 33554432
// is 32Mi, but they parse as DecimalSI where the configured values are BinarySI, so the
// quantities differ in both format and cached string while comparing equal. Suffixed
// notation would not do: an API round trip canonicalises 4096Mi to 4Gi, which is
// structurally identical to the configured value and would pin nothing.
func TestReconcileSystemDefaultsLimitRangeDoesNotReapplyEquivalentQuantities(t *testing.T) {
	scheme := limitRangeScheme(t)

	settled := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SystemDefaultsLimitRangeName,
			Namespace: "cozy-monitoring",
		},
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{{
				Type:           corev1.LimitTypeContainer,
				Default:        corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("4294967296")},
				DefaultRequest: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("33554432")},
			}},
		},
	}

	var listed []string
	var writes int
	cl := scanRecordingClient(scheme, &listed, &writes, settled)

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

	if writes != 0 {
		t.Errorf("%d writes against 4294967296/33554432, which is the configured 4Gi/32Mi in plain bytes; "+
			"the spec comparison must be semantic, not structural", writes)
	}
}

// A LimitRange whose spec has been stripped defaults nothing, so the namespace is back to
// admitting containers with a request and no limit and the pod list becomes meaningful
// again. Keying the skip on the object merely existing would miss that and leave the
// namespace stuck below a request it can never clear.
func TestReconcileSystemDefaultsLimitRangeScansPodsWhenTheDefaultIsStripped(t *testing.T) {
	scheme := limitRangeScheme(t)

	stripped := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SystemDefaultsLimitRangeName,
			Namespace: "cozy-monitoring",
		},
	}

	var listed []string
	var writes int
	cl := scanRecordingClient(scheme, &listed, &writes, stripped, systemPod("vmstorage-0", "8Gi", ""))

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

	if !listedKind(listed, "PodList") {
		t.Fatalf("did not list pods beside a LimitRange that defaults nothing; the skip must key on the default being in force, not on the object existing")
	}
	if got := limitRangeDefaultMemory(t, cl, "cozy-monitoring"); got.Cmp(resource.MustParse("8Gi")) != 0 {
		t.Errorf("default memory = %s, want 8Gi raised to clear the running pod", got.String())
	}
}

// The tenant boundary. It is one strings.HasPrefix, and everything downstream of it depends
// on it holding: packages/apps/tenant ships tenant-range-limits, which defaults container
// memory to 128Mi in every tenant namespace with resourceQuotas. A system LimitRange landing
// beside it would leave two LimitRanges defaulting the same containers, and which one wins
// is decided by the order LimitRanger happens to iterate them — neither chart controls that,
// so a tenant container's default memory limit would move between 128Mi and 4Gi with nothing
// in the tree changing.
//
// The system namespace in the same run is the positive control: without it the test would
// still pass if LimitRanges stopped being written anywhere at all.
func TestReconcileNamespacesLeavesTenantNamespacesAlone(t *testing.T) {
	scheme := limitRangeScheme(t)
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cozyv1alpha1 to scheme: %v", err)
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

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
		Components: []cozyv1alpha1.Component{
			{Name: "metallb", Install: &cozyv1alpha1.ComponentInstall{Namespace: "cozy-metallb"}},
			{Name: "tenant", Install: &cozyv1alpha1.ComponentInstall{Namespace: "tenant-root"}},
		},
	}

	if err := r.reconcileNamespaces(t.Context(), pkg, variant); err != nil {
		t.Fatalf("reconcileNamespaces: %v", err)
	}

	if limitRangeExists(t, cl, "tenant-root") {
		t.Error("system defaults LimitRange written into a tenant namespace; it would compete with " +
			"the tenant chart's tenant-range-limits for the same containers")
	}
	if !limitRangeExists(t, cl, "cozy-metallb") {
		t.Fatal("no LimitRange in the system namespace either, so the tenant assertion above proves nothing")
	}

	tenantNS := &corev1.Namespace{}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: "tenant-root"}, tenantNS); err != nil {
		t.Fatalf("tenant namespace was not created: %v", err)
	}
	if got, ok := tenantNS.Labels["cozystack.io/system"]; ok {
		t.Errorf("tenant namespace carries cozystack.io/system = %q; the same boundary gates both", got)
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

	// The other terminal phase. Both halves of the skip need a fixture of their own,
	// or either half can be deleted with the suite still green.
	crashed := systemPod("crashed-once", "9Gi", "")
	crashed.Status.Phase = corev1.PodFailed

	initHeavy := systemPod("with-init", "16Mi", "")
	initHeavy.Spec.InitContainers = []corev1.Container{{
		Name: "prepare",
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("6Gi")},
		},
	}}

	finishedJob := systemJob("migrated-once", "8Gi", "")
	finishedJob.Status.Conditions = []batchv1.JobCondition{{
		Type:   batchv1.JobComplete,
		Status: corev1.ConditionTrue,
	}}

	suspended := true
	suspendedCronJob := systemCronJob("suspended-backup", "7Gi", "")
	suspendedCronJob.Spec.Suspend = &suspended

	tests := []struct {
		name    string
		objects []client.Object
		want    *memoryRequestBlocker
	}{
		{
			name:    "no request above the limit",
			objects: []client.Object{systemPod("metallb-speaker", "512Mi", "")},
		},
		{
			name:    "a request equal to the limit is admissible",
			objects: []client.Object{systemPod("exactly-at", "4Gi", "")},
		},
		{
			// A finished pod is never recreated from this spec, so it cannot
			// block admission of anything — in either terminal phase.
			name:    "a terminated pod does not count",
			objects: []client.Object{terminated, crashed},
		},
		{
			// LimitRanger defaults init containers on the same terms as
			// regular ones, so the scan has to look at both.
			name:    "an init container counts",
			objects: []client.Object{initHeavy},
			want: &memoryRequestBlocker{
				workload:  "Pod/with-init",
				container: "prepare",
				request:   resource.MustParse("6Gi"),
			},
		},
		{
			// Three offenders, and the largest is deliberately neither the first
			// nor the last one the scan sees: pods are listed in name order, so
			// vmselect-0 sits in the middle. Together with asserting all three
			// reported fields, that is what pins the comparison — keeping the
			// first, the last or the smallest offender instead of the largest
			// each names a different pod and quantity here.
			name: "the largest offender is reported",
			objects: []client.Object{
				systemPod("vmagent-0", "5Gi", ""),
				systemPod("vmselect-0", "9Gi", ""),
				systemPod("vmstorage-0", "6Gi", ""),
			},
			want: &memoryRequestBlocker{
				workload:  "Pod/vmselect-0",
				container: "vmselect-0-app",
				request:   resource.MustParse("9Gi"),
			},
		},
		{
			// The gap a pod-only scan leaves: at replicas: 0 there is no pod to
			// find, so the namespace looks safe, and the request reappears at the
			// moment somebody scales the workload up and its pod is rejected.
			name:    "a scaled-to-zero deployment counts",
			objects: []client.Object{systemDeployment("vmalert", 0, "8Gi", "")},
			want: &memoryRequestBlocker{
				workload:  "Deployment/vmalert",
				container: "vmalert-app",
				request:   resource.MustParse("8Gi"),
			},
		},
		{
			name:    "a scaled-to-zero statefulset counts",
			objects: []client.Object{systemStatefulSet("vmstorage", 0, "8Gi", "")},
			want: &memoryRequestBlocker{
				workload:  "StatefulSet/vmstorage",
				container: "vmstorage-app",
				request:   resource.MustParse("8Gi"),
			},
		},
		{
			// A DaemonSet has no replica count to zero out, but it has no pods
			// either while no node matches it, and its template is still what the
			// next matching node instantiates.
			name:    "a daemonset template counts",
			objects: []client.Object{systemDaemonSet("node-exporter", "6Gi", "")},
			want: &memoryRequestBlocker{
				workload:  "DaemonSet/node-exporter",
				container: "node-exporter-app",
				request:   resource.MustParse("6Gi"),
			},
		},
		{
			// A CronJob between runs has no pods at all, and the next schedule
			// creates one from this template.
			name:    "a cronjob between runs counts",
			objects: []client.Object{systemCronJob("backup", "7Gi", "")},
			want: &memoryRequestBlocker{
				workload:  "CronJob/backup",
				container: "backup-app",
				request:   resource.MustParse("7Gi"),
			},
		},
		{
			// suspend is the same dormancy as replicas: 0 and is undone the same
			// way, so it does not earn an exemption the Deployment case does not get.
			name:    "a suspended cronjob counts",
			objects: []client.Object{suspendedCronJob},
			want: &memoryRequestBlocker{
				workload:  "CronJob/suspended-backup",
				container: "suspended-backup-app",
				request:   resource.MustParse("7Gi"),
			},
		},
		{
			// A Job created standalone — a Helm hook, a migration — has no owner
			// whose template would cover it, and one with spec.suspend has no pods
			// yet either.
			name:    "a job that has not run counts",
			objects: []client.Object{systemJob("migrate", "5Gi", "")},
			want: &memoryRequestBlocker{
				workload:  "Job/migrate",
				container: "migrate-app",
				request:   resource.MustParse("5Gi"),
			},
		},
		{
			// A completed Job never creates another pod, for the same reason a
			// terminated pod is skipped.
			name:    "a finished job does not count",
			objects: []client.Object{finishedJob},
		},
		{
			// A running workload appears twice, as a template and as pods. One
			// blocker comes back, and it names the workload: that is the object an
			// operator has to edit, and it outlives the pod names.
			name: "a workload and its own pods are reported once, as the workload",
			objects: []client.Object{
				systemDeployment("vmselect", 2, "8Gi", ""),
				systemPod("vmselect-6d4b", "8Gi", ""),
			},
			want: &memoryRequestBlocker{
				workload:  "Deployment/vmselect",
				container: "vmselect-app",
				request:   resource.MustParse("8Gi"),
			},
		},
		{
			// The mirror case, and why live pods are still scanned. Not because
			// a webhook raised the request after the template was rendered:
			// LimitRanger defaults the limit before any webhook or policy plugin
			// runs, so a pod one of them touched arrives here already carrying a
			// limit and is skipped. What is left is a pod admitted before the
			// LimitRange existed, or one its controller built from a custom
			// resource no template in this scan can show — either can ask for
			// more than any template in the namespace admits to.
			name: "a request raised on the live pod outranks its template",
			objects: []client.Object{
				systemDeployment("vmselect", 2, "5Gi", ""),
				systemPod("vmselect-6d4b", "9Gi", ""),
			},
			want: &memoryRequestBlocker{
				workload:  "Pod/vmselect-6d4b",
				container: "vmselect-6d4b-app",
				request:   resource.MustParse("9Gi"),
			},
		},
		{
			// Every kind the scan reads, all of them under the limit: the
			// LimitRange has to stay applicable in the ordinary case.
			name: "workloads below the limit do not block",
			objects: []client.Object{
				systemDeployment("grafana", 1, "512Mi", ""),
				systemStatefulSet("vlogs", 1, "1Gi", ""),
				systemDaemonSet("node-exporter", "64Mi", ""),
				systemCronJob("backup", "256Mi", ""),
				systemJob("migrate", "128Mi", ""),
				systemPod("grafana-6d4b", "512Mi", ""),
			},
		},
		{
			// LimitRanger never defaults a container that declares its own memory
			// limit, in a template exactly as in a pod.
			name:    "a template request that carries its own limit does not count",
			objects: []client.Object{systemDeployment("vmstorage", 1, "8Gi", "8Gi")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.objects...).Build()
			r := &PackageReconciler{
				Client:                     cl,
				APIReader:                  cl,
				Scheme:                     scheme,
				SystemNamespaceMemoryLimit: resource.MustParse("4Gi"),
			}

			got, err := r.findRequestAboveDefaultLimit(t.Context(), "cozy-monitoring", true)
			if err != nil {
				t.Fatalf("findRequestAboveDefaultLimit: %v", err)
			}

			if tt.want == nil {
				if got != nil {
					t.Fatalf("reported %s container %s (%s) as blocking; nothing should block here",
						got.workload, got.container, got.request.String())
				}
				return
			}
			if got == nil {
				t.Fatal("no blocker reported; a container that cannot be admitted under the default was missed")
			}
			if got.workload != tt.want.workload || got.container != tt.want.container ||
				got.request.Cmp(tt.want.request) != 0 {
				t.Errorf("blocker = %s container %s (%s), want %s container %s (%s)",
					got.workload, got.container, got.request.String(),
					tt.want.workload, tt.want.container, tt.want.request.String())
			}
		})
	}
}
