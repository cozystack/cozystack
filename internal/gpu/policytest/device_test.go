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

package policytest

import (
	"slices"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apiserver/pkg/authentication/user"

	"github.com/cozystack/cozystack/internal/gpu"
)

const (
	devicePolicyFile = "gpu-device-tamper-policy.yaml"
	deviceTamper     = "cozystack-gpu-device-tamper"
)

func device(finalizers []string, labels map[string]string) *unstructured.Unstructured {
	u := obj(deviceGVK.GroupVersion().String(), deviceGVK.Kind, "tenant-acme", "train-0", map[string]any{
		"spec": map[string]any{"className": "l40s-24c", "count": int64(1)},
	})
	if finalizers != nil {
		u.SetFinalizers(finalizers)
	}
	if labels != nil {
		u.SetLabels(labels)
	}
	return u
}

func TestDeviceTamperMatch(t *testing.T) {
	p := get(t, devicePolicyFile, deviceTamper).policy
	rules := p.Spec.MatchConstraints.ResourceRules
	if len(rules) != 1 {
		t.Fatalf("want one resource rule, got %d", len(rules))
	}
	r := rules[0]
	if !slices.Equal(r.APIGroups, []string{gpu.Group}) || !slices.Equal(r.Resources, []string{"gpudevices"}) {
		t.Errorf("rule = %+v, want gpudevices in %s", r, gpu.Group)
	}
	ops := []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update}
	if !slices.Equal(r.Operations, ops) {
		t.Errorf("operations = %v, want %v", r.Operations, ops)
	}
}

func TestDeviceTamper(t *testing.T) {
	v := compile(t, get(t, devicePolicyFile, deviceTamper).policy)
	held := []string{gpu.FinalizerDevice}
	mark := map[string]string{gpu.LabelTemplateDevice: gpu.LabelValueTrue}
	gcSA := &user.DefaultInfo{Name: "system:serviceaccount:kube-system:generic-garbage-collector", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:kube-system", "system:authenticated"}}
	otherPlatformSA := &user.DefaultInfo{Name: "system:serviceaccount:cozy-system:cozystack-api", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:cozy-system", "system:authenticated"}}
	impostorSA := &user.DefaultInfo{Name: "system:serviceaccount:tenant-acme:cozystack-controller", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:tenant-acme", "system:authenticated"}}
	kcm := &user.DefaultInfo{Name: "system:kube-controller-manager", Groups: []string{"system:authenticated"}}

	cases := []struct {
		name     string
		old, new *unstructured.Unstructured
		user     user.Info
		denied   bool
	}{
		{name: "tenant orders a device", new: device(nil, nil), user: tenantUser},
		{name: "tenant labels a device for a pool", old: device(held, nil), new: device(held, map[string]string{"pool": "inference"}), user: tenantUser},
		{name: "tenant edits a held device without touching finalizers", old: device(held, nil), new: device(held, nil), user: tenantUser},
		{name: "tenant removes the device finalizer", old: device(held, nil), new: device([]string{}, nil), user: tenantUser, denied: true},
		{name: "tenant drops the finalizer list", old: device(held, nil), new: device(nil, nil), user: tenantUser, denied: true},
		{name: "tenant adds a finalizer", old: device(held, nil), new: device(append(slices.Clone(held), "example.com/keep"), nil), user: tenantUser, denied: true},
		{name: "tenant orders a device with the controller's finalizer", new: device(held, nil), user: tenantUser, denied: true},
		{name: "tenant orders a device with a finalizer of its own", new: device([]string{"example.com/keep"}, nil), user: tenantUser, denied: true},
		{name: "tenant service account removes the device finalizer", old: device(held, nil), new: device([]string{}, nil), user: tenantNamespaced, denied: true},
		{name: "tenant reorders finalizers", old: device([]string{gpu.FinalizerDevice, "orphan"}, nil), new: device([]string{"orphan", gpu.FinalizerDevice}, nil), user: tenantUser},
		// The API server and the garbage collector own these two.
		{name: "foreground deletion finalizer comes and goes", old: device(held, nil), new: device(append(slices.Clone(held), "foregroundDeletion"), nil), user: tenantUser},
		{name: "orphan finalizer removed", old: device(append(slices.Clone(held), "orphan"), nil), new: device(held, nil), user: tenantUser},
		{name: "controller adds the device finalizer", old: device(nil, nil), new: device(held, nil), user: controllerSA},
		{name: "controller removes the device finalizer", old: device(held, nil), new: device([]string{}, nil), user: controllerSA},
		{name: "garbage collector removes a finalizer", old: device(append(slices.Clone(held), "foregroundDeletion"), nil), new: device(held, nil), user: gcSA},
		{name: "controller manager removes a finalizer", old: device(held, nil), new: device([]string{}, nil), user: kcm},
		{name: "cluster admin removes the device finalizer", old: device(held, nil), new: device([]string{}, nil), user: clusterAdmin},
		{name: "tenant orders a device with the template mark", new: device(nil, mark), user: tenantUser, denied: true},
		{name: "tenant adds the template mark", old: device(held, nil), new: device(held, mark), user: tenantUser, denied: true},
		{name: "tenant removes the template mark", old: device(held, mark), new: device(held, map[string]string{}), user: tenantUser, denied: true},
		{name: "tenant changes the template mark", old: device(held, mark), new: device(held, map[string]string{gpu.LabelTemplateDevice: "false"}), user: tenantUser, denied: true},
		{name: "tenant edits other labels of a template device", old: device(held, mark), new: device(held, map[string]string{gpu.LabelTemplateDevice: gpu.LabelValueTrue, "team": "ml"}), user: tenantUser},
		{name: "webhook creates a template device", new: device(nil, mark), user: controllerSA},
		// Platform callers are trusted by namespace group, not by name.
		{name: "another cozy-system service account removes the device finalizer", old: device(held, nil), new: device([]string{}, nil), user: otherPlatformSA},
		{name: "tenant service account named like the controller removes the device finalizer", old: device(held, nil), new: device([]string{}, nil), user: impostorSA, denied: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			denials := evaluate(t, v, request{gvk: deviceGVK, resource: "gpudevices", namespace: "tenant-acme", obj: tc.new, old: tc.old, user: tc.user})
			if got := len(denials) > 0; got != tc.denied {
				t.Fatalf("denied = %v, want %v (%v)", got, tc.denied, denials)
			}
			for _, m := range denials {
				if !strings.Contains(m, "GPUDevice train-0") {
					t.Errorf("message must name the device, got %q", m)
				}
			}
		})
	}
}
