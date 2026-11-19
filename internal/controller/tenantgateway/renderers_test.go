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

package tenantgateway

import (
	"bytes"
	"errors"
	"io"
	"os"
	"regexp"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	gatewayv1alpha1 "github.com/cozystack/cozystack/api/gateway/v1alpha1"
)

// gatewayCRDBundlePath is the Gateway API bundle the platform installs,
// which is what admits or refuses the Gateway this controller writes.
const gatewayCRDBundlePath = "../../../packages/system/gateway-api-crds/templates/crds-experimental.yaml"

// gatewayListenerRules returns the patterns the shipped Gateway CRD puts
// on spec.listeners[].name and spec.listeners[].hostname. They are read
// from the bundle rather than restated here, so a re-vendored bundle is
// what the renderer is judged against, and a rule the bundle no longer
// carries fails here rather than being tested against a stale copy.
func gatewayListenerRules(t *testing.T) (name, hostname *regexp.Regexp) {
	t.Helper()
	raw, err := os.ReadFile(gatewayCRDBundlePath)
	if err != nil {
		t.Fatalf("read Gateway API bundle: %v", err)
	}
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	for {
		var crd apiextensionsv1.CustomResourceDefinition
		err := dec.Decode(&crd)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode Gateway API bundle: %v", err)
		}
		if crd.Name != "gateways.gateway.networking.k8s.io" {
			continue
		}
		for i := range crd.Spec.Versions {
			v := &crd.Spec.Versions[i]
			if v.Name != "v1" || v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
				continue
			}
			listeners := v.Schema.OpenAPIV3Schema.Properties["spec"].Properties["listeners"]
			if listeners.Items == nil || listeners.Items.Schema == nil {
				t.Fatal("Gateway v1 spec.listeners carries no item schema")
			}
			props := listeners.Items.Schema.Properties
			if props["name"].Pattern == "" || props["hostname"].Pattern == "" {
				t.Fatalf("Gateway v1 listener name or hostname carries no pattern: name=%q hostname=%q", props["name"].Pattern, props["hostname"].Pattern)
			}
			return regexp.MustCompile(props["name"].Pattern), regexp.MustCompile(props["hostname"].Pattern)
		}
	}
	t.Fatal("Gateway v1 listener schema not found in the bundle")
	return nil, nil
}

// TestRenderGateway_ListenerNamesAreAdmissibleForEveryRouteHostname pins
// the renderer's own invariant against the shipped CRD: every hostname
// the listener hostname rule admits renders to a listener name the
// listener name rule admits and to a Certificate name the apiserver
// takes. The two rules differ in one place, the leading "*." a hostname
// may carry and a name may not, and the name used to be built from the
// hostname's first label verbatim, so a wildcard hostname produced a
// name the apiserver refused and took the whole Gateway write with it.
// The fake client validates no pattern, which is why the rule is read
// from the bundle and applied here.
func TestRenderGateway_ListenerNamesAreAdmissibleForEveryRouteHostname(t *testing.T) {
	nameRule, hostnameRule := gatewayListenerRules(t)
	// Positive control on the rules as read: a pattern that admitted
	// the asterisk, or a hostname rule that refused the wildcard, would
	// leave every assertion below unable to fail for the reason it is
	// written.
	if nameRule.MatchString("https-*-06a3e25d") {
		t.Fatalf("listener name rule %q admits an asterisk; the bundle no longer carries the rule this test exists for", nameRule)
	}
	if !hostnameRule.MatchString("*.foo.example.com") {
		t.Fatalf("listener hostname rule %q refuses a wildcard; the row that decides this test is unreachable", hostnameRule)
	}

	s := newScheme(t)
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:             "foo.example.com",
			CertMode:         gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName: "cilium",
		},
	}
	// A concrete hostname whose first label is the word the wildcard is
	// rewritten to sits beside the wildcard, so that the two cannot
	// share a name: the suffix hashes the whole hostname, asterisk
	// included.
	hostnames := []string{
		"*.foo.example.com",
		"*.apps.foo.example.com",
		"wildcard.foo.example.com",
		"harbor.foo.example.com",
		"foo.example.com",
	}
	for _, h := range hostnames {
		if !hostnameRule.MatchString(h) {
			t.Fatalf("fixture hostname %q is not one the listener hostname rule admits", h)
		}
	}
	r := &Reconciler{Scheme: s}
	gw, err := r.renderGateway(tgw, hostnames, nil)
	if err != nil {
		t.Fatalf("renderGateway: %v", err)
	}

	seen := map[string]string{}
	rendered := map[string]bool{}
	for _, l := range gw.Spec.Listeners {
		name := string(l.Name)
		if !nameRule.MatchString(name) {
			t.Errorf("listener %q is not a name the shipped Gateway CRD admits (%s)", name, nameRule)
		}
		if prev, dup := seen[name]; dup {
			t.Errorf("listener name %q rendered twice, for %q and %q", name, prev, hostnameOf(l.Hostname))
		}
		seen[name] = hostnameOf(l.Hostname)
		if l.Hostname != nil {
			rendered[string(*l.Hostname)] = true
		}
		if l.TLS == nil {
			continue
		}
		for _, ref := range l.TLS.CertificateRefs {
			if errs := validation.IsDNS1123Subdomain(string(ref.Name)); len(errs) > 0 {
				t.Errorf("listener %q references Secret %q, which is not a name the apiserver takes: %v", name, ref.Name, errs)
			}
		}
	}
	for _, h := range hostnames {
		if !rendered[h] {
			t.Errorf("no listener rendered for %q; a hostname dropped on the floor passes the name checks trivially", h)
		}
		if errs := validation.IsDNS1123Subdomain(perListenerCertName(tgw, h)); len(errs) > 0 {
			t.Errorf("Certificate name for %q is %q, which the apiserver refuses: %v", h, perListenerCertName(tgw, h), errs)
		}
	}
	if got, want := perListenerName("*.foo.example.com"), "https-wildcard-06a3e25d"; got != want {
		t.Errorf("perListenerName(*.foo.example.com) = %q, want %q: the label is rewritten and the hash of the whole hostname stays", got, want)
	}
}

func hostnameOf(h *gatewayv1.Hostname) string {
	if h == nil {
		return ""
	}
	return string(*h)
}

// TestPerListenerName_KeepsEveryNameThatWasAlreadyAdmissible pins the
// names concrete hostnames render to, byte for byte. A listener is
// addressed by its name: a route pins one by sectionName, and renaming
// a listener on a live Gateway is a delete and a create, with every
// route on it detached in between. Making the wildcard admissible must
// therefore move no name that already was, and this table is what says
// so on upgrade.
func TestPerListenerName_KeepsEveryNameThatWasAlreadyAdmissible(t *testing.T) {
	tgw := &gatewayv1alpha1.TenantGateway{ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"}}
	for _, tc := range []struct{ hostname, listener, cert string }{
		{"harbor.foo.example.com", "https-harbor-760d5a8a", "cozystack-harbor-760d5a8a-tls"},
		{"api.foo.example.com", "https-api-0d0905b9", "cozystack-api-0d0905b9-tls"},
		{"HARBOR.foo.example.com", "https-harbor-760d5a8a", "cozystack-harbor-760d5a8a-tls"},
		{"wildcard.foo.example.com", "https-wildcard-21b97197", "cozystack-wildcard-21b97197-tls"},
		{"shop.alice.foo.example.com", "https-shop-187a7b66", "cozystack-shop-187a7b66-tls"},
		{"foo.example.com", "https-foo-45a335de", "cozystack-foo-45a335de-tls"},
	} {
		if got := perListenerName(tc.hostname); got != tc.listener {
			t.Errorf("perListenerName(%q) = %q, want %q; a changed name detaches every route pinned to the old one", tc.hostname, got, tc.listener)
		}
		if got := perListenerCertName(tgw, tc.hostname); got != tc.cert {
			t.Errorf("perListenerCertName(%q) = %q, want %q; a changed name orders a new certificate and orphans the old Secret", tc.hostname, got, tc.cert)
		}
	}
}
