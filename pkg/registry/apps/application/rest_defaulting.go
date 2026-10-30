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

package application

import (
	"encoding/json"

	"github.com/cozystack/cozystack/pkg/apis/apps/defaulting"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
)

// applySpecDefaults applies default values to the Application spec based on the schema
func (r *REST) applySpecDefaults(app *appsv1alpha1.Application) error {
	if r.specSchema == nil {
		return nil
	}
	var m map[string]any
	if app.Spec != nil && len(app.Spec.Raw) > 0 {
		if err := json.Unmarshal(app.Spec.Raw, &m); err != nil {
			return err
		}
	}
	if m == nil {
		m = map[string]any{}
	}
	if err := defaultLikeKubernetes(&m, r.specSchema); err != nil {
		return err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	app.Spec = &apiextv1.JSON{Raw: raw}
	return nil
}

func defaultLikeKubernetes(root *map[string]any, s *structuralschema.Structural) error {
	return defaulting.Apply(root, s)
}
