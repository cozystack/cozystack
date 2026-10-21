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

package agent

// models names the NVIDIA device IDs of the platform's passthrough defaults
// (packages/core/platform/files/gpu-passthrough-defaults.yaml); a test keeps
// the two in step. The host carries no PCI ID database to read them from.
var models = map[string]string{
	"2335": "H200 SXM 141GB",
	"2330": "H100 SXM5 80GB",
	"2331": "H100 PCIe",
	"2321": "H100L 94GB",
	"26b9": "L40S",
	"26b5": "L40",
	"27b8": "L4",
	"20b2": "A100 SXM4 80GB",
	"20b0": "A100 SXM4 40GB",
	"20b5": "A100 PCIe 80GB",
	"20f1": "A100 PCIe 40GB",
	"20b7": "A30 PCIe",
	"2235": "A40",
	"2236": "A10",
	"1eb8": "T4",
	"1db1": "V100 SXM2 16GB",
	"1db5": "V100 SXM2 32GB",
	"1db4": "V100 PCIe 16GB",
	"1df6": "V100S PCIe 32GB",
}

// model falls back to <vendor>-<device>, the form of the per-model node
// labels.
func model(vendor, device string) string {
	if vendor == vendorNVIDIA {
		if m, ok := models[device]; ok {
			return m
		}
	}
	return vendor + "-" + device
}
