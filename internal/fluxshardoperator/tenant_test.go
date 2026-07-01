package fluxshardoperator

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func hrMeta(namespace, name string, labels map[string]string) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: labels},
	}
}

func TestTenantNamespaceForHR(t *testing.T) {
	tenantLabels := map[string]string{ApplicationKindLabel: TenantKind}
	cases := []struct {
		desc      string
		obj       *metav1.PartialObjectMetadata
		want      string
		wantFound bool
	}{
		{
			desc:      "child module in tenant namespace",
			obj:       hrMeta("tenant-foo", "etcd", map[string]string{ApplicationKindLabel: "Etcd"}),
			want:      "tenant-foo",
			wantFound: true,
		},
		{
			desc:      "user app in nested tenant namespace",
			obj:       hrMeta("tenant-foo-bar", "postgres-db1", map[string]string{ApplicationKindLabel: "Postgres"}),
			want:      "tenant-foo-bar",
			wantFound: true,
		},
		{
			desc:      "parent tenant HR in tenant-root",
			obj:       hrMeta("tenant-root", "tenant-foo", tenantLabels),
			want:      "tenant-foo",
			wantFound: true,
		},
		{
			desc:      "parent HR of a nested tenant",
			obj:       hrMeta("tenant-foo", "tenant-bar", tenantLabels),
			want:      "tenant-foo-bar",
			wantFound: true,
		},
		{
			desc:      "the root tenant itself",
			obj:       hrMeta("tenant-root", "tenant-root", tenantLabels),
			want:      "tenant-root",
			wantFound: true,
		},
		{
			desc:      "HR outside tenant namespaces",
			obj:       hrMeta("cozy-system", "whatever", nil),
			wantFound: false,
		},
		{
			desc:      "tenant-kind HR without tenant- name prefix falls back to namespace",
			obj:       hrMeta("tenant-foo", "oddball", tenantLabels),
			want:      "tenant-foo",
			wantFound: true,
		},
	}
	for _, c := range cases {
		got, found := TenantNamespaceForHR(c.obj)
		if found != c.wantFound || got != c.want {
			t.Errorf("%s: TenantNamespaceForHR() = %q,%v, want %q,%v", c.desc, got, found, c.want, c.wantFound)
		}
	}
}
