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

package cmd

import (
	"context"
	"fmt"
	"strings"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// AppDefInfo holds resolved information about an ApplicationDefinition.
type AppDefInfo struct {
	Name     string // e.g. "postgres"
	Kind     string // e.g. "Postgres"
	Plural   string // e.g. "postgreses"
	Singular string // e.g. "postgres"
	Prefix   string // e.g. "postgres-"
	IsModule bool
}

// AppDefRegistry provides fast lookup of ApplicationDefinitions by plural, singular, or kind.
type AppDefRegistry struct {
	byPlural   map[string]*AppDefInfo
	bySingular map[string]*AppDefInfo
	byKind     map[string]*AppDefInfo
	all        []*AppDefInfo
}

// discoverAppDefs lists all ApplicationDefinitions from the cluster and builds a registry.
func discoverAppDefs(ctx context.Context, typedClient client.Client) (*AppDefRegistry, error) {
	var list cozyv1alpha1.ApplicationDefinitionList
	if err := typedClient.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("failed to list ApplicationDefinitions: %w", err)
	}

	reg := &AppDefRegistry{
		byPlural:   make(map[string]*AppDefInfo),
		bySingular: make(map[string]*AppDefInfo),
		byKind:     make(map[string]*AppDefInfo),
	}

	for i := range list.Items {
		ad := &list.Items[i]
		info := &AppDefInfo{
			Name:     ad.Name,
			Kind:     ad.Spec.Application.Kind,
			Plural:   ad.Spec.Application.Plural,
			Singular: ad.Spec.Application.Singular,
			Prefix:   ad.Spec.Release.Prefix,
			IsModule: ad.Spec.Dashboard != nil && ad.Spec.Dashboard.Module,
		}
		reg.all = append(reg.all, info)
		reg.byPlural[strings.ToLower(info.Plural)] = info
		reg.bySingular[strings.ToLower(info.Singular)] = info
		reg.byKind[strings.ToLower(info.Kind)] = info
	}

	return reg, nil
}

// Resolve looks up an AppDefInfo by name (case-insensitive), checking plural, singular, then kind.
func (r *AppDefRegistry) Resolve(name string) *AppDefInfo {
	lower := strings.ToLower(name)
	if info, ok := r.byPlural[lower]; ok {
		return info
	}
	if info, ok := r.bySingular[lower]; ok {
		return info
	}
	if info, ok := r.byKind[lower]; ok {
		return info
	}
	return nil
}

// ResolveModule looks up an AppDefInfo among modules only.
func (r *AppDefRegistry) ResolveModule(name string) *AppDefInfo {
	lower := strings.ToLower(name)
	for _, info := range r.all {
		if !info.IsModule {
			continue
		}
		if strings.ToLower(info.Plural) == lower ||
			strings.ToLower(info.Singular) == lower ||
			strings.ToLower(info.Kind) == lower {
			return info
		}
	}
	return nil
}

// All returns all discovered AppDefInfo entries.
func (r *AppDefRegistry) All() []*AppDefInfo {
	return r.all
}
