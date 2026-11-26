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

package reservation

import "k8s.io/apimachinery/pkg/api/resource"

// shippedPresetTable is cozy-lib's resource preset table
// (packages/library/cozy-lib/templates/_resourcepresets.tpl), cpu and memory
// only: a reservation never counts ephemeral storage. The charts render from
// the template and the apiserver cannot read it at run time, so the two are
// kept equal by TestShippedPresetsMatchTheChartTable rather than by hand.
var shippedPresetTable = map[string][2]string{
	"t1.nano":    {"250m", "128Mi"},
	"t1.micro":   {"500m", "256Mi"},
	"t1.small":   {"1", "512Mi"},
	"t1.medium":  {"2", "1Gi"},
	"t1.large":   {"4", "2Gi"},
	"t1.xlarge":  {"8", "4Gi"},
	"t1.2xlarge": {"16", "8Gi"},
	"t1.4xlarge": {"32", "16Gi"},
	"c1.nano":    {"250m", "256Mi"},
	"c1.micro":   {"500m", "512Mi"},
	"c1.small":   {"1", "1Gi"},
	"c1.medium":  {"2", "2Gi"},
	"c1.large":   {"4", "4Gi"},
	"c1.xlarge":  {"8", "8Gi"},
	"c1.2xlarge": {"16", "16Gi"},
	"c1.4xlarge": {"32", "32Gi"},
	"s1.nano":    {"250m", "512Mi"},
	"s1.micro":   {"500m", "1Gi"},
	"s1.small":   {"1", "2Gi"},
	"s1.medium":  {"2", "4Gi"},
	"s1.large":   {"4", "8Gi"},
	"s1.xlarge":  {"8", "16Gi"},
	"s1.2xlarge": {"16", "32Gi"},
	"s1.4xlarge": {"32", "64Gi"},
	"u1.nano":    {"250m", "1Gi"},
	"u1.micro":   {"500m", "2Gi"},
	"u1.small":   {"1", "4Gi"},
	"u1.medium":  {"2", "8Gi"},
	"u1.large":   {"4", "16Gi"},
	"u1.xlarge":  {"8", "32Gi"},
	"u1.2xlarge": {"16", "64Gi"},
	"u1.4xlarge": {"32", "128Gi"},
	"m1.nano":    {"250m", "2Gi"},
	"m1.micro":   {"500m", "4Gi"},
	"m1.small":   {"1", "8Gi"},
	"m1.medium":  {"2", "16Gi"},
	"m1.large":   {"4", "32Gi"},
	"m1.xlarge":  {"8", "64Gi"},
	"m1.2xlarge": {"16", "128Gi"},
	"m1.4xlarge": {"32", "256Gi"},
	"nano":       {"250m", "128Mi"},
	"micro":      {"500m", "256Mi"},
	"small":      {"1", "512Mi"},
	"medium":     {"1", "1Gi"},
	"large":      {"2", "2Gi"},
	"xlarge":     {"4", "4Gi"},
	"2xlarge":    {"8", "8Gi"},
}

// ShippedPresets returns the preset table the charts render from, as
// reservations. Each call returns a fresh map the caller may keep.
func ShippedPresets() map[string]ResourceList {
	out := make(map[string]ResourceList, len(shippedPresetTable))
	for name, row := range shippedPresetTable {
		out[name] = ResourceList{
			KeyCPU:    resource.MustParse(row[0]),
			KeyMemory: resource.MustParse(row[1]),
		}
	}
	return out
}
