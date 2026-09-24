package main

import (
	"testing"

	mount "k8s.io/mount-utils"
)

func TestIsNFSMount(t *testing.T) {
	m := mount.NewFakeMounter([]mount.MountPoint{
		{Path: "/pods/nfs3", Type: "nfs"},
		{Path: "/pods/nfs4", Type: "nfs4"},
		{Path: "/pods/block", Type: "ext4"},
	})

	cases := map[string]bool{
		"/pods/nfs3":    true,
		"/pods/nfs4":    true,
		"/pods/block":   false,
		"/pods/missing": false,
	}
	for path, want := range cases {
		if got := isNFSMount(path, m); got != want {
			t.Errorf("isNFSMount(%q) = %v, want %v", path, got, want)
		}
	}
}
