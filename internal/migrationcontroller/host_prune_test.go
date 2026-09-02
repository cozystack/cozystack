// SPDX-License-Identifier: Apache-2.0

package migrationcontroller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	migrationv1alpha1 "github.com/cozystack/cozystack/api/migration/v1alpha1"
)

// hostOverride builds the Host and Secret pair the controller creates for one
// entry, as an earlier reconcile would have left them.
func hostOverride(src *migrationv1alpha1.VMImportSource, id string) (*unstructured.Unstructured, *corev1.Secret) {
	name := hostOverrideName(src.Name, id)
	owner := ownerRef(migrationv1alpha1.GroupVersion.WithKind("VMImportSource"), src.Name, src.UID)

	host := newObject(hostGVK)
	host.SetName(name)
	host.SetNamespace(src.Namespace)
	host.SetLabels(map[string]string{migrationv1alpha1.ManagedByLabel: migrationv1alpha1.ManagedByValue})
	host.SetOwnerReferences([]metav1.OwnerReference{owner})

	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:            name,
		Namespace:       src.Namespace,
		Labels:          map[string]string{migrationv1alpha1.ManagedByLabel: migrationv1alpha1.ManagedByValue},
		OwnerReferences: []metav1.OwnerReference{owner},
	}}
	return host, secret
}

// A Source is editable, so an override can be dropped after it was created. A
// Host left behind is not inert — Forklift keeps routing that ESXi host's
// transfers through the address it names — and its credentials would otherwise
// outlive the decision to stop using them.
func TestEditingASourceRemovesTheHostsItNoLongerLists(t *testing.T) {
	s := testScheme(t)
	src := vsphereSource("vcenter", "tenant-foo")
	src.Spec.Hosts = []migrationv1alpha1.HostOverride{{
		ID:      "host-10",
		Address: "10.0.30.29",
		Credentials: migrationv1alpha1.ProviderCredentials{
			Username: "root", Password: "x", InsecureSkipVerify: true,
		},
	}}

	keptHost, keptSecret := hostOverride(src, "host-10")
	goneHost, goneSecret := hostOverride(src, "host-99")

	c := clientfake.NewClientBuilder().WithScheme(s).
		WithObjects(src, keptHost, keptSecret, goneHost, goneSecret).Build()
	r := &VMImportSourceReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}

	if err := r.pruneHosts(context.Background(), src); err != nil {
		t.Fatalf("pruneHosts: %v", err)
	}

	gone := types.NamespacedName{Namespace: "tenant-foo", Name: hostOverrideName("vcenter", "host-99")}
	if err := c.Get(context.Background(), gone, newObject(hostGVK)); !apierrors.IsNotFound(err) {
		t.Errorf("the dropped Host survived (err %v); it would keep redirecting transfers", err)
	}
	if err := c.Get(context.Background(), gone, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Errorf("the dropped Host's credentials survived (err %v)", err)
	}

	kept := types.NamespacedName{Namespace: "tenant-foo", Name: hostOverrideName("vcenter", "host-10")}
	if err := c.Get(context.Background(), kept, newObject(hostGVK)); err != nil {
		t.Errorf("the still-listed Host was removed: %v", err)
	}
	if err := c.Get(context.Background(), kept, &corev1.Secret{}); err != nil {
		t.Errorf("the still-listed Host's credentials were removed: %v", err)
	}
}

// The label says this controller made the object, not that it made it for this
// Source. Two Sources in one namespace must not delete each other's Hosts.
func TestPruningLeavesAnotherSourcesHostsAlone(t *testing.T) {
	s := testScheme(t)
	mine := vsphereSource("vcenter", "tenant-foo")
	theirs := vsphereSource("other", "tenant-foo")
	theirs.UID = types.UID("other-uid")

	theirHost, theirSecret := hostOverride(theirs, "host-10")

	c := clientfake.NewClientBuilder().WithScheme(s).
		WithObjects(mine, theirs, theirHost, theirSecret).Build()
	r := &VMImportSourceReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}

	// `mine` lists no hosts at all, so an ownership-blind prune would take the
	// other Source's Host with it.
	if err := r.pruneHosts(context.Background(), mine); err != nil {
		t.Fatalf("pruneHosts: %v", err)
	}

	name := types.NamespacedName{Namespace: "tenant-foo", Name: hostOverrideName("other", "host-10")}
	if err := c.Get(context.Background(), name, newObject(hostGVK)); err != nil {
		t.Errorf("another Source's Host was deleted: %v", err)
	}
	if err := c.Get(context.Background(), name, &corev1.Secret{}); err != nil {
		t.Errorf("another Source's credentials were deleted: %v", err)
	}
}
