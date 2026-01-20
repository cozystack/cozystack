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
// Version is set at build time via -ldflags:
//
//	go build -ldflags "-X github.com/cozystack/cozystack/pkg/version.Version=v1.0.0"
package version

// Version is set at build time via -ldflags.
var Version = "dev"
