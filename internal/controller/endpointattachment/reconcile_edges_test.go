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

package endpointattachment

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/localsdn/v1alpha1"
)

func newInterceptedClient(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithStatusSubresource(&cozyv1alpha1.EndpointAttachment{}).
		WithObjects(objs...).
		WithInterceptorFuncs(funcs).
		Build()
}

func dbObjects(ea *cozyv1alpha1.EndpointAttachment) []client.Object {
	return []client.Object{
		helmRelease("postgres-mydb", "hr-1", "Postgres", "mydb"),
		endpoint("postgres-mydb-ro", "Postgres", "mydb", map[string]string{"role": "replica"}, 5432),
		claimObject("held", "public", "IPv4", true, "203.0.113.7"),
		classObject("public", ""),
		ea,
	}
}

func dbAttachment() *cozyv1alpha1.EndpointAttachment {
	return attachment("ro-public", "ea-1", "Postgres", "mydb", "postgres-mydb-ro", cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held"})
}

func reconcileOnce(r *Reconciler, ea *cozyv1alpha1.EndpointAttachment) (ctrl.Result, error) {
	return r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ea)})
}

func getAttachment(t *testing.T, c client.Client, ea *cozyv1alpha1.EndpointAttachment) *cozyv1alpha1.EndpointAttachment {
	t.Helper()
	got := &cozyv1alpha1.EndpointAttachment{}
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(ea), got); err != nil {
		t.Fatal(err)
	}
	return got
}

// TestAttachedNeedsThisGeneration: when a new spec fails to render, the
// Provisioned=True left by the previous generation must not keep the
// attachment reading Attached.
func TestAttachedNeedsThisGeneration(t *testing.T) {
	ea := dbAttachment()
	fail := false
	c := newInterceptedClient(t, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
			if _, ok := o.(*corev1.Service); ok && fail {
				return errors.New("apiserver unavailable")
			}
			return c.Update(ctx, o, opts...)
		},
	}, dbObjects(ea)...)
	r := &Reconciler{Client: c}
	run(t, c, r, ea)
	setIngress(t, c, onlyRendered(t, c, ea), "203.0.113.7")
	if got := run(t, c, r, ea); got.Status.Phase != cozyv1alpha1.EndpointAttachmentAttached {
		t.Fatalf("phase = %s, want Attached before the spec change", got.Status.Phase)
	}

	live := getAttachment(t, c, ea)
	live.Spec.LoadBalancer.Ports = []int32{5432}
	live.Generation = 2
	if err := c.Update(context.TODO(), live); err != nil {
		t.Fatal(err)
	}
	ep := &corev1.Service{}
	if err := c.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: "postgres-mydb-ro"}, ep); err != nil {
		t.Fatal(err)
	}
	ep.Spec.Selector = map[string]string{"role": "primary"}
	if err := c.Update(context.TODO(), ep); err != nil {
		t.Fatal(err)
	}
	fail = true
	if _, err := reconcileOnce(r, ea); err == nil {
		t.Fatal("a failed Service update must surface as an error")
	}
	got := getAttachment(t, c, ea)
	if got.Status.Phase == cozyv1alpha1.EndpointAttachmentAttached {
		t.Fatal("phase must not stay Attached while the new generation is unrendered")
	}
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonRenderFailed)
}

// TestClaimInUseWithdrawsOwnService: when the substrate records another
// Service as the claim's holder, this attachment's Service goes.
func TestClaimInUseWithdrawsOwnService(t *testing.T) {
	ea := dbAttachment()
	rival := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "rival-lb", UID: "svc-rival"}}
	c := newClient(t, append(dbObjects(ea), rival, addressObject("203.0.113.7", nil))...)
	run(t, c, nil, ea)
	own := onlyRendered(t, c, ea)

	addr := addressObject("203.0.113.7", &types.NamespacedName{Namespace: ns, Name: rival.Name})
	live := addressObject("203.0.113.7", nil)
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(live), live); err != nil {
		t.Fatal(err)
	}
	live.Object["status"] = addr.Object["status"]
	if err := c.Update(context.TODO(), live); err != nil {
		t.Fatal(err)
	}
	got := run(t, c, nil, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonClaimInUse)
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(own), &corev1.Service{}); err == nil {
		t.Fatal("the Service that lost the claim must be withdrawn")
	}
	if got.Status.ServiceName != "" {
		t.Fatalf("status.serviceName = %q after withdrawal", got.Status.ServiceName)
	}
}

// TestDatapathGateBeforeClaim: with the cozy-proxy declaration withdrawn, a
// delegated Service goes even while the claim cannot be found.
func TestDatapathGateBeforeClaim(t *testing.T) {
	ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
	c := newClient(t, append(objs, endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box"))...)
	run(t, c, &Reconciler{CozyProxyContractImplemented: true}, ea)
	own := onlyRendered(t, c, ea)

	claim := newClaim()
	if err := c.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: "held"}, claim); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.TODO(), claim); err != nil {
		t.Fatal(err)
	}
	got := run(t, c, &Reconciler{CozyProxyContractImplemented: false}, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonDatapathUnavailable)
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(own), &corev1.Service{}); err == nil {
		t.Fatal("the delegated Service must be withdrawn")
	}
}

// TestStaleCacheDoesNotDuplicateService: a Service the previous reconcile
// created but the cache has not seen is found through the API reader rather
// than created again.
func TestStaleCacheDoesNotDuplicateService(t *testing.T) {
	ea := dbAttachment()
	backing := newClient(t, dbObjects(ea)...)
	c := interceptor.NewClient(backing.(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*corev1.ServiceList); ok {
				return nil
			}
			return cl.List(ctx, list, opts...)
		},
	})
	r := &Reconciler{Client: c, Reader: backing}
	run(t, backing, &Reconciler{Reader: backing}, ea)
	run(t, c, r, ea)
	if n := len(renderedServices(t, backing, ea)); n != 1 {
		t.Fatalf("expected one Service despite the stale cache, got %d", n)
	}
}

// TestDuplicateServicesCollapse: should two owned Services exist anyway, the
// oldest stays and the other is withdrawn.
func TestDuplicateServicesCollapse(t *testing.T) {
	ea := dbAttachment()
	owned := func(name string, age time.Duration) *corev1.Service {
		return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("svc-" + name),
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
			OwnerReferences:   []metav1.OwnerReference{controllerOwnerReference(ea)}},
			Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer}}
	}
	c := newClient(t, append(dbObjects(ea), owned("ro-public-old", time.Hour), owned("ro-public-new", time.Minute))...)
	got := run(t, c, nil, ea)
	svc := onlyRendered(t, c, ea)
	if svc.Name != "ro-public-old" || got.Status.ServiceName != "ro-public-old" {
		t.Fatalf("kept %q (status %q), want the oldest", svc.Name, got.Status.ServiceName)
	}
}

// TestSettledKeepsPollingWithoutWatches: without a claim watch nothing would
// enqueue the attachment on a claim change, so a settled reconcile polls.
func TestSettledKeepsPollingWithoutWatches(t *testing.T) {
	for _, watched := range []bool{false, true} {
		ea := dbAttachment()
		c := newClient(t, dbObjects(ea)...)
		r := &Reconciler{Client: c, claimWatched: watched}
		run(t, c, r, ea)
		setIngress(t, c, onlyRendered(t, c, ea), "203.0.113.7")
		res, err := reconcileOnce(r, ea)
		if err != nil {
			t.Fatal(err)
		}
		expectCondition(t, getAttachment(t, c, ea), cozyv1alpha1.ConditionProvisioned, metav1.ConditionTrue, cozyv1alpha1.ReasonProvisioned)
		if watched != (res.RequeueAfter == 0) {
			t.Fatalf("claimWatched=%v: RequeueAfter = %s", watched, res.RequeueAfter)
		}
	}
}

// TestSpecChangedDuringOwnershipPatch: the ownership patch returns the object
// as stored now; when that carries a newer spec, this reconcile stops instead
// of rendering from a mix of the two.
func TestSpecChangedDuringOwnershipPatch(t *testing.T) {
	ea := dbAttachment()
	c := newInterceptedClient(t, interceptor.Funcs{
		Patch: func(ctx context.Context, cl client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
			if err := cl.Patch(ctx, o, p, opts...); err != nil {
				return err
			}
			if got, ok := o.(*cozyv1alpha1.EndpointAttachment); ok {
				got.Generation++
			}
			return nil
		},
	}, dbObjects(ea)...)
	run(t, c, &Reconciler{Client: c}, ea)
	if n := len(renderedServices(t, c, ea)); n != 0 {
		t.Fatalf("nothing may be rendered from a spec that moved mid-reconcile, got %d", n)
	}
}

// TestFailedWithdrawalKeepsServiceName: a detach whose delete fails keeps
// naming the Service and says the withdrawal is pending.
func TestFailedWithdrawalKeepsServiceName(t *testing.T) {
	ea := dbAttachment()
	failDelete := false
	c := newInterceptedClient(t, interceptor.Funcs{
		Delete: func(ctx context.Context, cl client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
			if svc, ok := o.(*corev1.Service); ok && failDelete && svc.Name != "postgres-mydb-ro" {
				return errors.New("apiserver unavailable")
			}
			return cl.Delete(ctx, o, opts...)
		},
	}, dbObjects(ea)...)
	r := &Reconciler{Client: c}
	run(t, c, r, ea)
	own := onlyRendered(t, c, ea)
	if err := c.Delete(context.TODO(), endpoint("postgres-mydb-ro", "Postgres", "mydb", nil)); err != nil {
		t.Fatal(err)
	}
	failDelete = true
	if _, err := reconcileOnce(r, ea); err == nil {
		t.Fatal("a failed withdrawal must surface as an error")
	}
	got := getAttachment(t, c, ea)
	if got.Status.ServiceName != own.Name {
		t.Fatalf("status.serviceName = %q, want %q kept until the delete succeeds", got.Status.ServiceName, own.Name)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, cozyv1alpha1.ConditionProvisioned); c == nil || !strings.Contains(c.Message, "pending") {
		t.Fatalf("message must say the withdrawal is pending, got %+v", c)
	}
}

// TestRenderSettlesWithoutRewrites: once the stored Service matches, another
// reconcile sends no update, in either node-port mode and for the whole-IP
// sentinel port.
func TestRenderSettlesWithoutRewrites(t *testing.T) {
	for _, nodePorts := range []bool{false, true} {
		ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
		updates := 0
		c := newInterceptedClient(t, interceptor.Funcs{
			Update: func(ctx context.Context, cl client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
				if _, ok := o.(*corev1.Service); ok {
					updates++
				}
				return cl.Update(ctx, o, opts...)
			},
		}, append(objs, endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box"))...)
		r := &Reconciler{Client: c, CozyProxyContractImplemented: true, AllocateLoadBalancerNodePorts: nodePorts}
		run(t, c, r, ea)
		svc := onlyRendered(t, c, ea)
		if svc.Spec.AllocateLoadBalancerNodePorts == nil || *svc.Spec.AllocateLoadBalancerNodePorts != nodePorts {
			t.Fatalf("allocateLoadBalancerNodePorts = %v, want %v", svc.Spec.AllocateLoadBalancerNodePorts, nodePorts)
		}
		if svc.Spec.Ports[0].TargetPort != intstr.FromInt32(SentinelPort) {
			t.Fatalf("sentinel targetPort = %v", svc.Spec.Ports[0].TargetPort)
		}
		updates = 0
		run(t, c, r, ea)
		if updates != 0 {
			t.Fatalf("nodePorts=%v: %d Service updates on an unchanged reconcile", nodePorts, updates)
		}
	}
}

// TestPort65535IsNotAPlaceholder: a Service serving on 65535 is mirrored like
// any other; only the headless VM placeholder publishes nothing.
func TestPort65535IsNotAPlaceholder(t *testing.T) {
	ea := attachment("app-ip", "ea-1", "Postgres", "mydb", "postgres-mydb-ro", cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held"})
	ep := endpoint("postgres-mydb-ro", "Postgres", "mydb", map[string]string{"role": "replica"}, SentinelPort)
	ep.Spec.ClusterIP = ""
	ep.Spec.Ports[0].TargetPort = intstr.FromInt32(8443)
	objs := dbObjects(ea)
	objs[1] = ep
	c := newClient(t, objs...)
	run(t, c, nil, ea)
	svc := onlyRendered(t, c, ea)
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != SentinelPort || svc.Spec.Ports[0].TargetPort != intstr.FromInt32(8443) {
		t.Fatalf("ports = %+v, want the endpoint's mirrored", svc.Spec.Ports)
	}
}

// TestSelectorlessEndpointPublishesNothing: a Service with hand-managed
// endpoints has no selector to mirror, so nothing is rendered.
func TestSelectorlessEndpointPublishesNothing(t *testing.T) {
	ea := dbAttachment()
	objs := dbObjects(ea)
	objs[1] = endpoint("postgres-mydb-ro", "Postgres", "mydb", nil, 5432)
	c := newClient(t, objs...)
	got := run(t, c, nil, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonNothingToPublish)
	if n := len(renderedServices(t, c, ea)); n != 0 {
		t.Fatalf("nothing may be rendered for a selectorless endpoint, got %d", n)
	}
}

// TestStoppedBindingIsKeptOnlyForItsEndpoint: retargeting an attachment
// whose VM is stopped withdraws the binding made for the old endpoint.
func TestStoppedBindingIsKeptOnlyForItsEndpoint(t *testing.T) {
	ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
	fip := existingFloatingIP(ea, vpcIP)
	fip.SetAnnotations(map[string]string{endpointAnnotation: "vm-instance-other"})
	c := newCozyplaneClient(t, append(objs, fip)...)
	run(t, c, &Reconciler{CozyProxyContractImplemented: true}, ea)
	if n := len(ownedFloatingIPs(t, c, ea)); n != 0 {
		t.Fatalf("a binding for another endpoint must be withdrawn, %d left", n)
	}
}

// TestVPCAnnotationChangesEnqueue: a pod gaining or changing its VPC address
// enqueues the whole-IP attachments of its namespace, and unrelated pod
// changes do not.
func TestVPCAnnotationChangesEnqueue(t *testing.T) {
	ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
	db := attachment("db-ip", "ea-2", "Postgres", "mydb", "postgres-mydb-ro", cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held"})
	c := newClient(t, append(objs, db)...)
	r := &Reconciler{Client: c}

	p := vpcAnnotationsChanged()
	before := launcherPod("virt-launcher-box", vpcName, "", nil)
	after := launcherPod("virt-launcher-box", vpcName, vpcIP, nil)
	if !p.Update(event.UpdateEvent{ObjectOld: before, ObjectNew: after}) {
		t.Fatal("a newly stamped VPC address must enqueue")
	}
	if p.Update(event.UpdateEvent{ObjectOld: after, ObjectNew: after.DeepCopy()}) {
		t.Fatal("an unchanged VPC address must not enqueue")
	}
	if p.Create(event.CreateEvent{Object: launcherPod("plain", "", "", nil)}) {
		t.Fatal("a pod outside any VPC must not enqueue")
	}
	reqs := r.mapPod(context.TODO(), after)
	if len(reqs) != 1 || reqs[0].Name != ea.Name {
		t.Fatalf("enqueued %v, want only the whole-IP attachment %s", reqs, ea.Name)
	}
}

// TestAttachedNeedsThisGenerationOnEarlyFailure: a reconcile of a new spec
// that fails before it reaches the Provisioned step leaves the previous
// generation's Provisioned=True in place, which alone must not read Attached.
func TestAttachedNeedsThisGenerationOnEarlyFailure(t *testing.T) {
	ea := dbAttachment()
	fail := false
	c := newInterceptedClient(t, interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*corev1.ServiceList); ok && fail {
				return errors.New("apiserver unavailable")
			}
			return cl.List(ctx, list, opts...)
		},
	}, dbObjects(ea)...)
	r := &Reconciler{Client: c}
	run(t, c, r, ea)
	setIngress(t, c, onlyRendered(t, c, ea), "203.0.113.7")
	run(t, c, r, ea)

	live := getAttachment(t, c, ea)
	live.Spec.LoadBalancer.Ports = []int32{5432}
	live.Generation = 2
	if err := c.Update(context.TODO(), live); err != nil {
		t.Fatal(err)
	}
	fail = true
	if _, err := reconcileOnce(r, ea); err == nil {
		t.Fatal("the failed lookup must surface as an error")
	}
	if got := getAttachment(t, c, ea); got.Status.Phase == cozyv1alpha1.EndpointAttachmentAttached {
		t.Fatal("a previous generation's Provisioned=True must not project Attached")
	}
}
