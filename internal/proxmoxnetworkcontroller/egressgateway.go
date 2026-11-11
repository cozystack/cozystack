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

package proxmoxnetworkcontroller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	// vegLabel is the label Kube-OVN puts on the pods of a VpcEgressGateway,
	// valued with the gateway's name.
	vegLabel = "ovn.kubernetes.io/vpc-egress-gateway"
	// portSecurityAnnotation is the primary interface's port security.
	portSecurityAnnotation = "ovn.kubernetes.io/port_security"
	// vpcIDLabel marks objects the vpc chart renders for a VPC.
	vpcIDLabel = "cozystack.io/vpcId"
)

// VEGPodSelector selects the pods this reconciler may look at; the manager
// caches only those.
func VEGPodSelector() labels.Selector {
	req, _ := labels.NewRequirement(vegLabel, selection.Exists, nil)
	return labels.NewSelector().Add(*req)
}

// +kubebuilder:object:generate=false

// EgressGatewayPodReconciler turns port security off on the pods of the VPC
// egress gateways the vpc chart renders.
//
// Cozystack's kube-ovn webhook turns port security on for every pod created in
// a tenant namespace, which pins a pod's port to its own MAC and IP. An egress
// gateway forwards: it receives VM packets addressed to the Internet and sends
// replies whose source is the Internet, so with port security on, OVN drops
// both directions. The webhook acts on CREATE only, and Kube-OVN re-applies
// port security when the annotation changes, so the annotation is flipped
// after the pod exists.
//
// Only pods whose whole ownership chain is the egress gateway's own
// Deployment, created by Kube-OVN for a VpcEgressGateway the vpc chart
// rendered, are touched. Tenants cannot create pods, ReplicaSets, Deployments
// or VpcEgressGateways, so a label alone, which a tenant could put on a
// virt-launcher pod through a VirtualMachine, is never enough.
type EgressGatewayPodReconciler struct {
	client.Client
	// Reader resolves the ownership chain past the cache, so the manager does
	// not keep informers on every ReplicaSet and Deployment in the cluster for
	// a lookup that happens once per gateway pod.
	Reader client.Reader
}

// SetupWithManager registers the reconciler.
func (r *EgressGatewayPodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	sel, err := predicate.LabelSelectorPredicate(metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{Key: vegLabel, Operator: metav1.LabelSelectorOpExists}},
	})
	if err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}, builder.WithPredicates(sel)).
		Named("egressgatewaypod").
		Complete(r)
}

// Reconcile flips port security off on one verified gateway pod.
func (r *EgressGatewayPodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pod := &corev1.Pod{}
	if err := r.Get(ctx, req.NamespacedName, pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !pod.DeletionTimestamp.IsZero() || pod.Annotations[portSecurityAnnotation] == "false" {
		return ctrl.Result{}, nil
	}
	ok, why, err := r.ownedByChartGateway(ctx, pod)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ok {
		log.FromContext(ctx).V(1).Info("not an egress gateway pod of a cozystack VPC", "pod", req.NamespacedName, "reason", why)
		return ctrl.Result{}, nil
	}
	patch := client.MergeFrom(pod.DeepCopy())
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[portSecurityAnnotation] = "false"
	if err := r.Patch(ctx, pod, patch); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.FromContext(ctx).Info("turned port security off on egress gateway pod", "pod", req.NamespacedName)
	return ctrl.Result{}, nil
}

// ownedByChartGateway walks pod -> ReplicaSet -> Deployment -> VpcEgressGateway
// and checks that the gateway is one the vpc chart rendered for its own VPC.
func (r *EgressGatewayPodReconciler) ownedByChartGateway(ctx context.Context, pod *corev1.Pod) (bool, string, error) {
	vegName := pod.Labels[vegLabel]
	rsRef := metav1.GetControllerOf(pod)
	if rsRef == nil || rsRef.Kind != "ReplicaSet" || rsRef.APIVersion != "apps/v1" {
		return false, "pod is not controlled by a ReplicaSet", nil
	}
	rs := &appsv1.ReplicaSet{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: rsRef.Name}, rs); err != nil {
		return false, "", ignoreNotFound(err)
	}
	if rs.UID != rsRef.UID {
		return false, "ReplicaSet UID does not match the pod's owner reference", nil
	}
	depRef := metav1.GetControllerOf(rs)
	if depRef == nil || depRef.Kind != "Deployment" || depRef.APIVersion != "apps/v1" {
		return false, "ReplicaSet is not controlled by a Deployment", nil
	}
	dep := &appsv1.Deployment{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: depRef.Name}, dep); err != nil {
		return false, "", ignoreNotFound(err)
	}
	if dep.UID != depRef.UID {
		return false, "Deployment UID does not match the ReplicaSet's owner reference", nil
	}
	// Kube-OVN owns the Deployment through a plain owner reference, without
	// the controller flag, so it is looked up by kind and name.
	var vegRef *metav1.OwnerReference
	for i := range dep.OwnerReferences {
		ref := &dep.OwnerReferences[i]
		if ref.Kind == "VpcEgressGateway" && ref.APIVersion == kubeovnGroupVersion.String() && ref.Name == vegName {
			vegRef = ref
			break
		}
	}
	if vegRef == nil {
		return false, fmt.Sprintf("Deployment is not owned by VpcEgressGateway %q", vegName), nil
	}
	veg := &VpcEgressGateway{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: vegName}, veg); err != nil {
		return false, "", ignoreNotFound(err)
	}
	if veg.UID != vegRef.UID {
		return false, "VpcEgressGateway UID does not match the Deployment's owner reference", nil
	}
	if id := veg.Labels[vpcIDLabel]; id == "" || id != veg.Spec.VPC {
		return false, "VpcEgressGateway was not rendered by the vpc chart for its own VPC", nil
	}
	return true, "", nil
}

func ignoreNotFound(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
