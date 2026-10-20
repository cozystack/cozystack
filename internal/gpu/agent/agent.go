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

// Package agent is the GPU node agent: it reports the GPU functions of its
// own node, and what kubelet's device plugins make of them, in the GPUNode
// of the same name.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	podresourcesv1 "k8s.io/kubelet/pkg/apis/podresources/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
)

const (
	// PodResourcesSocket is kubelet's pod-resources endpoint under its
	// default root directory, which Talos and k3s both keep.
	PodResourcesSocket = "/var/lib/kubelet/pod-resources/kubelet.sock"

	podNodeField = "spec.nodeName"
	// kubelet's own pod-resources client allows responses this large.
	maxResponseSize = 16 << 20
)

// Dial returns a client of kubelet's pod-resources socket. It connects on
// the first call, so an unreachable kubelet shows up as a failed sync.
func Dial(socket string) (podresourcesv1.PodResourcesListerClient, io.Closer, error) {
	conn, err := grpc.NewClient("unix://"+socket,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxResponseSize)))
	if err != nil {
		return nil, nil, fmt.Errorf("dialling %s: %w", socket, err)
	}
	return podresourcesv1.NewPodResourcesListerClient(conn), conn, nil
}

// Agent writes GPUNode/<NodeName> and nothing else. It creates the object,
// owned by its Node, and afterwards only merge-patches the status fields it
// owns, devices and resourceNames, because the binder writes conditions
// into the same status.
type Agent struct {
	NodeName     string
	Sysfs        string
	PodResources podresourcesv1.PodResourcesListerClient
	// Client must not be cached: the agent reads pods of its node only.
	Client client.Client
	// Interval between syncs. The pod-resources API has no watch, and
	// kubelet limits it to 100 requests per second; a sync makes two.
	Interval time.Duration

	synced atomic.Bool
}

// Start syncs every Interval until ctx ends. A failed sync is logged and
// retried on the next tick.
func (a *Agent) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithValues("node", a.NodeName)
	t := time.NewTicker(a.Interval)
	defer t.Stop()
	for {
		syncCtx, cancel := context.WithTimeout(ctx, a.Interval)
		err := a.Sync(syncCtx)
		cancel()
		if err != nil {
			logger.Error(err, "GPU inventory sync failed")
		} else {
			a.synced.Store(true)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Ready fails until the first sync has succeeded.
func (a *Agent) Ready(*http.Request) error {
	if !a.synced.Load() {
		return errors.New("no GPU inventory sync has succeeded yet")
	}
	return nil
}

// Sync reads the inventory once and writes it.
func (a *Agent) Sync(ctx context.Context) error {
	alloc, err := a.PodResources.GetAllocatableResources(ctx, &podresourcesv1.AllocatableResourcesRequest{})
	if err != nil {
		return fmt.Errorf("reading allocatable devices: %w", err)
	}
	used, err := a.PodResources.List(ctx, &podresourcesv1.ListPodResourcesRequest{})
	if err != nil {
		return fmt.Errorf("listing pod resources: %w", err)
	}
	var pods corev1.PodList
	if err := a.Client.List(ctx, &pods, client.MatchingFields{podNodeField: a.NodeName}); err != nil {
		return fmt.Errorf("listing pods of the node: %w", err)
	}
	phases := make(map[types.NamespacedName]corev1.PodPhase, len(pods.Items))
	for _, p := range pods.Items {
		phases[types.NamespacedName{Namespace: p.Namespace, Name: p.Name}] = p.Status.Phase
	}
	live := func(namespace, name string) bool {
		phase, ok := phases[types.NamespacedName{Namespace: namespace, Name: name}]
		return ok && podLive(phase)
	}

	st, err := Inventory(a.Sysfs, alloc, used.GetPodResources(), live)
	if err != nil {
		return err
	}
	return a.write(ctx, st)
}

func (a *Agent) write(ctx context.Context, st gpuv1alpha1.GPUNodeStatus) error {
	// A JSON merge patch replaces each list whole and leaves conditions
	// alone. A nil list is sent as null, which clears the field.
	patch, err := json.Marshal(map[string]any{"status": map[string]any{
		"devices":       st.Devices,
		"resourceNames": st.ResourceNames,
	}})
	if err != nil {
		return err
	}
	gn := &gpuv1alpha1.GPUNode{ObjectMeta: metav1.ObjectMeta{Name: a.NodeName}}
	err = a.Client.Status().Patch(ctx, gn, client.RawPatch(types.MergePatchType, patch))
	if !apierrors.IsNotFound(err) {
		return wrap(err, "patching GPUNode status")
	}
	if len(st.Devices) == 0 {
		return nil
	}

	node := &corev1.Node{}
	if err := a.Client.Get(ctx, client.ObjectKey{Name: a.NodeName}, node); err != nil {
		return fmt.Errorf("reading own Node: %w", err)
	}
	gn = &gpuv1alpha1.GPUNode{ObjectMeta: metav1.ObjectMeta{
		Name: a.NodeName,
		// No blockOwnerDeletion: setting it needs update on nodes/finalizers.
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: node.Name, UID: node.UID}},
	}}
	if err := a.Client.Create(ctx, gn); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating GPUNode: %w", err)
	}
	return wrap(a.Client.Status().Patch(ctx, gn, client.RawPatch(types.MergePatchType, patch)), "patching GPUNode status")
}

func wrap(err error, msg string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", msg, err)
}
