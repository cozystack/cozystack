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
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// DeviceRef is one entry of the gpu.AnnotationDevices list on a VMI:
// exactly one of Device and DeviceTemplate is set.
type DeviceRef struct {
	// Device names a GPUDevice in the VMI's namespace.
	Device         string          `json:"device,omitempty"`
	DeviceTemplate *DeviceTemplate `json:"deviceTemplate,omitempty"`
}

// DeviceTemplate asks for a GPUDevice created at VMI start and deleted
// with the VMI.
type DeviceTemplate struct {
	ClassName string `json:"className"`
	Count     int32  `json:"count"`
}

// ParseDeviceRefs reads and validates the gpu.AnnotationDevices value.
func ParseDeviceRefs(s string) ([]DeviceRef, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.DisallowUnknownFields()
	var refs []DeviceRef
	if err := dec.Decode(&refs); err != nil {
		return nil, fmt.Errorf("parse device references: %w", err)
	}
	if len(refs) == 0 {
		return nil, errors.New("device references: empty list")
	}
	seen := map[string]bool{}
	for i, r := range refs {
		switch {
		case (r.Device == "") == (r.DeviceTemplate == nil):
			return nil, fmt.Errorf("device reference %d: set exactly one of device and deviceTemplate", i)
		case r.DeviceTemplate != nil && r.DeviceTemplate.ClassName == "":
			return nil, fmt.Errorf("device reference %d: deviceTemplate needs className", i)
		case r.DeviceTemplate != nil && r.DeviceTemplate.Count < 1:
			return nil, fmt.Errorf("device reference %d: deviceTemplate count must be at least 1", i)
		case seen[r.Device]:
			return nil, fmt.Errorf("device reference %d: device %s is referenced twice", i, r.Device)
		}
		if r.Device != "" {
			seen[r.Device] = true
		}
	}
	return refs, nil
}

// FormatDeviceRefs renders refs as the gpu.AnnotationDevices value.
func FormatDeviceRefs(refs []DeviceRef) (string, error) {
	b, err := json.Marshal(refs)
	if err != nil {
		return "", fmt.Errorf("format device references: %w", err)
	}
	return string(b), nil
}

// TemplateDeviceName names the GPUDevice created for the deviceTemplate
// entry at index of a VMI's references, so a repeated admission or a
// recreated VMI finds the same device.
func TemplateDeviceName(vmi string, index int) string {
	return vmi + "-gpu-" + strconv.Itoa(index)
}
