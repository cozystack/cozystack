// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package securitygroup

import (
	"net/netip"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	sdnv1alpha1 "github.com/cozystack/cozystack/pkg/apis/sdn/v1alpha1"
)

// validProtocols is the set of L4 protocols a SecurityGroup port rule may name.
// An empty protocol defaults to ANY and is allowed.
var validProtocols = map[string]struct{}{"TCP": {}, "UDP": {}, "SCTP": {}, "ANY": {}}

// validateSecurityGroup rejects specs that are schema-valid but semantically
// wrong (malformed CIDR, out-of-range port, unknown protocol). Without this the
// backing CiliumNetworkPolicy write succeeds and the API returns success, but
// Cilium discards the rule asynchronously — so the policy silently never
// enforces, which for a deny-by-default firewall is a dangerous, invisible gap.
func validateSecurityGroup(sg *sdnv1alpha1.SecurityGroup) error {
	var errs field.ErrorList
	spec := field.NewPath("spec")

	// An empty endpointSelector selects every pod in the namespace in Cilium, so
	// a tenant intending a single-pod rule would silently get a namespace-wide
	// policy (and, with an empty ingress list, a namespace-wide default-deny).
	// Require it to select something, matching the "applies to the selected
	// pods" contract in the type and DESIGN docs.
	if len(sg.Spec.EndpointSelector.MatchLabels) == 0 && len(sg.Spec.EndpointSelector.MatchExpressions) == 0 {
		errs = append(errs, field.Required(spec.Child("endpointSelector"),
			"must select at least one pod; an empty selector would apply the policy to every pod in the namespace"))
	}

	for i := range sg.Spec.Ingress {
		in := &sg.Spec.Ingress[i]
		p := spec.Child("ingress").Index(i)
		errs = append(errs, validateCIDRs(p.Child("fromCIDR"), in.FromCIDR)...)
		errs = append(errs, validatePortRules(p.Child("toPorts"), in.ToPorts)...)
	}
	for i := range sg.Spec.Egress {
		eg := &sg.Spec.Egress[i]
		p := spec.Child("egress").Index(i)
		errs = append(errs, validateCIDRs(p.Child("toCIDR"), eg.ToCIDR)...)
		errs = append(errs, validatePortRules(p.Child("toPorts"), eg.ToPorts)...)
		errs = append(errs, validateFQDNs(p.Child("toFQDNs"), eg.ToFQDNs)...)
	}

	if len(errs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(
		sdnv1alpha1.SchemeGroupVersion.WithKind(kindSG).GroupKind(),
		sg.Name, errs)
}

func validateCIDRs(path *field.Path, cidrs []string) field.ErrorList {
	var errs field.ErrorList
	for i, c := range cidrs {
		// Mirror Cilium's CIDR.sanitize: a CIDR value may be a prefix
		// (10.0.0.0/24) or a bare IP (10.0.0.1, treated as /32 or /128).
		// Rejecting a bare IP here would make the validator stricter than
		// Cilium and block a legitimate single-host rule, so fall back to a
		// plain-address parse before declaring the value invalid.
		if _, err := netip.ParsePrefix(c); err != nil {
			if _, err := netip.ParseAddr(c); err != nil {
				errs = append(errs, field.Invalid(path.Index(i), c, "must be a valid CIDR or IP address"))
			}
		}
	}
	return errs
}

func validateFQDNs(path *field.Path, fqdns []sdnv1alpha1.FQDNSelector) field.ErrorList {
	var errs field.ErrorList
	for i := range fqdns {
		if fqdns[i].MatchName == "" && fqdns[i].MatchPattern == "" {
			errs = append(errs, field.Required(path.Index(i), "must set matchName or matchPattern"))
		}
	}
	return errs
}

func validatePortRules(path *field.Path, rules []sdnv1alpha1.PortRule) field.ErrorList {
	var errs field.ErrorList
	for i := range rules {
		for j := range rules[i].Ports {
			pp := rules[i].Ports[j]
			pPath := path.Index(i).Child("ports").Index(j)

			if pp.Port != "" {
				if n, err := strconv.Atoi(pp.Port); err == nil {
					if n < 1 || n > 65535 {
						errs = append(errs, field.Invalid(pPath.Child("port"), pp.Port, "port number must be between 1 and 65535"))
					}
				} else {
					for _, msg := range validation.IsValidPortName(pp.Port) {
						errs = append(errs, field.Invalid(pPath.Child("port"), pp.Port, msg))
					}
				}
			}

			if pp.Protocol != "" {
				if _, ok := validProtocols[strings.ToUpper(pp.Protocol)]; !ok {
					errs = append(errs, field.NotSupported(pPath.Child("protocol"), pp.Protocol, []string{"TCP", "UDP", "SCTP", "ANY"}))
				}
			}
		}
	}
	return errs
}
