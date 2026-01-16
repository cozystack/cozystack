/*
Copyright 2024 The Cozystack Authors.

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

package registry

import (
	"strconv"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
)

// MaxResourceVersion returns the maximum resourceVersion from all items in a list.
// This is useful when the list's ResourceVersion is empty (e.g., from controller-runtime cache).
func MaxResourceVersion(list runtime.Object) (string, error) {
	var max uint64

	err := meta.EachListItem(list, func(obj runtime.Object) error {
		accessor, err := meta.Accessor(obj)
		if err != nil {
			return err
		}

		rvStr := accessor.GetResourceVersion()
		if rvStr == "" {
			return nil
		}

		rv, err := strconv.ParseUint(rvStr, 10, 64)
		if err != nil {
			return err
		}

		if rv > max {
			max = rv
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	return strconv.FormatUint(max, 10), nil
}
