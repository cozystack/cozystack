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

// Package controller runs the GPU binder, the class and device lifecycle,
// usage records and metrics in cozystack-controller.
package controller

import (
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/cozystack/cozystack/internal/gpu"
)

// Setup adds the GPU reconcilers to mgr. With no reconciler implemented it
// adds nothing.
func Setup(mgr manager.Manager, cfg gpu.Config) error {
	return nil
}
