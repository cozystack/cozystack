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
	"bytes"
	"context"
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

const (
	kubernetesNodesKind = "KubernetesNodes"

	platformValuesNamespace = "cozy-system"
	platformValuesSecret    = "cozystack-values"
	platformValuesKey       = "values.yaml"

	substrateProxmox = "proxmox"
)

// workerPoolDefaults is what the platform chart writes under _cluster.worker-image,
// and only the keys the operator set.
type workerPoolDefaults struct {
	// Catalog is "true" when the worker image catalog package is enabled.
	Catalog             string `json:"catalog"`
	FactoryURL          string `json:"factory-url"`
	InstallerRepository string `json:"installer-repository"`
}

type platformWorkerValues struct {
	Cluster struct {
		WorkerImage workerPoolDefaults `json:"worker-image"`
	} `json:"_cluster"`
}

// applyCreateDefaults writes the platform's worker image defaults into the spec of a
// KubernetesNodes that is being created.
//
// Create only, on purpose: the image source and the installer are hashed into names
// the MachineDeployment points at, so a default applied when a pool renders, or on
// read, would re-image every existing pool the first time an operator set it. Written
// into the spec it is an ordinary field and nothing existing changes.
//
// A field the spec already carries, empty or not, is left alone, which is how a pool
// opts out. Failing to read the platform values never fails the create.
func (r *REST) applyCreateDefaults(ctx context.Context, app *appsv1alpha1.Application) {
	if r.kindName != kubernetesNodesKind {
		return
	}
	defaults, ok := r.loadWorkerPoolDefaults(ctx)
	if !ok {
		return
	}

	spec := map[string]any{}
	if app.Spec != nil && len(app.Spec.Raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(app.Spec.Raw))
		dec.UseNumber()
		if err := dec.Decode(&spec); err != nil {
			klog.Warningf("Not applying worker pool defaults to %s/%s: cannot read its spec: %v", app.Namespace, app.Name, err)
			return
		}
		if spec == nil {
			spec = map[string]any{}
		}
	}

	if !defaults.fill(spec) {
		return
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		klog.Warningf("Not applying worker pool defaults to %s/%s: cannot encode its spec: %v", app.Namespace, app.Name, err)
		return
	}
	app.Spec = &apiextv1.JSON{Raw: raw}
}

func (r *REST) loadWorkerPoolDefaults(ctx context.Context) (workerPoolDefaults, bool) {
	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: platformValuesNamespace, Name: platformValuesSecret}
	if err := r.c.Get(ctx, key, secret); err != nil {
		klog.Warningf("Not applying worker pool defaults: cannot read Secret %s: %v", key, err)
		return workerPoolDefaults{}, false
	}
	var values platformWorkerValues
	if err := yaml.Unmarshal(secret.Data[platformValuesKey], &values); err != nil {
		klog.Warningf("Not applying worker pool defaults: Secret %s does not hold readable %s: %v", key, platformValuesKey, err)
		return workerPoolDefaults{}, false
	}
	d := values.Cluster.WorkerImage
	return d, d.Catalog == "true" || d.FactoryURL != "" || d.InstallerRepository != ""
}

// fill sets every default the spec does not already carry and reports whether it
// changed anything.
//
// A proxmox pool takes neither osImage, which its render refuses, nor an image
// factory, since its disk is cloned from a VM template; it still gets the installer,
// which every pool upgrades its nodes with.
func (d workerPoolDefaults) fill(spec map[string]any) bool {
	changed := false
	substrate, _ := spec["substrate"].(string)
	imports := substrate != substrateProxmox

	if d.Catalog == "true" && imports && absent(spec, "osImage") {
		spec["osImage"] = map[string]any{"builtin": map[string]any{}}
		changed = true
	}

	talos, usable := objectField(spec, "talos")
	if !usable {
		return changed
	}
	filled := false
	if d.FactoryURL != "" && imports && absent(talos, "imageFactoryURL") {
		talos["imageFactoryURL"] = d.FactoryURL
		filled = true
	}
	if d.InstallerRepository != "" && absent(talos, "installerRepository") {
		talos["installerRepository"] = d.InstallerRepository
		filled = true
	}
	if filled {
		spec["talos"] = talos
	}
	return changed || filled
}

// absent reports whether a key is missing or null, which the API treats as unset.
func absent(m map[string]any, key string) bool {
	v, ok := m[key]
	return !ok || v == nil
}

// objectField returns the object stored under key, or a new empty one when the key
// is missing or null. The second result is false when the key holds something that
// is not an object, which is left for validation to refuse.
func objectField(m map[string]any, key string) (map[string]any, bool) {
	v, ok := m[key]
	if !ok || v == nil {
		return map[string]any{}, true
	}
	obj, isObject := v.(map[string]any)
	return obj, isObject
}
