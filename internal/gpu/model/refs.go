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

package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// ClaimRef is one entry of the gpu.AnnotationClaims list on a VMI:
// exactly one of Claim and ClaimTemplate is set.
type ClaimRef struct {
	// Claim names a GPUClaim in the VMI's namespace.
	Claim         string         `json:"claim,omitempty"`
	ClaimTemplate *ClaimTemplate `json:"claimTemplate,omitempty"`
}

// ClaimTemplate asks for a GPUClaim created at VMI start and deleted
// with the VMI.
type ClaimTemplate struct {
	ClassName string `json:"className"`
	Count     int32  `json:"count"`
}

// ParseClaimRefs reads the gpu.AnnotationClaims value and checks its
// shape. Claim and class names are not checked here; a malformed one is
// refused when the webhook looks it up.
func ParseClaimRefs(s string) ([]ClaimRef, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.DisallowUnknownFields()
	var refs []ClaimRef
	if err := dec.Decode(&refs); err != nil {
		return nil, fmt.Errorf("parse claim references: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("parse claim references: data after the list")
	}
	if len(refs) == 0 {
		return nil, errors.New("claim references: empty list")
	}
	seen := map[string]bool{}
	for i, r := range refs {
		switch {
		case (r.Claim == "") == (r.ClaimTemplate == nil):
			return nil, fmt.Errorf("claim reference %d: set exactly one of claim and claimTemplate", i)
		case r.ClaimTemplate != nil && r.ClaimTemplate.ClassName == "":
			return nil, fmt.Errorf("claim reference %d: claimTemplate needs className", i)
		case r.ClaimTemplate != nil && r.ClaimTemplate.Count < 1:
			return nil, fmt.Errorf("claim reference %d: claimTemplate count must be at least 1", i)
		case seen[r.Claim]:
			return nil, fmt.Errorf("claim reference %d: claim %s is referenced twice", i, r.Claim)
		}
		if r.Claim != "" {
			seen[r.Claim] = true
		}
	}
	return refs, nil
}

// FormatClaimRefs renders refs as the gpu.AnnotationClaims value.
func FormatClaimRefs(refs []ClaimRef) (string, error) {
	b, err := json.Marshal(refs)
	if err != nil {
		return "", fmt.Errorf("format claim references: %w", err)
	}
	return string(b), nil
}

// TemplateClaimName names the GPUClaim created for the claimTemplate
// entry at index of a VMI's references, so a repeated admission or a
// recreated VMI finds the same claim. A VMI name too long to take the
// suffix is cut short and followed by a hash of the full name.
func TemplateClaimName(vmi string, index int) string {
	suffix := "-gpu-" + strconv.Itoa(index)
	if len(vmi)+len(suffix) <= validation.DNS1123SubdomainMaxLength {
		return vmi + suffix
	}
	sum := sha256.Sum256([]byte(vmi))
	hash := hex.EncodeToString(sum[:])[:10]
	keep := validation.DNS1123SubdomainMaxLength - len(suffix) - len(hash) - 1
	return strings.TrimRight(vmi[:keep], "-.") + "-" + hash + suffix
}
