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

package registry

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ClientCreateOptions converts API create options for the controller-runtime client.
func ClientCreateOptions(options *metav1.CreateOptions) *client.CreateOptions {
	if options == nil {
		return &client.CreateOptions{}
	}
	raw := options.DeepCopy()
	return &client.CreateOptions{
		DryRun:          append([]string(nil), raw.DryRun...),
		FieldManager:    raw.FieldManager,
		FieldValidation: raw.FieldValidation,
		Raw:             raw,
	}
}

// CreateOptionsFromUpdate converts update options for a force-allow-create create.
func CreateOptionsFromUpdate(options *metav1.UpdateOptions) *metav1.CreateOptions {
	if options == nil {
		return &metav1.CreateOptions{}
	}
	return &metav1.CreateOptions{
		DryRun:          options.DryRun,
		FieldManager:    options.FieldManager,
		FieldValidation: options.FieldValidation,
	}
}

// ClientCreateOptionsFromUpdate converts force-create options for the controller-runtime client.
func ClientCreateOptionsFromUpdate(options *metav1.UpdateOptions) *client.CreateOptions {
	return ClientCreateOptions(CreateOptionsFromUpdate(options))
}

// ClientUpdateOptions converts API update options for the controller-runtime client.
func ClientUpdateOptions(options *metav1.UpdateOptions) *client.UpdateOptions {
	if options == nil {
		return &client.UpdateOptions{}
	}
	raw := options.DeepCopy()
	return &client.UpdateOptions{
		DryRun:          append([]string(nil), raw.DryRun...),
		FieldManager:    raw.FieldManager,
		FieldValidation: raw.FieldValidation,
		Raw:             raw,
	}
}

// ClientUpdateOptionsFromPatch converts patch options for a follow-up update.
func ClientUpdateOptionsFromPatch(options *metav1.PatchOptions) *client.UpdateOptions {
	if options == nil {
		return &client.UpdateOptions{}
	}
	return ClientUpdateOptions(&metav1.UpdateOptions{
		DryRun:          options.DryRun,
		FieldManager:    options.FieldManager,
		FieldValidation: options.FieldValidation,
	})
}

// ClientPatchOptions converts API patch options for the controller-runtime client.
func ClientPatchOptions(options *metav1.PatchOptions) *client.PatchOptions {
	if options == nil {
		return &client.PatchOptions{}
	}
	raw := options.DeepCopy()
	return &client.PatchOptions{
		DryRun:          append([]string(nil), raw.DryRun...),
		Force:           raw.Force,
		FieldManager:    raw.FieldManager,
		FieldValidation: raw.FieldValidation,
		Raw:             raw,
	}
}

// ClientDeleteOptions converts API delete options for the controller-runtime client.
func ClientDeleteOptions(options *metav1.DeleteOptions) *client.DeleteOptions {
	if options == nil {
		return &client.DeleteOptions{}
	}
	raw := options.DeepCopy()
	return &client.DeleteOptions{
		GracePeriodSeconds: raw.GracePeriodSeconds,
		Preconditions:      raw.Preconditions,
		PropagationPolicy:  raw.PropagationPolicy,
		Raw:                raw,
		DryRun:             append([]string(nil), raw.DryRun...),
	}
}
