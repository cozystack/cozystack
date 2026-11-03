//go:build envtest

/*
Copyright 2026 The Cozystack Authors.

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

package controller

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/controller/wildcardsecret"
	"github.com/cozystack/cozystack/internal/gpu"
)

// TestEnvtest_Setup drives both reconcilers through the watches Setup
// registers, so a missing or mis-scoped watch shows up as a copy or a
// status that never converges.
func TestEnvtest_Setup(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("set KUBEBUILDER_ASSETS, as make test-controllers-envtest does")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:        []string{"../../../packages/system/cozystack-controller/definitions"},
		ErrorIfCRDPathMissing:    true,
		ControlPlaneStartTimeout: 2 * time.Minute,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	s := testScheme(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// start runs the GPU controllers as cozystack-controller does and
	// returns a stop that waits for the manager to exit.
	start := func(enabled bool) (stop func()) {
		// The Secret cache is scoped as in cozystack-controller, so a copier
		// reading Secrets through the manager's cache would miss them here.
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: s, Metrics: metricsserver.Options{BindAddress: "0"},
			Controller: config.Controller{SkipNameValidation: ptr.To(true)},
			Cache:      cache.Options{ByObject: map[client.Object]cache.ByObject{&corev1.Secret{}: wildcardsecret.SecretCacheByObject()}}})
		if err != nil {
			t.Fatal(err)
		}
		secretMeta, err := cluster.New(cfg, func(o *cluster.Options) { o.Scheme = s })
		if err != nil {
			t.Fatal(err)
		}
		if err := mgr.Add(secretMeta); err != nil {
			t.Fatal(err)
		}
		if err := Setup(mgr, enabled, secretMeta.GetCache()); err != nil {
			t.Fatal(err)
		}
		mctx, mcancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { _ = mgr.Start(mctx); close(done) }()
		return func() { mcancel(); <-done }
	}

	cl, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	eventually := func(what string, cond func() error) {
		t.Helper()
		var last error
		if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 30*time.Second, true, func(context.Context) (bool, error) {
			last = cond()
			return last == nil, nil
		}); err != nil {
			t.Fatalf("%s: %v", what, last)
		}
	}
	copyData := func(ns string) (map[string]string, error) {
		sec := &corev1.Secret{}
		if err := cl.Get(ctx, client.ObjectKey{Namespace: ns, Name: copyName}, sec); err != nil {
			return nil, err
		}
		return dataOf(sec), nil
	}
	wantToken := func(ns, token string) func() error {
		return func() error {
			d, err := copyData(ns)
			if err != nil {
				return err
			}
			if d[gpu.LicenseTokenKey] != token {
				return fmt.Errorf("token %q, want %q", d[gpu.LicenseTokenKey], token)
			}
			return nil
		}
	}
	gone := func(ns string) func() error {
		return func() error {
			if _, err := copyData(ns); err == nil {
				return fmt.Errorf("copy in %s still exists", ns)
			}
			return nil
		}
	}
	condition := func(typ string, status metav1.ConditionStatus) func() error {
		return func() error {
			c := &cozyv1alpha1.GPUClass{}
			if err := cl.Get(ctx, client.ObjectKey{Name: "l40s"}, c); err != nil {
				return err
			}
			if got := meta.FindStatusCondition(c.Status.Conditions, typ); got == nil || got.Status != status {
				return fmt.Errorf("%s = %+v, want %s", typ, got, status)
			}
			return nil
		}
	}

	must(cl.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: gpu.LicenseSecretNamespace}}))
	tenant := namespace("tenant-a", "true")
	must(cl.Create(ctx, tenant))
	class := gpuClass("l40s", rnL40S, 0, licensed(tokenSecret, 4))
	class.CreationTimestamp = metav1.Time{}
	must(cl.Create(ctx, class))
	must(cl.Create(ctx, licenseCopy("tenant-a", "l40s", map[string]string{gpu.LicenseTokenKey: "token-0"})))

	stop := start(false)
	eventually("copy deleted while GPU features are off", gone("tenant-a"))
	// A node event after the copier has settled would start a pass of a
	// status controller run by mistake, which writes free; the copier
	// skips status updates and would not clear it again.
	must(cl.Create(ctx, gpuNode("cpu-0", nil)))
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if c := (&cozyv1alpha1.GPUClass{}); cl.Get(ctx, client.ObjectKey{Name: "l40s"}, c) != nil || c.Status.Free != nil {
			t.Fatalf("status computed while GPU features are off: %+v", c.Status)
		}
	}
	stop()

	t.Cleanup(start(true))
	eventually("LicenseSecretMissing while the hoster Secret is absent", condition(cozyv1alpha1.GPUClassLicenseSecretMissing, metav1.ConditionTrue))
	eventually("ResourceNameConflict written next to it", condition(cozyv1alpha1.GPUClassResourceNameConflict, metav1.ConditionFalse))

	src := hosterSecret(map[string]string{gpu.LicenseTokenKey: "token-1"})
	must(cl.Create(ctx, src))
	eventually("copy after the hoster Secret appears", wantToken("tenant-a", "token-1"))
	eventually("LicenseSecretMissing cleared", condition(cozyv1alpha1.GPUClassLicenseSecretMissing, metav1.ConditionFalse))

	src.Data[gpu.LicenseTokenKey] = []byte("token-2")
	must(cl.Update(ctx, src))
	eventually("copy refreshed after rotation", wantToken("tenant-a", "token-2"))

	must(cl.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: copyName}}))
	eventually("deleted copy written again", wantToken("tenant-a", "token-2"))

	tenant.Labels[gpu.LabelNamespaceVGPU] = "false"
	must(cl.Update(ctx, tenant))
	eventually("copy deleted with the entitlement", gone("tenant-a"))
	tenant.Labels[gpu.LabelNamespaceVGPU] = "true"
	must(cl.Update(ctx, tenant))
	eventually("copy back with the entitlement", wantToken("tenant-a", "token-2"))

	node := gpuNode("gpu-1", nil)
	allocatable := gpuNode("gpu-1", map[string]int64{rnL40S: 3}).Status
	must(cl.Create(ctx, node))
	node.Status = allocatable
	must(cl.Status().Update(ctx, node))
	free := func(want int32) func() error {
		return func() error {
			c := &cozyv1alpha1.GPUClass{}
			if err := cl.Get(ctx, client.ObjectKey{Name: "l40s"}, c); err != nil {
				return err
			}
			if c.Status.Free == nil || *c.Status.Free != want {
				return fmt.Errorf("free = %v, want %d", c.Status.Free, want)
			}
			return nil
		}
	}
	eventually("free after the node advertises units", free(3))
	must(cl.Create(ctx, gpuPod("vm-1", "gpu-1", rnL40S, 1, func(p *corev1.Pod) {
		p.Namespace = "tenant-a"
		p.Spec.Containers[0].Image = "virt-launcher"
	})))
	eventually("free after a pod takes a unit", free(2))

	must(cl.Delete(ctx, class))
	eventually("copy deleted with the class", gone("tenant-a"))
}
