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

// Package gpu holds the names the GPU components of cozystack-controller
// and cozystack-api share. The pure capacity, budget and tenant logic
// lives in package model.
//
// # Class entries
//
// A VM or a worker pool names a GPUClass and a count in its gpus list.
// The chart renders the entries onto the VMI template as AnnotationGPUs,
// next to LabelGPUReference, which KubeVirt copies to the VMI and its
// virt-launcher pod:
//
//	cozystack.io/gpus: '[{"className":"l40s-24c","count":2}]'
//
// # Starting a VMI
//
// A mutating webhook on VMI creation resolves each entry to its class and
// writes one gpus entry per unit, named with VMIGPUNamePrefix and
// requesting the class's resource name. The entries have to be complete
// at admission, since KubeVirt renders the launcher pod's requests from
// the VMI spec and nothing else can change that spec afterwards. The
// webhook refuses the VMI, naming the class, when an entry:
//
//   - names a class that does not exist, is being deleted, or lacks unique
//     ownership of its exact requested resource name;
//   - is a vGPU class next to another vGPU class, or takes the VMI's units
//     of its class above model.EffectiveMaxCount;
//   - names a vGPU class in a namespace without the vGPU entitlement
//     (model.Entitled);
//   - names a class whose resource name the KubeVirt permittedHostDevices
//     table leaves out, when the table is set (model.PermittedResourceNames),
//     since KubeVirt would accept the VMI and leave it Pending with no pod.
//
// Catalog-name ownership is acknowledged in status.ownedResourceName for
// the object's UID, independently of Ready, license state and generation.
// A serialized status writer retains that name until a successful
// resource-version-checked status transition acquires another name; an
// unacknowledged rename does not release the old name. This reserves a
// catalog name, not GPU capacity. Creation time and name select candidates
// only for unreserved names (model.ResourceNameCandidates).
//
// Admission requires requested and owned names to match uniquely in one
// fresh class snapshot (model.ConfirmedResourceNames). Missing ownership
// is pending; duplicate reservations fail closed without clearing the
// acknowledgements or choosing a creation-time winner. Conditions are not
// ownership evidence. This check is not atomic with future status writes:
// leader election is not fencing, and overlapping writers can produce
// duplicate ownership that the next fresh read must refuse.
//
// The webhook also refuses a labelled VMI that carries GPU entries of its own. It
// reads classes and the KubeVirt CR from the API server, not from a cache,
// writes no object besides its patch, and checks no capacity: a VM short
// of a free unit waits in Scheduling like a VM short of CPU.
//
// A second mutating webhook adds the classes' tolerations to the
// virt-launcher pod at creation, and to the temporary pod KubeVirt creates
// to bind WaitForFirstConsumer volumes. They go on the pod and not on the
// VMI, because under vmRolloutStrategy LiveUpdate KubeVirt resets a VMI's
// tolerations to the VM template's on every sync. Both webhooks select on
// LabelGPUReference and fail closed.
//
// # Licenses
//
// For each vGPU class with a license, the controller copies the hoster's
// Secret from LicenseSecretNamespace into every namespace whose
// LabelNamespaceVGPU is LabelValueTrue, named by LicenseSecretName and
// labelled LabelGPUClass, with the keys LicenseTokenKey and
// LicenseGriddConfKey. It deletes a namespace's copies when the label
// stops being LabelValueTrue, and a class's copies when the class or its
// license goes. While the hoster's Secret is missing or lacks
// LicenseTokenKey, it keeps the copies it has, writes no new ones and sets
// LicenseSecretMissing on the class. It touches no Secret without
// LabelGPUClass.
//
// The VMI webhook adds a secret volume for the copy of the VMI's licensed
// vGPU class, with LicenseVolumeLabel, and a disk for it after the VMI's
// other disks with no bootOrder. A missing copy is not refused: kubelet
// keeps the launcher pod from starting until the copy exists.
//
// # Budgets
//
// A tenant declares GPU budgets in resourceQuotas under model.ClassQuotaKey
// per class and QuotaKeyTotal over every class. They are not resource
// names a pod requests, so the tenant chart keeps them out of the
// namespace ResourceQuota, and they are checked when a VM or a pool is
// written, against the class entries it declares.
package gpu
