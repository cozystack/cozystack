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

package backend

import (
	"fmt"

	networkv1alpha1 "github.com/cozystack/cozystack/api/network/v1alpha1"
)

// registry maps a backend enum value to its implementation. Each backend
// registers itself from an init() in its own file, so the set of
// supported backends is the single extension point.
var registry = map[networkv1alpha1.ExposureBackend]Backend{}

// register adds a backend to the registry. Called from backend init().
func register(b Backend) {
	registry[b.Name()] = b
}

// For returns the backend implementing the given enum value, or an error
// if none is registered (an unknown backend value reaching here is a bug
// or an unsupported class).
func For(name networkv1alpha1.ExposureBackend) (Backend, error) {
	b, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("no backend registered for %q", name)
	}
	return b, nil
}
