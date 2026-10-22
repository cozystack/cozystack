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

package main

import (
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/controller"
	"github.com/cozystack/cozystack/internal/controller/cacert"
	"github.com/cozystack/cozystack/internal/controller/tenantgateway"
	"github.com/cozystack/cozystack/internal/controller/tenantlogrouting"
	"github.com/cozystack/cozystack/internal/controller/tenantquota"
	"github.com/cozystack/cozystack/internal/controller/wildcardsecret"
	"github.com/cozystack/cozystack/internal/gpu"
	gpucontroller "github.com/cozystack/cozystack/internal/gpu/controller"
	gpuwebhook "github.com/cozystack/cozystack/internal/gpu/webhook"
	"github.com/cozystack/cozystack/internal/telemetry"
)

// step is one piece of the manager's wiring. gpuOnly marks the steps that
// also run under --gpu-only.
type step struct {
	name    string
	gpuOnly bool
	run     func() error
}

type setupConfig struct {
	restConfig               *rest.Config
	gpu                      gpu.Config
	quotaBufferPercent       int64
	seaweedfsMetricsEndpoint string
	telemetry                telemetry.Config
}

// defaultLeaderElectionID is the Lease every cozystack-controller has
// elected on; changing it would let an upgraded replica lead beside an old one.
const defaultLeaderElectionID = "19a0338c.cozystack.io"

// validateGPUOnly refuses --gpu-only without --enable-gpu, where the binder
// alone serves no purpose, and a GPU-only instance electing on the default
// Lease: in the main controller's namespace it would take the Lease and
// stop every reconciler of the main controller while it holds it.
func validateGPUOnly(gpuOnly, gpuEnabled, leaderElect bool, leaderElectionID string) error {
	if !gpuOnly {
		return nil
	}
	if !gpuEnabled {
		return errors.New("--gpu-only needs --enable-gpu")
	}
	if leaderElect && leaderElectionID == defaultLeaderElectionID {
		return errors.New("--gpu-only with --leader-elect needs a --leader-election-id of its own")
	}
	return nil
}

// selectSteps returns every step, or with gpuOnly only the GPU ones. A step
// left out is never run, so its reconciler, informers and caches do not
// exist: an instance beside a full controller runs no second copy of them.
func selectSteps(steps []step, gpuOnly bool) []step {
	if !gpuOnly {
		return steps
	}
	var out []step
	for _, s := range steps {
		if s.gpuOnly {
			out = append(out, s)
		}
	}
	return out
}

// managerSteps lists the manager's wiring in order. Building the list must
// not touch mgr; only running a step may.
func managerSteps(mgr ctrl.Manager, c setupConfig) []step {
	return []step{
		{name: "WorkloadMonitor", run: func() error {
			return (&controller.WorkloadMonitorReconciler{
				Client:                   mgr.GetClient(),
				Scheme:                   mgr.GetScheme(),
				Recorder:                 mgr.GetEventRecorderFor("workloadmonitor-controller"),
				SeaweedfsMetricsEndpoint: c.seaweedfsMetricsEndpoint,
			}).SetupWithManager(mgr)
		}},
		{name: "WorkloadReconciler", run: func() error {
			return (&controller.WorkloadReconciler{
				Client: mgr.GetClient(),
				Scheme: mgr.GetScheme(),
			}).SetupWithManager(mgr)
		}},
		{name: "ApplicationDefinitionReconciler", run: func() error {
			return (&controller.ApplicationDefinitionReconciler{
				Client: mgr.GetClient(),
				Scheme: mgr.GetScheme(),
			}).SetupWithManager(mgr)
		}},
		{name: "ApplicationDefinitionHelmReconciler", run: func() error {
			return (&controller.ApplicationDefinitionHelmReconciler{
				Client:   mgr.GetClient(),
				Scheme:   mgr.GetScheme(),
				Recorder: mgr.GetEventRecorderFor("applicationdefinition-helm-reconciler"),
			}).SetupWithManager(mgr)
		}},
		{name: "TenantGateway", run: func() error {
			return (&tenantgateway.Reconciler{
				Client: mgr.GetClient(),
				Scheme: mgr.GetScheme(),
			}).SetupWithManager(mgr)
		}},
		{name: "TenantQuota", run: func() error {
			return (&tenantquota.Reconciler{
				Client:        mgr.GetClient(),
				Scheme:        mgr.GetScheme(),
				Recorder:      mgr.GetEventRecorderFor("tenantquota-controller"),
				BufferPercent: c.quotaBufferPercent,
			}).SetupWithManager(mgr)
		}},
		{name: "TenantLogRouting", run: func() error {
			return (&tenantlogrouting.Reconciler{
				Client: mgr.GetClient(),
			}).SetupWithManager(mgr)
		}},
		{name: "WildcardSecret", run: func() error {
			return (&wildcardsecret.Reconciler{
				Client:   mgr.GetClient(),
				Reader:   mgr.GetAPIReader(),
				Scheme:   mgr.GetScheme(),
				Recorder: mgr.GetEventRecorderFor("wildcardsecret"),
			}).SetupWithManager(mgr)
		}},
		{name: "CACert", run: func() error {
			// The CA-extraction reconciler watches its source Secrets as metadata only, so
			// no private key ever enters a cache. It cannot watch them on the manager's
			// cache: that cache's Secret informer is label-scoped to WildcardSecret's
			// replicas (the manager's Cache options in main.go), a metadata informer for the
			// same v1/Secret GVK routes to that same scoped informer and inherits the
			// selector, and the watch would then silently never fire for an unlabelled CA
			// source — the projection would appear only on the slow resync. So it owns a
			// SEPARATE cache, metadata-only and unscoped, holding Secret metadata stubs
			// cluster-wide (no key material). It is a cluster.Cluster so mgr.Add registers
			// it in the manager's cache-runnable group — started, waited-for-sync before the
			// controllers run, and stopped on shutdown, exactly like the shared cache.
			caSecretCluster, err := cluster.New(c.restConfig, func(o *cluster.Options) {
				o.Scheme = scheme
			})
			if err != nil {
				return fmt.Errorf("build the CA source metadata cache: %w", err)
			}
			if err := mgr.Add(caSecretCluster); err != nil {
				return fmt.Errorf("add the CA source metadata cache to the manager: %w", err)
			}
			return (&cacert.Reconciler{
				Client:   mgr.GetClient(),
				Reader:   mgr.GetAPIReader(),
				Recorder: mgr.GetEventRecorderFor("cacert-controller"),
			}).SetupWithManager(mgr, caSecretCluster.GetCache())
		}},
		{name: "gpu-binder", gpuOnly: true, run: func() error {
			return gpucontroller.Setup(mgr, c.gpu)
		}},
		// Asking the manager for its webhook server starts it, and the server
		// needs a serving certificate, so registration waits for --enable-gpu.
		{name: "gpu-webhooks", gpuOnly: true, run: func() error {
			if !c.gpu.Enabled {
				return nil
			}
			return gpuwebhook.Register(mgr, c.gpu)
		}},
		{name: "healthz", gpuOnly: true, run: func() error {
			return mgr.AddHealthzCheck("healthz", healthz.Ping)
		}},
		{name: "readyz", gpuOnly: true, run: func() error {
			return addReadyz(mgr, c.gpu)
		}},
		{name: "telemetry", run: func() error {
			collector, err := telemetry.NewCollector(mgr.GetClient(), &c.telemetry, mgr.GetConfig())
			if err != nil {
				setupLog.V(1).Error(err, "unable to create telemetry collector, telemetry will be disabled")
				return nil
			}
			if collector != nil {
				if err := mgr.Add(collector); err != nil {
					setupLog.Error(err, "unable to set up telemetry collector")
					setupLog.V(1).Error(err, "unable to set up telemetry collector, continuing without telemetry")
				}
			}
			return nil
		}},
	}
}

// addReadyz: with GPU enabled, a replica is ready only once the GPU, Node,
// ConfigMap and Namespace informers have synced. Every replica starts
// them, because one that is not the leader starts no controller and GPU
// admission has to answer from a warm cache rather than LIST inside its
// deadline. Readiness does not wait for KubeVirt, whose kinds a fresh
// install can serve only after this controller starts: packages that
// depend on cozystack-controller wait for its readiness. The VMI and
// KubeVirt informers start once their kinds are served.
func addReadyz(mgr ctrl.Manager, cfg gpu.Config) error {
	readyz := healthz.Ping
	if cfg.Enabled {
		vmiGVK := schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachineInstance"}
		kvGVK := vmiGVK.GroupVersion().WithKind("KubeVirt")
		vmi := &unstructured.Unstructured{}
		vmi.SetGroupVersionKind(vmiGVK)
		kv := &unstructured.Unstructured{}
		kv.SetGroupVersionKind(kvGVK)
		warmup := &gpu.Warmup{
			Mapper:    mgr.GetRESTMapper(),
			Informers: mgr.GetCache(),
			Objects: []client.Object{&gpuv1alpha1.GPUDevice{}, &gpuv1alpha1.GPUClass{}, &gpuv1alpha1.GPUNode{},
				&corev1.Node{}, &corev1.ConfigMap{}, &corev1.Namespace{}},
			Kinds:           []schema.GroupVersionKind{vmiGVK, kvGVK},
			KubeVirtObjects: []client.Object{vmi, kv},
			Interval:        5 * time.Second,
			OnWait: func(k schema.GroupVersionKind, err error) {
				setupLog.Info("waiting for a KubeVirt kind the GPU webhooks read", "kind", k.String(), "reason", err.Error())
			},
		}
		if err := mgr.Add(warmup); err != nil {
			return fmt.Errorf("add the GPU informer warm-up: %w", err)
		}
		readyz = warmup.Ready
	}
	return mgr.AddReadyzCheck("readyz", readyz)
}
