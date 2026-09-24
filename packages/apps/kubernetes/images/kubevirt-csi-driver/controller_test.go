package main

import (
	"context"
	"encoding/json"
	"net"
	"reflect"
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"

	kubevirtclient "kubevirt.io/csi-driver/pkg/kubevirt"
	"kubevirt.io/csi-driver/pkg/service"
	"kubevirt.io/csi-driver/pkg/util"
)

// fakeVirtClient implements only the calls the RWX CreateVolume path makes;
// any other call panics on the nil embedded interface.
type fakeVirtClient struct {
	kubevirtclient.Client
	created *cdiv1.DataVolume
}

func (f *fakeVirtClient) GetDataVolume(_ context.Context, _, name string) (*cdiv1.DataVolume, error) {
	return nil, errors.NewNotFound(cdiv1.SchemeGroupVersion.WithResource("datavolumes").GroupResource(), name)
}

func (f *fakeVirtClient) CreateDataVolume(_ context.Context, _ string, dv *cdiv1.DataVolume) (*cdiv1.DataVolume, error) {
	f.created = dv
	return dv, nil
}

func newTestControllerService(virtClient kubevirtclient.Client) *WrappedControllerService {
	enforcement := util.StorageClassEnforcement{AllowAll: true}
	return &WrappedControllerService{
		ControllerService:       service.NewControllerService(virtClient, "infra-ns", nil, enforcement),
		virtClient:              virtClient,
		infraNamespace:          "infra-ns",
		storageClassEnforcement: enforcement,
	}
}

// RPCs neither the wrapper nor upstream implement must reach the client as
// Unimplemented through the gRPC server, not as a panic on a nil embedded
// pointer. Methods are invoked by name with an empty message so the test also
// compiles against spec versions that predate some of them; there the server
// answers Unimplemented for an unknown method.
func TestControllerAnswersUnimplementedForRPCsTheDriverDoesNotServe(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	csi.RegisterControllerServer(srv, newTestControllerService(&fakeVirtClient{}))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	for _, method := range []string{
		"ControllerModifyVolume",
		"GetSnapshot",
		"ControllerGetVolumeHealth",
		"ControllerListVolumeHealth",
	} {
		t.Run(method, func(t *testing.T) {
			err := conn.Invoke(context.Background(), "/csi.v1.Controller/"+method, &emptypb.Empty{}, &emptypb.Empty{})
			if got := status.Code(err); got != codes.Unimplemented {
				t.Errorf("%s: code = %v (err %v), want Unimplemented", method, got, err)
			}
		})
	}
}

// Upstream rejects RWX+Filesystem, so the wrapper builds the DataVolume itself;
// the JSON it sends to CDI must carry the requested size under
// spec.storage.resources.requests.storage or CDI provisions nothing usable.
func TestCreateVolumeRWXFilesystemDataVolumeSpec(t *testing.T) {
	const size = 3 << 30
	fake := &fakeVirtClient{}
	w := newTestControllerService(fake)

	_, err := w.CreateVolume(context.Background(), &csi.CreateVolumeRequest{
		Name:          "pvc-rwx",
		CapacityRange: &csi.CapacityRange{RequiredBytes: size},
		VolumeCapabilities: []*csi.VolumeCapability{{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
		}},
		Parameters: map[string]string{kubevirtclient.InfraStorageClassNameParameter: "replicated-nfs"},
	})
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	if fake.created == nil {
		t.Fatal("CreateVolume did not create a DataVolume")
	}

	raw, err := json.Marshal(fake.created)
	if err != nil {
		t.Fatalf("marshal DataVolume: %v", err)
	}
	var dv struct {
		Spec struct {
			Storage struct {
				AccessModes  []string `json:"accessModes"`
				VolumeMode   string   `json:"volumeMode"`
				StorageClass string   `json:"storageClassName"`
				Resources    struct {
					Requests map[string]string `json:"requests"`
				} `json:"resources"`
			} `json:"storage"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &dv); err != nil {
		t.Fatalf("unmarshal DataVolume: %v", err)
	}
	st := dv.Spec.Storage

	req, ok := st.Resources.Requests["storage"]
	if !ok {
		t.Fatalf("spec.storage.resources.requests.storage missing in %s", raw)
	}
	q, err := resource.ParseQuantity(req)
	if err != nil {
		t.Fatalf("parse storage request %q: %v", req, err)
	}
	if q.Value() != size {
		t.Errorf("storage request = %s (%d bytes), want %d", req, q.Value(), size)
	}
	if want := []string{string(corev1.ReadWriteMany)}; !reflect.DeepEqual(st.AccessModes, want) {
		t.Errorf("accessModes = %v, want %v", st.AccessModes, want)
	}
	if st.VolumeMode != string(corev1.PersistentVolumeFilesystem) {
		t.Errorf("volumeMode = %q, want %q", st.VolumeMode, corev1.PersistentVolumeFilesystem)
	}
	if st.StorageClass != "replicated-nfs" {
		t.Errorf("storageClassName = %q, want replicated-nfs", st.StorageClass)
	}
}

func TestIsRWXFilesystem(t *testing.T) {
	mount := &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}
	block := &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}
	mode := func(m csi.VolumeCapability_AccessMode_Mode) *csi.VolumeCapability_AccessMode {
		return &csi.VolumeCapability_AccessMode{Mode: m}
	}

	cases := []struct {
		name string
		caps []*csi.VolumeCapability
		want bool
	}{
		{"RWX mount", []*csi.VolumeCapability{{AccessType: mount, AccessMode: mode(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)}}, true},
		{"RWX block is a hotplug volume", []*csi.VolumeCapability{{AccessType: block, AccessMode: mode(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)}}, false},
		{"RWO mount", []*csi.VolumeCapability{{AccessType: mount, AccessMode: mode(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER)}}, false},
		{"nil capability is skipped", []*csi.VolumeCapability{nil}, false},
		{"no capabilities", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRWXFilesystem(tc.caps); got != tc.want {
				t.Errorf("isRWXFilesystem = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsNFSVolume(t *testing.T) {
	fs := corev1.PersistentVolumeFilesystem
	block := corev1.PersistentVolumeBlock
	pvc := func(mode *corev1.PersistentVolumeMode, modes ...corev1.PersistentVolumeAccessMode) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{Spec: corev1.PersistentVolumeClaimSpec{AccessModes: modes, VolumeMode: mode}}
	}

	cases := []struct {
		name string
		pvc  *corev1.PersistentVolumeClaim
		want bool
	}{
		{"RWX filesystem", pvc(&fs, corev1.ReadWriteMany), true},
		{"RWX with unset volume mode defaults to filesystem", pvc(nil, corev1.ReadWriteMany), true},
		{"RWX block is a live-migration hotplug volume", pvc(&block, corev1.ReadWriteMany), false},
		{"RWO filesystem", pvc(&fs, corev1.ReadWriteOnce), false},
		{"RWX among several access modes", pvc(&fs, corev1.ReadWriteOnce, corev1.ReadWriteMany), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNFSVolume(tc.pvc); got != tc.want {
				t.Errorf("isNFSVolume = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseNFSExport(t *testing.T) {
	cases := []struct {
		in               string
		host, port, path string
	}{
		{"nfs://10.0.0.5:2050/export/pvc-1", "10.0.0.5", "2050", "/export/pvc-1"},
		{"nfs://nfs.example.com/export", "nfs.example.com", "2049", "/export"},
		{"nfs://nfs.example.com:2049", "nfs.example.com", "2049", "/"},
		{"nfs://[2001:db8::1]:2049/x", "2001:db8::1", "2049", "/x"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			host, port, path, err := parseNFSExport(tc.in)
			if err != nil {
				t.Fatalf("parseNFSExport(%q): %v", tc.in, err)
			}
			if host != tc.host || port != tc.port || path != tc.path {
				t.Errorf("parseNFSExport(%q) = %q, %q, %q; want %q, %q, %q", tc.in, host, port, path, tc.host, tc.port, tc.path)
			}
		})
	}

	if _, _, _, err := parseNFSExport("nfs://bad host/x"); err == nil {
		t.Error("parseNFSExport accepted a malformed URL")
	}
}

func TestGetNFSExport(t *testing.T) {
	cases := []struct {
		name    string
		spec    corev1.PersistentVolumeSpec
		want    string
		wantErr bool
	}{
		{
			name: "native NFS PV",
			spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{
				NFS: &corev1.NFSVolumeSource{Server: "10.0.0.5", Path: "/export/pvc-1"},
			}},
			want: "nfs://10.0.0.5:2049/export/pvc-1",
		},
		{
			name: "LINSTOR CSI PV",
			spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{VolumeAttributes: map[string]string{
					"linstor.csi.linbit.com/nfs-export": "nfs://10.0.0.6:2049/pvc-2",
				}},
			}},
			want: "nfs://10.0.0.6:2049/pvc-2",
		},
		{
			name: "CSI PV without export attribute",
			spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{VolumeAttributes: map[string]string{"other": "x"}},
			}},
			wantErr: true,
		},
		{name: "no volume source", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := getNFSExport(&corev1.PersistentVolume{Spec: tc.spec})
			if (err != nil) != tc.wantErr {
				t.Fatalf("getNFSExport err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("getNFSExport = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildEndpointSelectorListsEveryVM(t *testing.T) {
	got := buildEndpointSelector([]string{"vm-a", "vm-b"})
	want := map[string]interface{}{
		"matchExpressions": []interface{}{
			map[string]interface{}{
				"key":      "kubevirt.io/vm",
				"operator": "In",
				"values":   []interface{}{"vm-a", "vm-b"},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildEndpointSelector = %#v, want %#v", got, want)
	}
}
