package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const volumePath = apiPrefix + "/namespaces/tenant-a/virtualmachinevolumes/vm-disk-data/upload"

// upload posts body to path as user. A body of known length is sent with
// Content-Length, as curl and the browser do; sized false sends it chunked.
func (env *testEnv) upload(t *testing.T, path, user string, body string, sized bool, header http.Header) (*http.Response, metav1.Status) {
	t.Helper()
	var reader io.Reader = strings.NewReader(body)
	if !sized {
		reader = io.MultiReader(reader)
	}
	req, err := http.NewRequest(http.MethodPost, env.proxy.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if user != "" {
		req.Header.Set("X-Remote-User", user)
	}
	resp, err := env.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var status metav1.Status
	_ = json.Unmarshal(raw, &status)
	return resp, status
}

func TestVolumeUpload(t *testing.T) {
	env := newTestEnv(t, "running", allowAlice)
	image := "QFI\xfb and the rest of a qcow2 image"

	resp, status := env.upload(t, volumePath+"?filename=debian-13.QCOW2", "alice", image, true, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %d %s", resp.StatusCode, status.Message)
	}
	if status.Message != "local:import/vm-disk-data.qcow2" {
		t.Fatalf("volid %q", status.Message)
	}
	// The file carries kubemox's name, the caller's extension and the bytes
	// as they came.
	up := env.pve.uploaded
	if up == nil || up.Content != "import" || up.Filename != "vm-disk-data.qcow2" || string(up.Bytes) != image {
		t.Fatalf("uploaded %+v", up)
	}
	// The SubjectAccessReview asked for get on the kubemox volume.
	last := env.sars[len(env.sars)-1]
	if ra := last.ResourceAttributes; ra.Verb != "get" || ra.Group != vmGroup || ra.Resource != volumeResource || ra.Name != "vm-disk-data" || ra.Namespace != "tenant-a" {
		t.Fatalf("SAR %+v", ra)
	}

	// The browser names the file in Content-Disposition instead.
	disposition := http.Header{"Content-Disposition": {`attachment; filename="image.raw"`}}
	if resp, status := env.upload(t, volumePath, "alice", image, true, disposition); resp.StatusCode != http.StatusCreated || status.Message != "local:import/vm-disk-data.raw" {
		t.Fatalf("Content-Disposition: %d %s", resp.StatusCode, status.Message)
	}

	for name, tc := range map[string]struct {
		path, user string
		sized      bool
		want       int
		message    string
	}{
		"no length":       {volumePath + "?filename=a.qcow2", "alice", false, http.StatusLengthRequired, "Content-Length"},
		"no file name":    {volumePath, "alice", true, http.StatusBadRequest, "filename"},
		"wrong extension": {volumePath + "?filename=a.vhdx", "alice", true, http.StatusBadRequest, ".qcow2, .raw, .vmdk, .ova"},
		"not waiting":     {apiPrefix + "/namespaces/tenant-a/virtualmachinevolumes/vm-disk-ready/upload?filename=a.qcow2", "alice", true, http.StatusConflict, "not waiting for an upload"},
		"missing volume":  {apiPrefix + "/namespaces/tenant-a/virtualmachinevolumes/ghost/upload?filename=a.qcow2", "alice", true, http.StatusNotFound, "not found"},
		"forbidden user":  {volumePath + "?filename=a.qcow2", "mallory", true, http.StatusForbidden, "forbidden"},
		"no user":         {volumePath + "?filename=a.qcow2", "", true, http.StatusUnauthorized, ""},
	} {
		env.pve.uploaded = nil
		resp, status := env.upload(t, tc.path, tc.user, image, tc.sized, nil)
		if resp.StatusCode != tc.want || !strings.Contains(status.Message, tc.message) {
			t.Errorf("%s: %d %q, want %d containing %q", name, resp.StatusCode, status.Message, tc.want, tc.message)
		}
		if env.pve.uploaded != nil {
			t.Errorf("%s: reached the hypervisor", name)
		}
	}

	if resp, _ := env.do(t, http.MethodGet, volumePath, "alice"); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", resp.StatusCode)
	}
}

func TestVolumeUploadReportsTheHypervisorsWords(t *testing.T) {
	env := newTestEnv(t, "running", allowAlice)
	env.pve.uploadExit = "unable to parse volume filename 'vm-disk-data.qcow2'"
	resp, status := env.upload(t, volumePath+"?filename=a.qcow2", "alice", "bytes", true, nil)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(status.Message, env.pve.uploadExit) {
		t.Fatalf("%d %q", resp.StatusCode, status.Message)
	}
}
