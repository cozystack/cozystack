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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
// opts out. A platform that has no values Secret has set nothing and the create goes
// on. Any other failure to read it fails the create: the pool would be stored without
// the defaults, with no sign to the client, and since they are hashed into names the
// MachineDeployment points at, putting them in later re-images it.
func (r *REST) applyCreateDefaults(ctx context.Context, app *appsv1alpha1.Application) error {
	if r.kindName != kubernetesNodesKind {
		return nil
	}
	defaults, ok, err := r.loadWorkerPoolDefaults(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	spec := map[string]any{}
	if app.Spec != nil && len(app.Spec.Raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(app.Spec.Raw))
		dec.UseNumber()
		if err := dec.Decode(&spec); err != nil {
			klog.Warningf("Not applying worker pool defaults to %s/%s: cannot read its spec: %v", app.Namespace, app.Name, err)
			return nil
		}
		if spec == nil {
			spec = map[string]any{}
		}
	}

	if !defaults.fill(spec) {
		return nil
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		klog.Warningf("Not applying worker pool defaults to %s/%s: cannot encode its spec: %v", app.Namespace, app.Name, err)
		return nil
	}
	app.Spec = &apiextv1.JSON{Raw: raw}
	return nil
}

func (r *REST) loadWorkerPoolDefaults(ctx context.Context) (workerPoolDefaults, bool, error) {
	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: platformValuesNamespace, Name: platformValuesSecret}
	if err := r.c.Get(ctx, key, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return workerPoolDefaults{}, false, nil
		}
		klog.Errorf("Cannot read the platform worker pool defaults from Secret %s: %v", key, err)
		return workerPoolDefaults{}, false, apierrors.NewServiceUnavailable(fmt.Sprintf("cannot read the platform worker pool defaults from Secret %s, try again: %v", key, err))
	}
	// An empty document decodes to an empty struct without an error, which would read
	// as "no defaults" instead of as a Secret that holds none.
	raw := secret.Data[platformValuesKey]
	if len(bytes.TrimSpace(raw)) == 0 {
		klog.Errorf("Secret %s holds no %s", key, platformValuesKey)
		return workerPoolDefaults{}, false, apierrors.NewInternalError(fmt.Errorf("secret %s does not hold readable %s, so the platform worker pool defaults cannot be applied: it is missing or empty", key, platformValuesKey))
	}
	var values platformWorkerValues
	if err := yaml.Unmarshal(raw, &values); err != nil {
		klog.Errorf("Secret %s does not hold readable %s: %v", key, platformValuesKey, err)
		return workerPoolDefaults{}, false, apierrors.NewInternalError(fmt.Errorf("secret %s does not hold readable %s, so the platform worker pool defaults cannot be applied: %w", key, platformValuesKey, err))
	}
	d := values.Cluster.WorkerImage
	return d, d.Catalog == "true" || d.FactoryURL != "" || d.InstallerRepository != "", nil
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
