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

package presets

// Preset describes chart sizing; Replacement is nonempty for deprecated names.
type Preset struct {
	Name        string
	CPU         string
	Memory      string
	Replacement string
}

var table = []Preset{
	{Name: "t1.nano", CPU: "250m", Memory: "128Mi"},
	{Name: "t1.micro", CPU: "500m", Memory: "256Mi"},
	{Name: "t1.small", CPU: "1", Memory: "512Mi"},
	{Name: "t1.medium", CPU: "2", Memory: "1Gi"},
	{Name: "t1.large", CPU: "4", Memory: "2Gi"},
	{Name: "t1.xlarge", CPU: "8", Memory: "4Gi"},
	{Name: "t1.2xlarge", CPU: "16", Memory: "8Gi"},
	{Name: "t1.4xlarge", CPU: "32", Memory: "16Gi"},
	{Name: "c1.nano", CPU: "250m", Memory: "256Mi"},
	{Name: "c1.micro", CPU: "500m", Memory: "512Mi"},
	{Name: "c1.small", CPU: "1", Memory: "1Gi"},
	{Name: "c1.medium", CPU: "2", Memory: "2Gi"},
	{Name: "c1.large", CPU: "4", Memory: "4Gi"},
	{Name: "c1.xlarge", CPU: "8", Memory: "8Gi"},
	{Name: "c1.2xlarge", CPU: "16", Memory: "16Gi"},
	{Name: "c1.4xlarge", CPU: "32", Memory: "32Gi"},
	{Name: "s1.nano", CPU: "250m", Memory: "512Mi"},
	{Name: "s1.micro", CPU: "500m", Memory: "1Gi"},
	{Name: "s1.small", CPU: "1", Memory: "2Gi"},
	{Name: "s1.medium", CPU: "2", Memory: "4Gi"},
	{Name: "s1.large", CPU: "4", Memory: "8Gi"},
	{Name: "s1.xlarge", CPU: "8", Memory: "16Gi"},
	{Name: "s1.2xlarge", CPU: "16", Memory: "32Gi"},
	{Name: "s1.4xlarge", CPU: "32", Memory: "64Gi"},
	{Name: "u1.nano", CPU: "250m", Memory: "1Gi"},
	{Name: "u1.micro", CPU: "500m", Memory: "2Gi"},
	{Name: "u1.small", CPU: "1", Memory: "4Gi"},
	{Name: "u1.medium", CPU: "2", Memory: "8Gi"},
	{Name: "u1.large", CPU: "4", Memory: "16Gi"},
	{Name: "u1.xlarge", CPU: "8", Memory: "32Gi"},
	{Name: "u1.2xlarge", CPU: "16", Memory: "64Gi"},
	{Name: "u1.4xlarge", CPU: "32", Memory: "128Gi"},
	{Name: "m1.nano", CPU: "250m", Memory: "2Gi"},
	{Name: "m1.micro", CPU: "500m", Memory: "4Gi"},
	{Name: "m1.small", CPU: "1", Memory: "8Gi"},
	{Name: "m1.medium", CPU: "2", Memory: "16Gi"},
	{Name: "m1.large", CPU: "4", Memory: "32Gi"},
	{Name: "m1.xlarge", CPU: "8", Memory: "64Gi"},
	{Name: "m1.2xlarge", CPU: "16", Memory: "128Gi"},
	{Name: "m1.4xlarge", CPU: "32", Memory: "256Gi"},
	{Name: "nano", CPU: "250m", Memory: "128Mi"},
	{Name: "micro", CPU: "500m", Memory: "256Mi"},
	{Name: "small", CPU: "1", Memory: "512Mi"},
	{Name: "medium", CPU: "1", Memory: "1Gi"},
	{Name: "large", CPU: "2", Memory: "2Gi"},
	{Name: "xlarge", CPU: "4", Memory: "4Gi"},
	{Name: "2xlarge", CPU: "8", Memory: "8Gi"},
}

// Table provides presets in series/size order, followed by legacy names.
// Each call owns its slice so callers cannot change subsequent responses.
func Table() []Preset {
	out := append([]Preset(nil), table...)
	for i := range out {
		out[i].Replacement = LegacyMapping[out[i].Name]
	}
	return out
}
