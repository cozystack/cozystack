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
	"errors"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cozystack/cozystack/internal/siterouter/denyset"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/apis/apps/validation"
)

// SiteRouter deny-set admission (DECISIONS.md D9/D10).
//
// A SiteRouter app instance declares tunnel remoteCIDRs; if any overlaps a
// cluster-owned network the controller would program a return route that
// blackholes cluster traffic. This SiteRouter-scoped validating admission check
// rejects such a remoteCIDR synchronously at apply time (a precise Forbidden
// naming the offending CIDR and the colliding network) — the same decision the
// controller makes at reconcile time, via the same pure denyset.Validate helper,
// so admission and reconcile never disagree. Shape validation (well-formed CIDR,
// peer address, ports/enums) stays in values.schema.json; this check adds only
// the cross-resource deny-set that the schema cannot express.
//
// It is a no-op for every non-SiteRouter kind, so generic app-instance admission
// and the Ready/WorkloadsReady conversion (convertHelmReleaseToApplication) are
// completely untouched.

const (
	// siteRouterConfigNamespace / siteRouterConfigName locate the cluster-wide
	// cozystack ConfigMap the deny-set sources the cluster CIDRs from. Mirrors the
	// controller's cozystackConfig* constants; the ConfigMap key names and the
	// default CIDRs live in the shared denyset package (single source of truth).
	siteRouterConfigNamespace = "cozy-system"
	siteRouterConfigName      = "cozystack"
)

// validateSiteRouterRemoteCIDRs rejects a SiteRouter instance whose declared
// remoteCIDRs are malformed or overlap a cluster-owned network. It returns nil
// for any other kind (the check is SiteRouter-specific) and for a SiteRouter with
// no remoteCIDRs. On a violation it returns a Forbidden status error whose message
// names every offender and its colliding network. The cluster CIDRs come from the
// cozy-system/cozystack ConfigMap, read through the uncached watch client when one
// is wired (production) and the cached client otherwise (unit tests), falling back
// to the platform-values defaults when the ConfigMap is absent.
func (r *REST) validateSiteRouterRemoteCIDRs(ctx context.Context, app *appsv1alpha1.Application) error {
	if r.kindName != validation.SiteRouterKind {
		return nil
	}
	cidrs := siteRouterRemoteCIDRs(app)
	if len(cidrs) == 0 {
		return nil
	}

	nets, err := r.siteRouterClusterNetworks(ctx)
	if err != nil {
		return err
	}

	rejections := denyset.Validate(cidrs, nets)
	if len(rejections) == 0 {
		return nil
	}

	msgs := make([]string, 0, len(rejections))
	for _, rej := range rejections {
		msgs = append(msgs, rej.Message())
	}
	return apierrors.NewForbidden(r.gvr.GroupResource(), app.Name, errors.New(strings.Join(msgs, "; ")))
}

// siteRouterClusterNetworks resolves the deny-set's cluster networks from the
// cozy-system/cozystack ConfigMap via the shared denyset mapping, so the apiserver
// and the controller judge a remoteCIDR against identical networks (D10).
func (r *REST) siteRouterClusterNetworks(ctx context.Context) (denyset.ClusterNetworks, error) {
	cm := &corev1.ConfigMap{}
	err := r.clusterReader().Get(ctx, types.NamespacedName{Namespace: siteRouterConfigNamespace, Name: siteRouterConfigName}, cm)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return denyset.ClusterNetworksFromConfigMap(nil), nil // all defaults
		}
		return denyset.ClusterNetworks{}, err
	}
	return denyset.ClusterNetworksFromConfigMap(cm.Data), nil
}

// clusterReader returns the reader the SiteRouter deny-set uses for its one
// cluster ConfigMap read. The direct (uncached) watch client is preferred so the
// read does not spin up a cluster-wide ConfigMap informer in the apiserver cache;
// it falls back to the cached client when no watch client is wired (unit tests).
func (r *REST) clusterReader() client.Reader {
	if r.w != nil {
		return r.w
	}
	return r.c
}

// siteRouterRemoteCIDRs extracts the remoteCIDRs list from an Application's
// spec.values. A nil/empty/unparseable spec yields no CIDRs (nothing to validate);
// malformed CIDR strings are preserved so denyset.Validate can reject them.
func siteRouterRemoteCIDRs(app *appsv1alpha1.Application) []string {
	if app.Spec == nil || len(app.Spec.Raw) == 0 {
		return nil
	}
	var values struct {
		RemoteCIDRs []string `json:"remoteCIDRs"`
	}
	if err := json.Unmarshal(app.Spec.Raw, &values); err != nil {
		return nil
	}
	return values.RemoteCIDRs
}
