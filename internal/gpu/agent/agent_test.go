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

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	podresourcesv1 "k8s.io/kubelet/pkg/apis/podresources/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
)

const ownNode = "gpu-node-1"

type writeLog struct {
	mu    sync.Mutex
	names []string
}

func (w *writeLog) add(t *testing.T, verb string, obj client.Object) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := obj.(*gpuv1alpha1.GPUNode); !ok {
		t.Errorf("%s of %T: the agent writes only GPUNode", verb, obj)
	}
	w.names = append(w.names, verb+" "+obj.GetName())
}

// newAgent wires an Agent to a fake kubelet serving the A10 in IOMMU group
// 42 to the pod tenant-a/virt-launcher-a, and to a fake API server holding
// objs and that pod in phase, or without the pod if phase is empty.
func newAgent(t *testing.T, phase corev1.PodPhase, objs ...client.Object) (*Agent, client.Client, *writeLog) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := gpuv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	log := &writeLog{}
	forbidden := func(verb string) error {
		t.Errorf("the agent must never %s", verb)
		return errors.New("forbidden in test")
	}
	objs = append(objs, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: ownNode, UID: "node-uid-1"}})
	if phase != "" {
		objs = append(objs, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: "virt-launcher-a"},
			Spec:       corev1.PodSpec{NodeName: ownNode},
			Status:     corev1.PodStatus{Phase: phase},
		})
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&gpuv1alpha1.GPUNode{}).
		WithIndex(&corev1.Pod{}, podNodeField, func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).
		WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				log.add(t, "create", obj)
				return c.Create(ctx, obj, opts...)
			},
			Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
				return forbidden("update an object")
			},
			Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
				return forbidden("patch an object outside its status")
			},
			Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
				return forbidden("delete")
			},
			SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
				return forbidden("update a status")
			},
			SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
				if sub != "status" {
					return forbidden("patch subresource " + sub)
				}
				if p.Type() != types.MergePatchType {
					t.Errorf("status patch type %s, want a merge patch", p.Type())
				}
				log.add(t, "patch-status", obj)
				return c.SubResource(sub).Patch(ctx, obj, p, opts...)
			},
		}).
		Build()

	kubelet := serveKubelet(t, &fakeKubelet{
		alloc: &podresourcesv1.AllocatableResourcesResponse{Devices: []*podresourcesv1.ContainerDevices{
			devs("nvidia.com/A10", "42"),
		}},
		list: &podresourcesv1.ListPodResourcesResponse{PodResources: []*podresourcesv1.PodResources{
			podUsing("tenant-a", "virt-launcher-a", devs("nvidia.com/A10", "42")),
		}},
	})
	return &Agent{
		NodeName:     ownNode,
		Sysfs:        writeSysfs(t),
		PodResources: kubelet,
		Client:       c,
	}, c, log
}

func getGPUNode(t *testing.T, c client.Client, name string) *gpuv1alpha1.GPUNode {
	t.Helper()
	gn := &gpuv1alpha1.GPUNode{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: name}, gn); err != nil {
		t.Fatal(err)
	}
	return gn
}

func TestSyncCreatesGPUNodeOwnedByNode(t *testing.T) {
	a, c, _ := newAgent(t, corev1.PodRunning)
	if err := a.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	gn := getGPUNode(t, c, ownNode)

	wantOwner := []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: ownNode, UID: "node-uid-1"}}
	if !reflect.DeepEqual(gn.OwnerReferences, wantOwner) {
		t.Errorf("ownerReferences = %+v, want %+v", gn.OwnerReferences, wantOwner)
	}
	d := deviceAt(t, gn.Status, "0000:a1:00.0")
	if d.ResourceName != "nvidia.com/A10" || !ptr.Deref(d.Healthy, false) ||
		d.UsedBy == nil || *d.UsedBy != (gpuv1alpha1.GPUPodReference{Namespace: "tenant-a", Pod: "virt-launcher-a"}) {
		t.Errorf("device = %s", dump(d))
	}
	if !reflect.DeepEqual(gn.Status.ResourceNames, []string{"nvidia.com/A10"}) {
		t.Errorf("resourceNames = %v", gn.Status.ResourceNames)
	}
}

// The binder writes conditions into the same status; a whole-status write
// by the agent would drop them.
func TestSyncKeepsBinderConditions(t *testing.T) {
	cond := metav1.Condition{
		Type: gpu.ConditionLostCapacity, Status: metav1.ConditionTrue, Reason: "DeviceMissing",
		Message: "set by the binder", LastTransitionTime: metav1.Now(),
	}
	existing := &gpuv1alpha1.GPUNode{
		ObjectMeta: metav1.ObjectMeta{Name: ownNode},
		Status: gpuv1alpha1.GPUNodeStatus{
			Devices:       []gpuv1alpha1.GPUNodeDevice{{PCIAddress: "0000:ff:00.0", Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: "nvidia.com/OLD"}},
			ResourceNames: []string{"nvidia.com/OLD"},
			Conditions:    []metav1.Condition{cond},
		},
	}
	a, c, log := newAgent(t, corev1.PodRunning, existing)
	if err := a.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	gn := getGPUNode(t, c, ownNode)
	if got := apimeta.FindStatusCondition(gn.Status.Conditions, gpu.ConditionLostCapacity); got == nil || got.Message != cond.Message {
		t.Errorf("binder condition lost: %+v", gn.Status.Conditions)
	}
	if !reflect.DeepEqual(gn.Status.ResourceNames, []string{"nvidia.com/A10"}) {
		t.Errorf("resourceNames = %v, want the agent's", gn.Status.ResourceNames)
	}
	for _, d := range gn.Status.Devices {
		if d.PCIAddress == "0000:ff:00.0" {
			t.Errorf("stale device kept: %+v", d)
		}
	}
	if !reflect.DeepEqual(log.names, []string{"patch-status " + ownNode}) {
		t.Errorf("writes = %v, want one status patch", log.names)
	}
}

func TestSyncWritesOnlyOwnNode(t *testing.T) {
	other := &gpuv1alpha1.GPUNode{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-node-2"},
		Status:     gpuv1alpha1.GPUNodeStatus{ResourceNames: []string{"nvidia.com/OTHER"}},
	}
	a, c, log := newAgent(t, corev1.PodRunning, other)
	for range 2 {
		if err := a.Sync(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"patch-status " + ownNode, "create " + ownNode, "patch-status " + ownNode, "patch-status " + ownNode}
	if !reflect.DeepEqual(log.names, want) {
		t.Errorf("writes = %v, want %v", log.names, want)
	}
	if got := getGPUNode(t, c, "gpu-node-2"); !reflect.DeepEqual(got.Status, other.Status) {
		t.Errorf("other node's GPUNode changed: %s", dump(got.Status))
	}
}

func TestSyncCreatesNothingWithoutGPUs(t *testing.T) {
	a, c, log := newAgent(t, corev1.PodRunning)
	a.Sysfs = emptySysfs(t)
	if err := a.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(log.names, []string{"patch-status " + ownNode}) {
		t.Errorf("writes = %v, want only the status patch that found nothing", log.names)
	}
	var list gpuv1alpha1.GPUNodeList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Errorf("GPUNodes = %s, want none", dump(list.Items))
	}
}

// GPUs that vanish from the node are cleared from an existing GPUNode, so
// the binder sees them missing.
func TestSyncClearsVanishedGPUs(t *testing.T) {
	existing := &gpuv1alpha1.GPUNode{
		ObjectMeta: metav1.ObjectMeta{Name: ownNode},
		Status: gpuv1alpha1.GPUNodeStatus{
			Devices:       []gpuv1alpha1.GPUNodeDevice{{PCIAddress: "0000:a1:00.0", Function: gpuv1alpha1.PCIFunctionWhole}},
			ResourceNames: []string{"nvidia.com/A10"},
		},
	}
	a, c, _ := newAgent(t, corev1.PodRunning, existing)
	a.Sysfs = emptySysfs(t)
	if err := a.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	gn := getGPUNode(t, c, ownNode)
	if len(gn.Status.Devices) != 0 || len(gn.Status.ResourceNames) != 0 {
		t.Errorf("status = %s, want empty", dump(gn.Status))
	}
}

// kubelet still lists the pod, but the API server has it finished or gone.
func TestSyncSkipsFinishedPods(t *testing.T) {
	for _, phase := range []corev1.PodPhase{corev1.PodSucceeded, corev1.PodFailed, ""} {
		t.Run(string(phase), func(t *testing.T) {
			a, c, _ := newAgent(t, phase)
			if err := a.Sync(context.Background()); err != nil {
				t.Fatal(err)
			}
			if d := deviceAt(t, getGPUNode(t, c, ownNode).Status, "0000:a1:00.0"); d.UsedBy != nil {
				t.Errorf("usedBy = %+v, want none", d.UsedBy)
			}
		})
	}
}

func emptySysfs(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bus/pci/devices"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// A sysfs that is not mounted must fail the sync rather than report a node
// whose GPUs are all gone.
func TestSyncFailsWithoutSysfs(t *testing.T) {
	a, _, log := newAgent(t, corev1.PodRunning)
	a.Sysfs = t.TempDir()
	if err := a.Sync(context.Background()); err == nil {
		t.Error("Sync succeeded without sysfs")
	}
	if len(log.names) != 0 {
		t.Errorf("writes = %v, want none", log.names)
	}
}

func TestReadyAfterFirstSync(t *testing.T) {
	a, _, _ := newAgent(t, corev1.PodRunning)
	a.Interval = time.Hour
	if a.Ready(nil) == nil {
		t.Fatal("ready before any sync")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- a.Start(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for a.Ready(nil) != nil {
		if time.Now().After(deadline) {
			t.Fatal("not ready after the first sync")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Start = %v", err)
	}
}

func TestNotReadyWhileSyncsFail(t *testing.T) {
	a, _, _ := newAgent(t, corev1.PodRunning)
	a.Sysfs = t.TempDir()
	a.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if a.Ready(nil) == nil {
		t.Error("ready although no sync succeeded")
	}
}
