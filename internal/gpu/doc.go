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

// Package gpu holds what the GPU components of cozystack-controller,
// cozystack-api and the node agent share: label, annotation, finalizer and
// quota keys, the settings ConfigMap, and the feature Config. The pure
// placement, budget and capacity logic lives in package model.
//
// # Binding
//
// The binder runs in the controller's leader and is the only writer of a
// GPUClaim's status.phase, status.nodeName, status.boundAt and
// status.rejectedNodes. Every check of capacity and budget that decides a
// bind runs again in its single loop, so two orders racing on one unit
// cannot both win.
//
// # Starting a VMI
//
// The VMI admission webhook runs in every replica, binds, moves and
// creates no claim, and waits for nothing. For a VMI with AnnotationClaims
// it resolves the claim references and checks each claimTemplate entry
// against its class, maxCount and the vGPU entitlement; budgets and
// capacity are the controller's, so a template claim that does not fit
// keeps the VM waiting at start rather than refused. On a dry run the
// webhook stops there and writes nothing. Otherwise it:
//
//  1. writes status.attachedTo on every held claim of the VMI, with
//     optimistic concurrency, so no other VMI can take one,
//  2. adds to the VMI one gpus entry per GPU unit, held and template
//     claims alike, named with VMIGPUNamePrefix and requesting the class's
//     resource name, since KubeVirt renders the launcher pod's requests
//     from the VMI spec and the spec cannot change afterwards.
//
// The webhook can be called more than once for one VMI, so both steps are
// idempotent. An attachment whose VMI has not appeared after
// Config.AttachmentGrace, because the VMI was refused by a later admission
// step, is cleared by the controller.
//
// The pod admission webhook adds SchedulingGateClaims and the tolerations
// of the claims' classes to the virt-launcher pod at creation, and to the
// temporary pod KubeVirt creates for WaitForFirstConsumer volumes, but not
// to migration target pods. It adds the gate only when it is absent,
// since a reinvocation would otherwise duplicate it.
//
// For a gated pod, the controller creates the VMI's template claims,
// named by TemplateClaimName with LabelTemplateClaim and owned by the
// VMI, under the budget and capacity checks of any order, then gathers
// the whole set onto one node that every class of the set serves,
// skipping the nodes in the claims' status.rejectedNodes, and finally,
// in one pod update, appends a metadata.name requirement for that node
// to every required node selector term, or adds one term with it when
// the pod has none, and removes the gate. The term count of a gated pod
// cannot change, and the API server lets node affinity change only while
// the pod being updated is still gated, so the pin and the ungating
// cannot be split. A set held whole on a cordoned node whose only reason
// to leave is the cordon, with no room elsewhere or pinned by a Stay
// class, is pinned where it is. A set that cannot be placed keeps the
// pod gated; its claims carry a condition and the VMI gets a Warning event
// with the same reason.
//
// model.Snapshot.CheckOrder judges one order. To judge a set, such as a
// VMI's template claims, add each claim to the snapshot as Bound before
// checking the next.
//
// A pin cannot be undone. When the scheduler refuses a pinned pod on a
// node that is Ready, not cordoned and still serves the set, for lack of
// resources, the controller deletes the pod and records the node in
// status.rejectedNodes of every claim of the set; the node is not picked
// for the set again until its allocatable, its taints or its Ready
// condition change, or the entry's until passes. A pinned pod waiting on
// a cordoned or not Ready node, or under a Stay pin, is not such a
// refusal.
//
// Status is a subresource that no tenant role grants, so a tenant cannot
// forge an attachment, a placement or a rejection.
//
// # Settings ConfigMap
//
// The controller writes SettingsConfigMap in SettingsNamespace:
//
//	data:
//	  resourceNames: "nvidia.com/GH100_H100,nvidia.com/NVIDIA_L40S-24C"
//	  rawResourceNames: "allowed"
//
// resourceNames is the effective GPU resource-name set (model.ResourceNames,
// formatted by model.FormatResourceNames); rawResourceNames is a
// RawNamesPolicy. The ValidatingAdmissionPolicies take it as paramRef, and
// cozystack-api reads it for the option sources and the tenant quota gate.
// The controller owns the object; nothing else writes it.
package gpu
