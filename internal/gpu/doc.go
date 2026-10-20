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
// The binder runs in the leader and is the only writer of a GPUDevice's
// status.phase, status.nodeName and status.boundAt. Every check of capacity
// and budget that decides a bind runs again in its single loop, so two
// admissions racing on one unit cannot both win.
//
// The VMI admission webhook runs in every replica and never binds. It first
// runs every refusal check, including budget and entitlement of the
// template devices; on a dry run it stops there and writes nothing.
// Otherwise, for a VMI, it:
//
//  1. creates the template devices,
//  2. writes status.attachedTo on every device of the VMI's set, held and
//     template, with optimistic concurrency, so no other VMI can take one,
//  3. writes the same status.bindRequest (ID, VMI, Devices, RequestedAt) on
//     each of them, with an ID unique to this admission,
//  4. waits on its informer until every device of the set carries that ID
//     with a Result, and admits the VMI only if all of them are Succeeded
//     and share one node.
//
// model.Snapshot.CheckOrder judges one order. To judge a set, such as a
// VMI's template devices, add each admitted device to the snapshot as Bound
// before checking the next.
//
// The binder, for a request whose devices all carry the same ID, either
// places the whole set on one eligible node with room for all of it, moving
// or binding devices as needed, and writes Result Succeeded on each; or
// changes no placement and writes Result Refused with a Message that names
// each device and its node. Two exceptions answer Succeeded with the set
// left where it is, both for a set without template devices still to place
// that is held entirely on one cordoned node: under Move when the cordon is
// the node's only problem and no other node has room for the set, and
// whenever a device of a Stay class is among it, unless a member has lost
// capacity there or the node is gone or no longer serves a member's class.
// The VMI is pinned there and waits for the cordon to lift. A partial
// request, where some device of Devices lacks the ID, is not acted on. A
// move that has started finishes even if the webhook stops waiting.
//
// The webhook can be called more than once for one VMI, so every step is
// idempotent: a repeated call finds its template devices and its
// attachments already in place, and admits without a new request when every
// device of the set carries a Succeeded request for that VMI on one node.
//
// When the webhook refuses or times out, it clears what it wrote. When it
// cannot (it crashed, or the VMI was refused by a later admission step),
// the controller clears an attachment and a request whose VMI has not
// appeared after Config.AttachmentGrace, and deletes such template devices,
// as well as a template device that has no attachment and is older than
// Config.AttachmentGrace.
//
// Status is a subresource that no tenant role grants, so a tenant cannot
// forge an attachment or a request.
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
