// SPDX-License-Identifier: Apache-2.0

package migrationcontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	migrationv1alpha1 "github.com/cozystack/cozystack/api/migration/v1alpha1"
)

// The VM picker in the console needs to offer machine names while writing
// managed-object references, and only Forklift's inventory knows the mapping.
// The aggregated API that serves dropdowns lists Kubernetes objects with a
// dynamic client and has no way to speak HTTPS to the inventory, nor should it
// grow one: that would put the source's credentials path in a second component.
// So this controller — which already holds an inventory client — publishes the
// list, and the option provider reads it back as an ordinary object.
//
// The list lives in its own ConfigMap rather than on the Source's status. A
// status carrying thousands of entries is rewritten on every refresh and wakes
// everything watching the Source; a separate object is read only by the thing
// that needs it, and an ownerReference still has it deleted with the Source.
// maxPublishedVMs bounds the object. A vCenter with more machines than this is
// past the point where a dropdown is the right interface anyway, and an
// unbounded write is not something to put in etcd on a timer.
const maxPublishedVMs = 5000

// publishInventory refreshes the source's VM list. A failure here never fails
// the Source: the connection is what the Source reports on, and an inventory
// that is briefly unavailable should degrade the picker, not the status.
func (r *VMImportSourceReconciler) publishInventory(ctx context.Context, src *migrationv1alpha1.VMImportSource) error {
	if r.Inventory == nil {
		return nil
	}

	provider := newObject(providerGVK)
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: src.Namespace, Name: sourceProviderName(src.Name),
	}, provider); err != nil {
		return err
	}
	uid := string(provider.GetUID())
	if uid == "" {
		return fmt.Errorf("source Provider %s/%s has no UID yet", src.Namespace, sourceProviderName(src.Name))
	}

	vms, err := r.Inventory.VMs(ctx, uid)
	if err != nil {
		return err
	}

	// Sorted by name so the dropdown is stable across refreshes and the
	// truncation, when it happens, is at least predictable.
	sort.Slice(vms, func(i, j int) bool { return vms[i].Name < vms[j].Name })
	truncated := len(vms) > maxPublishedVMs
	if truncated {
		vms = vms[:maxPublishedVMs]
	}

	published := make([]migrationv1alpha1.PublishedVM, 0, len(vms))
	for _, vm := range vms {
		published = append(published, migrationv1alpha1.PublishedVM{ID: vm.ID, Name: vm.Name, Path: vm.Path})
	}
	encoded, err := json.Marshal(published)
	if err != nil {
		return err
	}

	cm := &corev1.ConfigMap{}
	cm.Name = migrationv1alpha1.InventoryConfigMapName(src.Name)
	cm.Namespace = src.Namespace
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		if cm.Annotations == nil {
			cm.Annotations = map[string]string{}
		}
		if truncated {
			cm.Annotations[migrationv1alpha1.InventoryTruncatedAnnotation] = "true"
		} else {
			delete(cm.Annotations, migrationv1alpha1.InventoryTruncatedAnnotation)
		}
		cm.Data = map[string]string{migrationv1alpha1.InventoryVMsKey: string(encoded)}
		return controllerutil.SetControllerReference(src, cm, r.Scheme)
	})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// publishedVMs decodes what publishInventory wrote. It lives here so the writer
// and the reader cannot drift apart.
func publishedVMs(cm *corev1.ConfigMap) ([]migrationv1alpha1.PublishedVM, error) {
	raw, ok := cm.Data[migrationv1alpha1.InventoryVMsKey]
	if !ok || raw == "" {
		return nil, nil
	}
	var vms []migrationv1alpha1.PublishedVM
	if err := json.Unmarshal([]byte(raw), &vms); err != nil {
		return nil, err
	}
	return vms, nil
}
