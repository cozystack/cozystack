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

// validateSiteRouterDeclaredNetworks rejects a SiteRouter instance whose declared
// declared networks are malformed or overlap a cluster-owned network. It judges
// remoteCIDRs, staticRoutes[].destination and bgp.neighbors[].address — every
// field that programs a route or opens a firewall rule on the gateway. It returns
// nil for any other kind (the check is SiteRouter-specific) and for a SiteRouter
// that declares none of them. On a violation it returns a Forbidden status error
// whose message names every offender, its field and its colliding network. The cluster CIDRs come from the
// cozy-system/cozystack ConfigMap, read through the uncached watch client when one
// is wired (production) and the cached client otherwise (unit tests), falling back
// to the platform-values defaults when the ConfigMap is absent.
func (r *REST) validateSiteRouterDeclaredNetworks(ctx context.Context, app *appsv1alpha1.Application) error {
	if r.kindName != validation.SiteRouterKind {
		return nil
	}
	declared, err := siteRouterDenysetInputs(app)
	if err != nil {
		return apierrors.NewBadRequest("spec.values does not decode: " + err.Error())
	}
	if len(declared.RemoteCIDRs) == 0 && len(declared.StaticRouteDestinations) == 0 && len(declared.BGPNeighborAddresses) == 0 {
		return nil
	}

	nets, err := r.siteRouterClusterNetworks(ctx)
	if err != nil {
		return err
	}

	rejections, err := denyset.Validate(declared, nets)
	if err != nil {
		// A malformed cluster network is an operator-supplied value. Surface it as
		// a server-side error rather than a Forbidden aimed at the tenant, who has
		// no way to act on it.
		return apierrors.NewInternalError(err)
	}
	if len(rejections) == 0 {
		return nil
	}

	msgs := make([]string, 0, len(rejections))
	for _, rej := range rejections {
		msgs = append(msgs, rej.Message())
	}
	return apierrors.NewForbidden(r.gvr.GroupResource(), app.Name, errors.New(strings.Join(msgs, "; ")))
}

// siteRouterClusterNetworks resolves the deny-set's stable CIDRs and live node
// and service addresses through the shared discovery helper, so the apiserver
// and controller judge a remoteCIDR against identical networks (D10).
func (r *REST) siteRouterClusterNetworks(ctx context.Context) (denyset.ClusterNetworks, error) {
	return denyset.DiscoverClusterNetworks(ctx, r.clusterReader(), types.NamespacedName{
		Namespace: siteRouterConfigNamespace,
		Name:      siteRouterConfigName,
	})
}

// clusterReader returns the reader SiteRouter deny-set discovery uses for the
// ConfigMap, Node, and Service reads. The direct (uncached) watch client is
// preferred so discovery does not require cluster-wide cache informers; it falls
// back to the cached client when no watch client is wired (unit tests).
func (r *REST) clusterReader() client.Reader {
	if r.w != nil {
		return r.w
	}
	return r.c
}

// siteRouterDenysetInputs extracts every declared field the deny set judges from
// an Application's spec.values. A nil/empty spec yields nothing to validate.
// Malformed address strings are preserved so denyset.Validate can reject them
// with a legible message; it is only a spec whose SHAPE is wrong that errors
// here.
//
// The error return is the point. spec.values is not schema-checked before this
// runs, so `remoteCIDRs: ["10.244.0.0/16", 42]` fails the []string decode — and
// swallowing that, as this used to, skipped validation entirely and admitted the
// object. The controller's own extraction is more permissive and keeps the string
// elements, so the tenant got a 201 and a broken tunnel where the whole point of
// this file (see the parity note above) is a synchronous rejection.
//
// This is the controller-side twin of denysetInputs; the two must enumerate the
// same fields.
func siteRouterDenysetInputs(app *appsv1alpha1.Application) (denyset.Inputs, error) {
	if app.Spec == nil || len(app.Spec.Raw) == 0 {
		return denyset.Inputs{}, nil
	}
	var values struct {
		RemoteCIDRs  []string `json:"remoteCIDRs"`
		StaticRoutes []struct {
			Destination string `json:"destination"`
		} `json:"staticRoutes"`
		BGP struct {
			Neighbors []struct {
				Address string `json:"address"`
			} `json:"neighbors"`
		} `json:"bgp"`
	}
	if err := json.Unmarshal(app.Spec.Raw, &values); err != nil {
		return denyset.Inputs{}, err
	}

	in := denyset.Inputs{RemoteCIDRs: values.RemoteCIDRs}
	for _, rt := range values.StaticRoutes {
		if rt.Destination != "" {
			in.StaticRouteDestinations = append(in.StaticRouteDestinations, rt.Destination)
		}
	}
	for _, n := range values.BGP.Neighbors {
		if n.Address != "" {
			in.BGPNeighborAddresses = append(in.BGPNeighborAddresses, n.Address)
		}
	}
	return in, nil
}
