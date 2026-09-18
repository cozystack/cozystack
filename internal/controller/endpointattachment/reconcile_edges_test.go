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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
		WithObjects(withBackendPods(objs)...).
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
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonWithdrawalPending)
	if c := meta.FindStatusCondition(got.Status.Conditions, cozyv1alpha1.ConditionProvisioned); !strings.Contains(c.Message, "pending") {
		t.Fatalf("message must say the withdrawal is pending, got %q", c.Message)
	}
	if got.Status.Phase == cozyv1alpha1.EndpointAttachmentDetached {
		t.Fatal("phase must not read Detached while the Service is still published")
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

// TestWithdrawalReachesServicesTheCacheMissed: a detach withdraws every
// owned Service the API holds, not only the one the cache showed.
func TestWithdrawalReachesServicesTheCacheMissed(t *testing.T) {
	ea := dbAttachment()
	owned := func(name string, age time.Duration) *corev1.Service {
		return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("svc-" + name),
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
			OwnerReferences:   []metav1.OwnerReference{controllerOwnerReference(ea)}}}
	}
	objs := dbObjects(ea)
	objs = append(objs[:1], objs[2:]...) // no endpoint Service: the attachment detaches
	backing := newClient(t, append(objs, owned("ro-public-seen", time.Hour), owned("ro-public-unseen", time.Minute))...)
	c := interceptor.NewClient(backing.(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if err := cl.List(ctx, list, opts...); err != nil {
				return err
			}
			if sl, ok := list.(*corev1.ServiceList); ok {
				var kept []corev1.Service
				for _, svc := range sl.Items {
					if svc.Name != "ro-public-unseen" {
						kept = append(kept, svc)
					}
				}
				sl.Items = kept
			}
			return nil
		},
	})
	if _, err := reconcileOnce(&Reconciler{Client: c, Reader: backing}, ea); err != nil {
		t.Fatal(err)
	}
	if n := len(renderedServices(t, backing, ea)); n != 0 {
		t.Fatalf("every owned Service must be withdrawn, %d left", n)
	}
}

// TestSelectorLossWithdrawsBeforeTheClaim: an endpoint that loses its
// selector withdraws what was published even when the claim is gone too.
func TestSelectorLossWithdrawsBeforeTheClaim(t *testing.T) {
	ea := dbAttachment()
	c := newClient(t, dbObjects(ea)...)
	run(t, c, nil, ea)
	own := onlyRendered(t, c, ea)

	ep := &corev1.Service{}
	if err := c.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: "postgres-mydb-ro"}, ep); err != nil {
		t.Fatal(err)
	}
	ep.Spec.Selector = nil
	if err := c.Update(context.TODO(), ep); err != nil {
		t.Fatal(err)
	}
	claim := newClaim()
	if err := c.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: "held"}, claim); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.TODO(), claim); err != nil {
		t.Fatal(err)
	}
	got := run(t, c, nil, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonNothingToPublish)
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(own), &corev1.Service{}); err == nil {
		t.Fatal("the Service mirroring the old selector must be withdrawn")
	}
}

// TestMissingClaimIsNotReportedHeld: a referenced claim that is gone is no
// longer named in status, and a later detach does not claim it holds an
// address.
func TestMissingClaimIsNotReportedHeld(t *testing.T) {
	ea := dbAttachment()
	c := newClient(t, dbObjects(ea)...)
	run(t, c, nil, ea)

	claim := newClaim()
	if err := c.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: "held"}, claim); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.TODO(), claim); err != nil {
		t.Fatal(err)
	}
	got := run(t, c, nil, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonClaimNotFound)
	if got.Status.ClaimName != "" || len(got.Status.Addresses) != 0 {
		t.Fatalf("a missing claim must not stay in status: %+v", got.Status)
	}

	live := getAttachment(t, c, ea)
	live.Status.ClaimName = "held"
	if err := c.Status().Update(context.TODO(), live); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.TODO(), endpoint("postgres-mydb-ro", "Postgres", "mydb", nil)); err != nil {
		t.Fatal(err)
	}
	got = run(t, c, nil, ea)
	if c := meta.FindStatusCondition(got.Status.Conditions, cozyv1alpha1.ConditionProvisioned); c != nil && strings.Contains(c.Message, "held by") {
		t.Fatalf("a detach must not report a missing claim as holding the address: %q", c.Message)
	}
	if got.Status.ClaimName != "" {
		t.Fatalf("status.claimName = %q for a claim that no longer exists", got.Status.ClaimName)
	}
}

// TestSwitchWaitsForAFinalizedService: a Service held by a finalizer stays
// listed after its delete is accepted, and no FloatingIP wears the claim
// until it is gone.
func TestSwitchWaitsForAFinalizedService(t *testing.T) {
	ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
	c := newCozyplaneClient(t, append(objs,
		launcherPod("virt-launcher-box", vpcName, vpcIP, nil),
		endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box"),
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "box-ip-old", UID: "svc-old",
			Finalizers:      []string{"service.kubernetes.io/load-balancer-cleanup"},
			OwnerReferences: []metav1.OwnerReference{controllerOwnerReference(ea)}}})...)
	r := &Reconciler{CozyProxyContractImplemented: true}
	for i := 0; i < 2; i++ {
		got := run(t, c, r, ea)
		expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonWithdrawalPending)
		if n := len(ownedFloatingIPs(t, c, ea)); n != 0 {
			t.Fatalf("pass %d: no FloatingIP while the old Service is still being removed, got %d", i, n)
		}
	}
	old := &corev1.Service{}
	if err := c.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: "box-ip-old"}, old); err != nil {
		t.Fatal(err)
	}
	old.Finalizers = nil
	if err := c.Update(context.TODO(), old); err != nil {
		t.Fatal(err)
	}
	run(t, c, r, ea)
	onlyFloatingIP(t, c, ea)
}

// TestUnstampedBackendHoldsTheBinding: while one backend has no VPC address
// yet, nothing is bound or re-pointed, and a binding made for another
// endpoint is withdrawn.
func TestUnstampedBackendHoldsTheBinding(t *testing.T) {
	t.Run("no binding yet", func(t *testing.T) {
		ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
		c := newCozyplaneClient(t, append(objs,
			launcherPod("virt-launcher-a", vpcName, vpcIP, nil),
			launcherPod("virt-launcher-b", vpcName, "", nil),
			endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-a", "virt-launcher-b"))...)
		got := run(t, c, nil, ea)
		expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonWaitingForVPCAddress)
		if n := len(ownedFloatingIPs(t, c, ea)); n != 0 {
			t.Fatalf("nothing may be bound while a backend is unstamped, got %d", n)
		}
	})
	t.Run("binding for this endpoint stays as it is", func(t *testing.T) {
		ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
		fip := existingFloatingIP(ea, "10.10.0.9")
		fip.SetAnnotations(map[string]string{endpointAnnotation: "vm-instance-box"})
		c := newCozyplaneClient(t, append(objs, fip,
			launcherPod("virt-launcher-a", vpcName, vpcIP, nil),
			launcherPod("virt-launcher-b", vpcName, "", nil),
			endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-a", "virt-launcher-b"))...)
		run(t, c, nil, ea)
		if v, _, _ := unstructured.NestedString(onlyFloatingIP(t, c, ea).Object, "spec", "target"); v != "10.10.0.9" {
			t.Fatalf("target re-pointed to %q before every backend was stamped", v)
		}
	})
	t.Run("binding for another endpoint goes", func(t *testing.T) {
		ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
		fip := existingFloatingIP(ea, vpcIP)
		fip.SetAnnotations(map[string]string{endpointAnnotation: "vm-instance-other"})
		c := newCozyplaneClient(t, append(objs, fip,
			launcherPod("virt-launcher-box", vpcName, "", nil),
			endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box"))...)
		got := run(t, c, nil, ea)
		expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonWaitingForVPCAddress)
		if n := len(ownedFloatingIPs(t, c, ea)); n != 0 {
			t.Fatalf("the old endpoint's binding must be withdrawn, %d left", n)
		}
	})
}

// TestOwnershipPatchRefusesAReplacedObject: the ownership patch carries the
// resource version it read, so it cannot land on a same-name replacement.
func TestOwnershipPatchRefusesAReplacedObject(t *testing.T) {
	ea := dbAttachment()
	c := newClient(t, dbObjects(ea)...)
	stale := getAttachment(t, c, ea)
	live := stale.DeepCopy()
	live.Labels = map[string]string{"touched": "true"}
	if err := c.Update(context.TODO(), live); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: c}
	if _, err := r.ensureAttachmentOwnership(context.TODO(), stale, helmRelease("postgres-mydb", "hr-1", "Postgres", "mydb")); err == nil {
		t.Fatal("a patch from a stale read must conflict")
	}
}

type failingMapper struct{ meta.RESTMapper }

func (failingMapper) RESTMapping(schema.GroupKind, ...string) (*meta.RESTMapping, error) {
	return nil, errors.New("the server is currently unable to handle the request")
}

// TestOptionalKindDiscovery: a kind that is not served is no error, and a
// failed discovery is reported to the caller rather than ending setup.
func TestOptionalKindDiscovery(t *testing.T) {
	m := meta.NewDefaultRESTMapper(nil)
	m.Add(floatingIPGVK, meta.RESTScopeNamespace)
	if served, err := kindServed(m, floatingIPGVK); !served || err != nil {
		t.Fatalf("served kind: served=%v err=%v", served, err)
	}
	if served, err := kindServed(m, claimGVK); served || err != nil {
		t.Fatalf("unserved kind: served=%v err=%v", served, err)
	}
	if served, err := kindServed(failingMapper{m}, floatingIPGVK); served || err == nil {
		t.Fatalf("failed discovery: served=%v err=%v", served, err)
	}
}

// TestDetachedNeedsThisGeneration: a Detached left by an earlier generation
// must not describe a retargeted spec the controller has not evaluated.
func TestDetachedNeedsThisGeneration(t *testing.T) {
	ea := dbAttachment()
	objs := dbObjects(ea)
	objs = append(objs[:1], objs[2:]...)
	fail := false
	c := newInterceptedClient(t, interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if fail {
				return errors.New("apiserver unavailable")
			}
			return cl.List(ctx, list, opts...)
		},
	}, objs...)
	r := &Reconciler{Client: c}
	if got := run(t, c, r, ea); got.Status.Phase != cozyv1alpha1.EndpointAttachmentDetached && got.Status.Phase != cozyv1alpha1.EndpointAttachmentPending {
		t.Fatalf("phase = %s", got.Status.Phase)
	}
	live := getAttachment(t, c, ea)
	live.Status.Conditions = nil
	if err := c.Status().Update(context.TODO(), live); err != nil {
		t.Fatal(err)
	}
	ea2 := getAttachment(t, c, ea)
	ea2.Status.Conditions = []metav1.Condition{
		{Type: cozyv1alpha1.ConditionResolved, Status: metav1.ConditionFalse, Reason: cozyv1alpha1.ReasonEndpointNotFound, ObservedGeneration: 1, LastTransitionTime: metav1.Now()},
		{Type: cozyv1alpha1.ConditionProvisioned, Status: metav1.ConditionFalse, Reason: cozyv1alpha1.ReasonEndpointDetached, ObservedGeneration: 1, LastTransitionTime: metav1.Now()},
	}
	if err := c.Status().Update(context.TODO(), ea2); err != nil {
		t.Fatal(err)
	}
	ea3 := getAttachment(t, c, ea)
	ea3.Spec.Endpoint.ServiceName = "postgres-mydb-rw"
	ea3.Generation = 2
	if err := c.Update(context.TODO(), ea3); err != nil {
		t.Fatal(err)
	}
	fail = true
	if _, err := reconcileOnce(r, ea); err == nil {
		t.Fatal("the failed lookup must surface")
	}
	if got := getAttachment(t, c, ea); got.Status.Phase == cozyv1alpha1.EndpointAttachmentDetached {
		t.Fatal("an earlier generation's Detached must not describe the retargeted spec")
	}
}

// TestChartDelegationWins: a delegated Service the attachment does not own,
// such as a vm-instance chart Service with external: true, takes the backend
// even when it appears after the attachment's own Service, because nothing
// would ever withdraw it.
func TestChartDelegationWins(t *testing.T) {
	ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
	c := newClient(t, append(objs, endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box"))...)
	r := &Reconciler{CozyProxyContractImplemented: true}
	run(t, c, r, ea)
	own := onlyRendered(t, c, ea)

	chart := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "vm-instance-box-external", UID: "svc-chart",
		CreationTimestamp: metav1.NewTime(own.CreationTimestamp.Add(time.Hour)),
		Labels:            map[string]string{ServiceProxyNameLabel: ServiceProxyName},
		Annotations:       map[string]string{WholeIPAnnotation: "false"}},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, Selector: map[string]string{"vm": "box"},
			Ports: []corev1.ServicePort{{Name: "port-22", Port: 22}}}}
	for _, o := range []client.Object{chart, endpointSlice("vm-instance-box-external-1", chart.Name, true, "virt-launcher-box")} {
		if err := c.Create(context.TODO(), o); err != nil {
			t.Fatal(err)
		}
	}
	got := run(t, c, r, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonBackendAlreadyDelegated)
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(own), &corev1.Service{}); err == nil {
		t.Fatal("the attachment's Service must yield the backend to the chart's")
	}
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(chart), &corev1.Service{}); err != nil {
		t.Fatalf("the chart's Service must be left alone: %v", err)
	}
}

// TestDualClaimRefusedInVPC: a FloatingIP wears one address, so a Dual
// claim is refused on the VPC path rather than bound and left pending, and a
// binding made earlier is withdrawn.
func TestDualClaimRefusedInVPC(t *testing.T) {
	for _, withFIP := range []bool{false, true} {
		ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
		for i, o := range objs {
			if u, ok := o.(*unstructured.Unstructured); ok && u.GetName() == "held" {
				objs[i] = claimObject("held", "public", "Dual", true, "203.0.113.7", "2001:db8::7")
			}
		}
		objs = append(objs,
			launcherPod("virt-launcher-box", vpcName, vpcIP, nil),
			endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box"))
		if withFIP {
			objs = append(objs, existingFloatingIP(ea, vpcIP))
		}
		c := newCozyplaneClient(t, objs...)
		got := run(t, c, nil, ea)
		expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonDualStackUnsupportedInVPC)
		if n := len(ownedFloatingIPs(t, c, ea)); n != 0 {
			t.Fatalf("withFIP=%v: no FloatingIP may stand for a Dual claim, got %d", withFIP, n)
		}
		if n := len(renderedServices(t, c, ea)); n != 0 {
			t.Fatalf("withFIP=%v: no Service either, got %d", withFIP, n)
		}
	}
}

// TestKCCMServiceIsNotAnEndpoint: a LoadBalancer Service the cloud-controller-
// manager of a tenant Kubernetes cluster creates carries the cluster's
// lineage and the tenant-resource mark, but belongs to a workload inside that
// cluster, so it is not attachable.
func TestKCCMServiceIsNotAnEndpoint(t *testing.T) {
	ea := attachment("lb-ip", "ea-1", "Kubernetes", "mycluster", "a1b2c3d4", cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held"})
	kccm := endpoint("a1b2c3d4", "Kubernetes", "mycluster", map[string]string{"app": "web"}, 443)
	kccm.Labels[kccmTenantServiceLabel] = "web-lb"
	c := newClient(t, ea, kccm,
		helmRelease("kubernetes-mycluster", "hr-1", "Kubernetes", "mycluster"),
		claimObject("held", "public", "IPv4", true, "203.0.113.7"), classObject("public", ""))
	got := run(t, c, nil, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionResolved, metav1.ConditionFalse, cozyv1alpha1.ReasonEndpointNotTenantFacing)
	if n := len(renderedServices(t, c, ea)); n != 0 {
		t.Fatalf("nothing may be rendered for a kccm Service, got %d", n)
	}
}

// TestUnrecordedBindingIsNotKept: a FloatingIP made before the endpoint was
// recorded on it cannot be told to belong to the current endpoint, so it is
// not kept for a stopped VM or across a retarget.
func TestUnrecordedBindingIsNotKept(t *testing.T) {
	for _, tt := range []struct {
		name string
		pods []client.Object
	}{
		{name: "stopped VM"},
		{name: "retarget onto an unstamped pod", pods: []client.Object{
			launcherPod("virt-launcher-box", vpcName, "", nil),
			endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box"),
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
			fip := existingFloatingIP(ea, vpcIP)
			fip.SetAnnotations(nil)
			c := newCozyplaneClient(t, append(append(objs, fip), tt.pods...)...)
			run(t, c, &Reconciler{CozyProxyContractImplemented: true}, ea)
			if n := len(ownedFloatingIPs(t, c, ea)); n != 0 {
				t.Fatalf("an unrecorded binding must be withdrawn, %d left", n)
			}
		})
	}
}

// TestServiceWaitsForTheFloatingIPsService: the switch from a FloatingIP back
// to a Service deletes the FloatingIP in the foreground, which keeps it
// listed until the Service it owns is gone, and renders nothing while it is
// listed, whether or not the substrate recorded that Service as the claim's
// holder.
func TestServiceWaitsForTheFloatingIPsService(t *testing.T) {
	ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
	fip := existingFloatingIP(ea, vpcIP)
	fip.SetFinalizers([]string{metav1.FinalizerDeleteDependents})
	fipSvc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "box-ip-fip01-abcde", UID: "fip-svc",
		Annotations:     map[string]string{ClaimAnnotation: "held"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "sdn.cozystack.io/v1alpha1", Kind: "FloatingIP", Name: fip.GetName(), UID: fip.GetUID(), Controller: new(true)}}}}
	var policies []metav1.DeletionPropagation
	m := meta.NewDefaultRESTMapper(nil)
	m.Add(floatingIPGVK, meta.RESTScopeNamespace)
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithRESTMapper(m).
		WithStatusSubresource(&cozyv1alpha1.EndpointAttachment{}).
		WithObjects(withBackendPods(append(objs, fip, fipSvc,
			launcherPod("virt-launcher-box", "", "", nil),
			endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box")))...).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, cl client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
				if u, ok := o.(*unstructured.Unstructured); ok && u.GroupVersionKind().Kind == "FloatingIP" {
					do := &client.DeleteOptions{}
					do.ApplyOptions(opts)
					if do.PropagationPolicy != nil {
						policies = append(policies, *do.PropagationPolicy)
					}
				}
				return cl.Delete(ctx, o, opts...)
			},
		}).Build()
	r := &Reconciler{CozyProxyContractImplemented: true}
	for i := 0; i < 2; i++ {
		got := run(t, c, r, ea)
		expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonWithdrawalPending)
		if n := len(renderedServices(t, c, ea)); n != 0 {
			t.Fatalf("pass %d: no Service while the FloatingIP's own Service may still wear the claim, got %d", i, n)
		}
	}
	if len(policies) == 0 || policies[0] != metav1.DeletePropagationForeground {
		t.Fatalf("the FloatingIP must be deleted in the foreground, got %v", policies)
	}
	live := newFloatingIP()
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(fip), live); err != nil {
		t.Fatal(err)
	}
	live.SetFinalizers(nil)
	if err := c.Update(context.TODO(), live); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.TODO(), fipSvc); err != nil {
		t.Fatal(err)
	}
	run(t, c, r, ea)
	onlyRendered(t, c, ea)
}

// TestCozyProxyRefusesNonIPv4: cozy-proxy programs IPv4 only, so a method
// attachment on its path with an IPv6 or Dual claim renders nothing, and a
// Service rendered earlier is withdrawn. The cozyplane FloatingIP path still
// serves IPv6, and a plain attachment with no method is not affected.
func TestCozyProxyRefusesNonIPv4(t *testing.T) {
	claims := map[string][]string{"IPv6": {"2001:db8::7"}, "Dual": {"203.0.113.7", "2001:db8::7"}}
	for _, method := range []cozyv1alpha1.ExposureMethod{cozyv1alpha1.ExposureMethodWholeIP, cozyv1alpha1.ExposureMethodPortList} {
		for family, addrs := range claims {
			t.Run(string(method)+"/"+family, func(t *testing.T) {
				ea, objs := wholeIPVM(method)
				for i, o := range objs {
					if u, ok := o.(*unstructured.Unstructured); ok && u.GetName() == "held" {
						objs[i] = claimObject("held", "public", family, true, addrs...)
					}
				}
				c := newClient(t, append(objs, endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box"))...)
				stale := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "box-ip-old", UID: "svc-old",
					OwnerReferences: []metav1.OwnerReference{controllerOwnerReference(ea)}},
					Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, Ports: []corev1.ServicePort{{Port: 22}}}}
				if err := c.Create(context.TODO(), stale); err != nil {
					t.Fatal(err)
				}
				got := run(t, c, &Reconciler{CozyProxyContractImplemented: true}, ea)
				expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonFamilyUnsupportedByDatapath)
				if n := len(renderedServices(t, c, ea)); n != 0 {
					t.Fatalf("no Service may stand for a %s claim on the cozy-proxy path, got %d", family, n)
				}
			})
		}
	}
	t.Run("cozyplane serves IPv6", func(t *testing.T) {
		ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
		for i, o := range objs {
			if u, ok := o.(*unstructured.Unstructured); ok && u.GetName() == "held" {
				objs[i] = claimObject("held", "public", "IPv6", true, "2001:db8::7")
			}
		}
		c := newCozyplaneClient(t, append(objs,
			launcherPod("virt-launcher-box", vpcName, vpcIP, nil),
			endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box"))...)
		run(t, c, nil, ea)
		onlyFloatingIP(t, c, ea)
	})
	t.Run("no method is not affected", func(t *testing.T) {
		ea := attachment("ro-public", "ea-1", "Postgres", "mydb", "postgres-mydb-ro", cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held"})
		c := newClient(t, helmRelease("postgres-mydb", "hr-1", "Postgres", "mydb"), ea,
			endpoint("postgres-mydb-ro", "Postgres", "mydb", map[string]string{"role": "replica"}, 5432),
			claimObject("held", "public", "IPv6", true, "2001:db8::7"), classObject("public", ""))
		run(t, c, nil, ea)
		onlyRendered(t, c, ea)
	})
}

// TestNothingRendersBeforeTheClassIsAccepted: a consumer's loadBalancerClass
// is fixed at creation, and the class's provisioner reports it in status, so
// neither a Service nor a FloatingIP is created until the class is Accepted
// for its current generation. An empty status.loadBalancerClass before that
// means not reported yet, not the cluster default.
func TestNothingRendersBeforeTheClassIsAccepted(t *testing.T) {
	notReported := pendingClassObject("public")
	stale := classObject("public", "metallb.io/public")
	stale.SetGeneration(2)
	refused := classObject("public", "")
	refused.Object["status"].(map[string]interface{})["conditions"] = []interface{}{map[string]interface{}{
		"type": "Accepted", "status": "False", "reason": "PoolRefused", "message": "IPAddressPool cozy-metallb/public auto-assigns", "observedGeneration": int64(1),
	}}
	for name, class := range map[string]*unstructured.Unstructured{"not reported": notReported, "stale generation": stale, "refused": refused} {
		t.Run("service/"+name, func(t *testing.T) {
			ea := attachment("ro-public", "ea-1", "Postgres", "mydb", "postgres-mydb-ro", cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held"})
			c := newClient(t, helmRelease("postgres-mydb", "hr-1", "Postgres", "mydb"), ea,
				endpoint("postgres-mydb-ro", "Postgres", "mydb", map[string]string{"role": "replica"}, 5432),
				claimObject("held", "public", "IPv4", true, "203.0.113.7"), class.DeepCopy())
			got := run(t, c, nil, ea)
			expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonClassNotAccepted)
			if n := len(renderedServices(t, c, ea)); n != 0 {
				t.Fatalf("no Service may be created before the class is accepted, got %d", n)
			}
		})
		t.Run("floatingip/"+name, func(t *testing.T) {
			ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
			for i, o := range objs {
				if u, ok := o.(*unstructured.Unstructured); ok && u.GroupVersionKind() == classGVK {
					objs[i] = class.DeepCopy()
				}
			}
			c := newCozyplaneClient(t, append(objs,
				launcherPod("virt-launcher-box", vpcName, vpcIP, nil),
				endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box"))...)
			got := run(t, c, nil, ea)
			expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonClassNotAccepted)
			if n := len(ownedFloatingIPs(t, c, ea)); n != 0 {
				t.Fatalf("no FloatingIP may be created before the class is accepted, got %d", n)
			}
		})
	}
}

// TestPublishedServiceSurvivesClassLosingAcceptance: a class whose
// provisioner withdraws acceptance later, such as a pool turned unusable,
// does not take a live address down with it.
func TestPublishedServiceSurvivesClassLosingAcceptance(t *testing.T) {
	ea := attachment("ro-public", "ea-1", "Postgres", "mydb", "postgres-mydb-ro", cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held"})
	c := newClient(t, helmRelease("postgres-mydb", "hr-1", "Postgres", "mydb"), ea,
		endpoint("postgres-mydb-ro", "Postgres", "mydb", map[string]string{"role": "replica"}, 5432),
		claimObject("held", "public", "IPv4", true, "203.0.113.7"), classObject("public", ""))
	run(t, c, nil, ea)
	svc := onlyRendered(t, c, ea)

	class := &unstructured.Unstructured{}
	class.SetGroupVersionKind(classGVK)
	if err := c.Get(context.TODO(), client.ObjectKey{Name: "public"}, class); err != nil {
		t.Fatal(err)
	}
	class.Object["status"].(map[string]interface{})["conditions"] = []interface{}{map[string]interface{}{
		"type": "Accepted", "status": "False", "reason": "PoolRefused", "observedGeneration": int64(1),
	}}
	if err := c.Update(context.TODO(), class); err != nil {
		t.Fatal(err)
	}
	got := run(t, c, nil, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonClassNotAccepted)
	if live := onlyRendered(t, c, ea); live.UID != svc.UID {
		t.Fatal("the published Service must stay while the class is not accepted")
	}
}
