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

package application

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// InstallationDefaultsNamespace and InstallationDefaultsName locate the
	// ConfigMap the platform chart renders from the applicationDefaults value.
	InstallationDefaultsNamespace = "cozy-system"
	InstallationDefaultsName      = "cozystack-application-defaults"

	// The defaults are optional, so waiting for the cache is bounded: an unreadable
	// ConfigMap must neither keep the API from serving nor hang a create. A read
	// that exceeds the timeout fails open like any other read error.
	InstallationDefaultsSyncTimeout = 30 * time.Second
	installationDefaultsReadTimeout = 5 * time.Second
)

// NewInstallationDefaultsCache returns a cache holding only the installation
// defaults ConfigMap. It is separate from the manager cache because scoping a
// ConfigMap informer there would hide every other ConfigMap from cached reads.
// The create path reads from it, so a transient API error never reaches a create.
func NewInstallationDefaultsCache(cfg *rest.Config, scheme *runtime.Scheme) (cache.Cache, error) {
	return cache.New(cfg, cache.Options{
		Scheme: scheme,
		ByObject: map[client.Object]cache.ByObject{
			&corev1.ConfigMap{}: {
				Namespaces: map[string]cache.Config{
					InstallationDefaultsNamespace: {
						FieldSelector: fields.OneTermEqualSelector("metadata.name", InstallationDefaultsName),
					},
				},
			},
		},
	})
}
