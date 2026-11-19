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

// reservedEntities are Cilium's built-in entity names. They stay
// platform-managed and are deliberately not tenant-expressible — a SecurityGroup
// reaches external destinations through CIDR/FQDN, not entities — so the API
// rejects an application or SecurityGroup name that collides with one.
var reservedEntities = map[string]struct{}{
	"world": {}, "cluster": {}, "kube-apiserver": {}, "host": {},
	"remote-node": {}, "health": {}, "init": {}, "unmanaged": {}, "all": {},
}

// validateSecurityGroup rejects specs that are schema-valid but semantically
// wrong (malformed CIDR, out-of-range port, unknown protocol, an attachment or
// peer that cannot be projected to a valid selector). Without this the backing
// CiliumNetworkPolicy write succeeds and the API returns success, but Cilium
// discards the rule asynchronously — so the policy silently never enforces,
// which for a deny-by-default firewall is a dangerous, invisible gap.
func validateSecurityGroup(sg *sdnv1alpha1.SecurityGroup) error {
	var errs field.ErrorList
	spec := field.NewPath("spec")

	// The SecurityGroup's own name becomes the backing policy's membership label
	// key (membershipLabelKey(name)). A resource name is validated as a DNS-1123
	// subdomain (up to 253 chars), which is more permissive than a label key, so
	// reject a name that cannot form a valid key here — with a clean field error
	// on metadata.name — instead of letting the backing CiliumNetworkPolicy write
	// fail later with a confusing label-key error.
	for _, msg := range validation.IsQualifiedName(membershipLabelKey(sg.Name)) {
		errs = append(errs, field.Invalid(field.NewPath("metadata", "name"), sg.Name, msg))
	}

	att := spec.Child("attachments")
	for i := range sg.Spec.Attachments {
		errs = append(errs, validateAppRef(att.Index(i), &sg.Spec.Attachments[i])...)
	}

	for i := range sg.Spec.Ingress {
		in := &sg.Spec.Ingress[i]
		p := spec.Child("ingress").Index(i)
		for j := range in.FromApp {
			errs = append(errs, validateAppRef(p.Child("fromApp").Index(j), &in.FromApp[j])...)
		}
		errs = append(errs, validateSGNames(p.Child("fromSG"), in.FromSG)...)
		errs = append(errs, validateCIDRs(p.Child("fromCIDR"), in.FromCIDR)...)
		errs = append(errs, validatePortRules(p.Child("toPorts"), in.ToPorts)...)
	}
	for i := range sg.Spec.Egress {
		eg := &sg.Spec.Egress[i]
		p := spec.Child("egress").Index(i)
		for j := range eg.ToApp {
			errs = append(errs, validateAppRef(p.Child("toApp").Index(j), &eg.ToApp[j])...)
		}
		errs = append(errs, validateSGNames(p.Child("toSG"), eg.ToSG)...)
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

// validateAppRef requires kind and name and rejects any component that is not a
// valid label value. Attachments and fromApp/toApp peers project to lineage
// matchLabels, so a value Kubernetes would reject as a label value (e.g. > 63
// chars) must be caught here rather than producing an unenforceable policy.
// Validating apiGroup too keeps the reverse projection lossless. The name must
// also not collide with a reserved Cilium entity.
func validateAppRef(path *field.Path, ref *sdnv1alpha1.ApplicationReference) field.ErrorList {
	var errs field.ErrorList
	if ref.Kind == "" {
		errs = append(errs, field.Required(path.Child("kind"), "must reference an application kind"))
	}
	if ref.Name == "" {
		errs = append(errs, field.Required(path.Child("name"), "must reference an application name"))
	}
	for _, f := range []struct {
		name  string
		value string
	}{{"apiGroup", ref.APIGroup}, {"kind", ref.Kind}, {"name", ref.Name}} {
		if f.value == "" {
			continue
		}
		for _, msg := range validation.IsValidLabelValue(f.value) {
			errs = append(errs, field.Invalid(path.Child(f.name), f.value, msg))
		}
	}
	if _, bad := reservedEntities[strings.ToLower(ref.Name)]; bad {
		errs = append(errs, field.Invalid(path.Child("name"), ref.Name, "must not name a reserved Cilium entity"))
	}
	return errs
}

// validateSGNames rejects a fromSG/toSG peer whose name cannot project to a
// valid membership-label key (MembershipLabelPrefix + name) and one that
// collides with a reserved Cilium entity. References are resolved within the
// SecurityGroup's own namespace (the backing CiliumNetworkPolicy is namespaced),
// so cross-namespace reach is structurally impossible; a reference to a
// non-existent group simply selects no pods.
func validateSGNames(path *field.Path, names []string) field.ErrorList {
	var errs field.ErrorList
	for i, n := range names {
		if n == "" {
			errs = append(errs, field.Required(path.Index(i), "must name a SecurityGroup"))
			continue
		}
		if _, bad := reservedEntities[strings.ToLower(n)]; bad {
			errs = append(errs, field.Invalid(path.Index(i), n, "must not name a reserved Cilium entity"))
			continue
		}
		for _, msg := range validation.IsQualifiedName(sdnv1alpha1.MembershipLabelPrefix + n) {
			errs = append(errs, field.Invalid(path.Index(i), n, msg))
		}
	}
	return errs
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
