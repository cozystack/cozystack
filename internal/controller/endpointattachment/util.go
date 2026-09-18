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
	"crypto/sha256"
	"encoding/hex"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/localsdn/v1alpha1"
)

var attachmentGVK = cozyv1alpha1.GroupVersion.WithKind("EndpointAttachment")

func controllerOwnerReference(ea *cozyv1alpha1.EndpointAttachment) metav1.OwnerReference {
	return *metav1.NewControllerRef(ea, attachmentGVK)
}

// ownerLabelValue bounds the attachment name to a label value. A custom
// resource name may run to 253 characters and a label value to 63; past that
// the name is truncated and suffixed with a hash of the whole name, so two
// long names that share a prefix still label apart.
func ownerLabelValue(name string) string {
	if len(name) <= validation.LabelValueMaxLength {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	suffix := hex.EncodeToString(sum[:4])
	prefix := strings.TrimRight(name[:validation.LabelValueMaxLength-len(suffix)-1], "-.")
	return prefix + "-" + suffix
}

// maxGenerateNamePrefix is the longest base the apiserver keeps before
// appending its five random characters to a generateName.
const maxGenerateNamePrefix = validation.DNS1035LabelMaxLength - 5

// childGenerateName derives the generateName prefix of the objects rendered
// for an attachment. A Service name is a DNS-1035 label, so the prefix may
// hold neither the dots nor the leading digit an attachment name allows, and
// the apiserver validates the prefix itself, so it must fit the label length.
func childGenerateName(name string) string {
	prefix := strings.ReplaceAll(name, ".", "-")
	if prefix[0] >= '0' && prefix[0] <= '9' {
		prefix = "ea-" + prefix
	}
	if len(prefix) > maxGenerateNamePrefix-1 {
		prefix = prefix[:maxGenerateNamePrefix-1]
	}
	return prefix + "-"
}

// isControlledBy reports whether obj's controller owner reference points at
// this attachment's UID. Labels are never consulted: they are mutable and
// can collide, the UID cannot.
func isControlledBy(obj metav1.Object, ea *cozyv1alpha1.EndpointAttachment) bool {
	ref := metav1.GetControllerOf(obj)
	return ref != nil && ref.UID == ea.UID
}

func isNoKindMatch(err error) bool {
	return meta.IsNoMatchError(err)
}

func copyMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func equalStringMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func equalBoolPtr(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func equalStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ingressAddresses lists the addresses a LoadBalancer Service has been
// assigned.
func ingressAddresses(svc *corev1.Service) []string {
	var out []string
	for _, i := range svc.Status.LoadBalancer.Ingress {
		if i.IP != "" {
			out = append(out, i.IP)
		}
	}
	return out
}
