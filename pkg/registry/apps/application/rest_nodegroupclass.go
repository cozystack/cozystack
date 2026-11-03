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
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

const kubernetesNodesKind = "KubernetesNodes"

// validateNodeGroupClass refuses a new reference to a NodeGroupClass being
// deleted. cozystack-controller holds such a class until no pool names it, so
// a reference made now would hold the deletion back; pools already on the
// class keep it. old is nil on create. The built-in talos and kairos resolve
// without the object and are not checked. A class that cannot be read fails
// the request: the pool cannot be checked against it.
func (r *REST) validateNodeGroupClass(ctx context.Context, app, old *appsv1alpha1.Application) (field.ErrorList, error) {
	if r.kindName != kubernetesNodesKind {
		return nil, nil
	}
	name := nodeGroupClassName(app)
	if name == "" || name == "talos" || name == "kairos" {
		return nil, nil
	}
	if old != nil && nodeGroupClassName(old) == name {
		return nil, nil
	}
	path := field.NewPath("spec", "nodeGroupClassName")
	ngc := &cozyv1alpha1.NodeGroupClass{}
	if err := r.c.Get(ctx, types.NamespacedName{Name: name}, ngc); err != nil {
		// A missing class fails the pool's render with its name.
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading NodeGroupClass %q: %w", name, err)
	}
	if ngc.DeletionTimestamp != nil {
		return field.ErrorList{field.Invalid(path, name, fmt.Sprintf("NodeGroupClass %q is being deleted; name another class", name))}, nil
	}
	return nil, nil
}

func nodeGroupClassName(app *appsv1alpha1.Application) string {
	if app == nil || app.Spec == nil {
		return ""
	}
	var v struct {
		NodeGroupClassName string `json:"nodeGroupClassName"`
	}
	if err := json.Unmarshal(app.Spec.Raw, &v); err != nil {
		return ""
	}
	return v.NodeGroupClassName
}
