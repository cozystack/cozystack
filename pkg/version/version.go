/*
Copyright 2025 The Cozystack Authors.

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

// Package version provides the version information for cozystack components.
//
// Version resolution order:
//  1. the COZYSTACK_VERSION environment variable, if set and non-empty;
//  2. otherwise the value baked in at build time via -ldflags:
//     go build -ldflags "-X github.com/cozystack/cozystack/pkg/version.Version=v1.0.0"
//
// The environment override lets a single image report any release name, so a
// release-candidate image can be promoted to stable by retagging alone — no
// rebuild. The deploy-time value is supplied by the installer chart
// (cozystackOperator.platformVersion) and injected as COZYSTACK_VERSION on the
// operator Deployment.
package version

import "os"

// Version is set at build time via -ldflags, and may be overridden at runtime
// by the COZYSTACK_VERSION environment variable (see init and resolve).
var Version = "dev"

func init() {
	Version = resolve(Version)
}

// resolve returns the COZYSTACK_VERSION environment value when set and
// non-empty, otherwise the baked-in build-time value.
func resolve(baked string) string {
	if v := os.Getenv("COZYSTACK_VERSION"); v != "" {
		return v
	}
	return baked
}
