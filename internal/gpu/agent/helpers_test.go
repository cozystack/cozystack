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

package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	podresourcesv1 "k8s.io/kubelet/pkg/apis/podresources/v1"
)

const (
	mdevA100  = "6f2a3c1e-0b1d-4c8e-9a51-2d4f0e7b8a01"
	mdevA100b = "9d8e7f6a-5b4c-4d3e-8f2a-1b0c9d8e7f6a"
	mdevC1    = "7a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
)

// writeSysfs materialises testdata/sysfs.txt under a fresh directory.
func writeSysfs(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	f, err := os.Open("testdata/sysfs.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kind, rest, _ := strings.Cut(line, " ")
		path, arg, _ := strings.Cut(rest, " ")
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		switch kind {
		case "file":
			err = os.WriteFile(full, []byte(arg+"\n"), 0o644)
		case "link":
			err = os.Symlink(arg, full)
		default:
			t.Fatalf("bad fixture line %q", line)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return root
}

type fakeKubelet struct {
	podresourcesv1.UnimplementedPodResourcesListerServer
	alloc *podresourcesv1.AllocatableResourcesResponse
	list  *podresourcesv1.ListPodResourcesResponse
}

func (f *fakeKubelet) List(context.Context, *podresourcesv1.ListPodResourcesRequest) (*podresourcesv1.ListPodResourcesResponse, error) {
	return f.list, nil
}

func (f *fakeKubelet) GetAllocatableResources(context.Context, *podresourcesv1.AllocatableResourcesRequest) (*podresourcesv1.AllocatableResourcesResponse, error) {
	return f.alloc, nil
}

// serveKubelet serves f on a unix socket the way kubelet does and returns a
// client dialled through Dial.
func serveKubelet(t *testing.T, f *fakeKubelet) podresourcesv1.PodResourcesListerClient {
	t.Helper()
	// Not t.TempDir: its path can exceed the 104-byte sun_path on macOS.
	dir, err := os.MkdirTemp("", "pr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "kubelet.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	podresourcesv1.RegisterPodResourcesListerServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	c, closer, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	return c
}

func devs(rn string, ids ...string) *podresourcesv1.ContainerDevices {
	return &podresourcesv1.ContainerDevices{ResourceName: rn, DeviceIds: ids}
}

func podUsing(ns, name string, d ...*podresourcesv1.ContainerDevices) *podresourcesv1.PodResources {
	return &podresourcesv1.PodResources{
		Namespace: ns,
		Name:      name,
		Containers: []*podresourcesv1.ContainerResources{
			{Name: "compute", Devices: d},
		},
	}
}

func dump(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
