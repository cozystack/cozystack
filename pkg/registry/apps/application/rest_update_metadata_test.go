package application

import (
	"context"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

// TestUpdate_PreservesForeignHelmReleaseMetadata pins the metadata
// carry-over contract on the Update path. Update rebuilds the
// HelmRelease from the Application and issues a full-replace PUT, so
// without an explicit carry-over every update strips metadata owned by
// other controllers. The most harmful instance is helm-controller's
// finalizers.fluxcd.io finalizer: once stripped, a delete that lands
// before helm-controller re-adds it removes the HelmRelease without
// running `helm uninstall`, orphaning every resource of the release
// (VMs, databases, PVCs). A label patch immediately followed by a
// delete — a common client pattern — hits that window almost every
// time.
func TestUpdate_PreservesForeignHelmReleaseMetadata(t *testing.T) {
	existing := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "postgresql-mydb",
			Namespace:  "default",
			Finalizers: []string{"finalizers.fluxcd.io"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps.cozystack.io/v1alpha1",
				Kind:       "Tenant",
				Name:       "root",
				UID:        types.UID("owner-uid"),
			}},
			Labels: map[string]string{
				ApplicationKindLabel:               "PostgreSQL",
				ApplicationGroupLabel:              "apps.cozystack.io",
				ApplicationNameLabel:               "mydb",
				"kustomize.toolkit.fluxcd.io/name": "core",
				LabelPrefix + "stale":              "removed-by-this-update",
			},
			Annotations: map[string]string{
				"reconcile.fluxcd.io/requestedAt": "2026-06-12T00:00:00Z",
				AnnotationPrefix + "stale":        "removed-by-this-update",
			},
		},
	}
	r := newTestRESTWithSchemes(existing)

	app := &appsv1alpha1.Application{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps.cozystack.io/v1alpha1",
			Kind:       "PostgreSQL",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mydb",
			Namespace: "default",
			Labels:    map[string]string{"fresh": "yes"},
		},
	}

	ctx := request.WithNamespace(context.Background(), "default")
	if _, _, err := r.Update(
		ctx,
		"mydb",
		newDefaultUpdatedObjectInfo(app),
		nil,
		nil,
		false,
		&metav1.UpdateOptions{},
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &helmv2.HelmRelease{}
	if err := r.c.Get(ctx, client.ObjectKey{Namespace: "default", Name: "postgresql-mydb"}, got); err != nil {
		t.Fatalf("failed to fetch updated HelmRelease: %v", err)
	}

	if len(got.Finalizers) != 1 || got.Finalizers[0] != "finalizers.fluxcd.io" {
		t.Errorf("expected finalizers.fluxcd.io to be preserved, got %v", got.Finalizers)
	}
	if len(got.OwnerReferences) != 1 || got.OwnerReferences[0].UID != types.UID("owner-uid") {
		t.Errorf("expected ownerReferences to be preserved, got %v", got.OwnerReferences)
	}
	if got.Labels["kustomize.toolkit.fluxcd.io/name"] != "core" {
		t.Errorf("expected foreign label to be preserved, got labels %v", got.Labels)
	}
	if got.Annotations["reconcile.fluxcd.io/requestedAt"] != "2026-06-12T00:00:00Z" {
		t.Errorf("expected foreign annotation to be preserved, got annotations %v", got.Annotations)
	}
	if got.Labels[LabelPrefix+"fresh"] != "yes" {
		t.Errorf("expected new prefixed user label to be set, got labels %v", got.Labels)
	}
	if _, ok := got.Labels[LabelPrefix+"stale"]; ok {
		t.Errorf("expected stale prefixed label to be dropped, got labels %v", got.Labels)
	}
	if _, ok := got.Annotations[AnnotationPrefix+"stale"]; ok {
		t.Errorf("expected stale prefixed annotation to be dropped, got annotations %v", got.Annotations)
	}
	if got.Labels[ApplicationNameLabel] != "mydb" {
		t.Errorf("expected application metadata labels to remain, got labels %v", got.Labels)
	}
}

// TestCreate_DoesNotMutateSharedConfigLabels pins mergeMaps' allocation
// contract. Create and Update build the HelmRelease label set starting
// from r.releaseConfig.Labels, a long-lived map shared by every request
// for the kind, and then write the application metadata labels into the
// result in place. If mergeMaps returns one of its inputs instead of a
// fresh map — which the old implementation did whenever the other input
// was nil, e.g. for an Application with no labels — those in-place
// writes land on the shared config map: requests cross-contaminate
// (application.name varies per request) and concurrent map writes crash
// the apiserver.
func TestCreate_DoesNotMutateSharedConfigLabels(t *testing.T) {
	configLabels := map[string]string{"cozystack.io/ui": "true"}
	r := newTestRESTWithSchemes()
	r.releaseConfig.Labels = configLabels

	app := &appsv1alpha1.Application{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps.cozystack.io/v1alpha1",
			Kind:       "PostgreSQL",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nolabels",
			Namespace: "default",
			// No labels: the conversion yields a nil label map, the
			// trigger for mergeMaps returning the config map itself.
		},
	}

	ctx := request.WithNamespace(context.Background(), "default")
	if _, err := r.Create(ctx, app, nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(configLabels) != 1 || configLabels["cozystack.io/ui"] != "true" {
		t.Errorf("expected shared config labels to be untouched, got %v", configLabels)
	}
}
