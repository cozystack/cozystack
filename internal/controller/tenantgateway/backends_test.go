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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
)

// TestServiceReferenceGranted walks each field a ReferenceGrant is
// matched on. A grant admitting more than it should widens the
// withdrawal to routes Cilium will not serve, which is the defect this
// resolution exists to close; one admitting less strands a route that
// does forward. Both directions need a fixture, so every leg gets a
// case that differs from the admitting one in that field alone.
func TestServiceReferenceGranted(t *testing.T) {
	named := gatewayv1beta1.ObjectName("api-backend")
	other := gatewayv1beta1.ObjectName("something-else")
	grant := func(ns string, from gatewayv1beta1.ReferenceGrantFrom, to gatewayv1beta1.ReferenceGrantTo) gatewayv1beta1.ReferenceGrant {
		return gatewayv1beta1.ReferenceGrant{
			ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: ns},
			Spec: gatewayv1beta1.ReferenceGrantSpec{
				From: []gatewayv1beta1.ReferenceGrantFrom{from},
				To:   []gatewayv1beta1.ReferenceGrantTo{to},
			},
		}
	}
	fromTLSRoute := gatewayv1beta1.ReferenceGrantFrom{
		Group:     gatewayv1.GroupName,
		Kind:      "TLSRoute",
		Namespace: "tenant-foo",
	}
	toAnyService := gatewayv1beta1.ReferenceGrantTo{Group: "", Kind: "Service"}

	cases := []struct {
		name   string
		grants []gatewayv1beta1.ReferenceGrant
		want   bool
	}{
		{
			name:   "no grants at all",
			grants: nil,
			want:   false,
		},
		{
			name:   "unnamed to entry admits every Service",
			grants: []gatewayv1beta1.ReferenceGrant{grant("cozy-public", fromTLSRoute, toAnyService)},
			want:   true,
		},
		{
			name:   "named to entry admits that Service",
			grants: []gatewayv1beta1.ReferenceGrant{grant("cozy-public", fromTLSRoute, gatewayv1beta1.ReferenceGrantTo{Group: "", Kind: "Service", Name: &named})},
			want:   true,
		},
		{
			name:   "named to entry admits no other Service",
			grants: []gatewayv1beta1.ReferenceGrant{grant("cozy-public", fromTLSRoute, gatewayv1beta1.ReferenceGrantTo{Group: "", Kind: "Service", Name: &other})},
			want:   false,
		},
		{
			name:   "grant in the wrong namespace",
			grants: []gatewayv1beta1.ReferenceGrant{grant("cozy-elsewhere", fromTLSRoute, toAnyService)},
			want:   false,
		},
		{
			name: "from names another kind",
			grants: []gatewayv1beta1.ReferenceGrant{grant("cozy-public",
				gatewayv1beta1.ReferenceGrantFrom{Group: gatewayv1.GroupName, Kind: "HTTPRoute", Namespace: "tenant-foo"},
				toAnyService)},
			want: false,
		},
		{
			name: "from names another namespace",
			grants: []gatewayv1beta1.ReferenceGrant{grant("cozy-public",
				gatewayv1beta1.ReferenceGrantFrom{Group: gatewayv1.GroupName, Kind: "TLSRoute", Namespace: "tenant-bar"},
				toAnyService)},
			want: false,
		},
		{
			name:   "to names another kind",
			grants: []gatewayv1beta1.ReferenceGrant{grant("cozy-public", fromTLSRoute, gatewayv1beta1.ReferenceGrantTo{Group: "", Kind: "Secret"})},
			want:   false,
		},
		{
			name:   "to names another group",
			grants: []gatewayv1beta1.ReferenceGrant{grant("cozy-public", fromTLSRoute, gatewayv1beta1.ReferenceGrantTo{Group: "multicluster.x-k8s.io", Kind: "Service"})},
			want:   false,
		},
		{
			name: "one grant among several admits",
			grants: []gatewayv1beta1.ReferenceGrant{
				grant("cozy-elsewhere", fromTLSRoute, toAnyService),
				grant("cozy-public", fromTLSRoute, toAnyService),
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := serviceReferenceGranted(tc.grants, "tenant-foo", "cozy-public", "api-backend")
			if got != tc.want {
				t.Errorf("serviceReferenceGranted = %v, want %v", got, tc.want)
			}
		})
	}
}
