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

package gpu

import "time"

// Config is the GPU feature configuration cozystack-controller takes from
// its flags.
type Config struct {
	// Enabled turns on everything that watches KubeVirt objects: binder
	// placement, the VMI and pod webhooks, VMI garbage collection. The
	// gpu.cozystack.io lifecycle paths (finalizers, usage-record cleanup)
	// run regardless, so disabling the feature never hangs a deletion.
	Enabled bool

	// ResourceNames is the hoster's gpu.resourceNames list. The effective
	// set (model.ResourceNames) adds the classes' names, the names node
	// agents report and the NVIDIA (10DE) and mdev names of the KubeVirt
	// permittedHostDevices table.
	ResourceNames []string

	// RawResourceNames is the hoster's gpu.rawResourceNames; empty means
	// RawNamesAllowed.
	RawResourceNames RawNamesPolicy

	// LostCapacityGrace is how long a node may serve fewer units of a
	// resource than its devices hold, or stay NotReady, before it has lost
	// capacity. kubelet reports zero allocatable after every restart, so it
	// must outlast a plugin re-registration.
	LostCapacityGrace time.Duration

	// AttachmentGrace is how long an attachment the VMI webhook recorded
	// may wait for its VMI to appear. It must exceed the API server request
	// timeout.
	AttachmentGrace time.Duration
}
