package main

import (
	"context"
	"fmt"
	"net/url"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"

	kubevirtclient "kubevirt.io/csi-driver/pkg/kubevirt"
	"kubevirt.io/csi-driver/pkg/service"
	"kubevirt.io/csi-driver/pkg/util"
)

const (
	nfsVolumeKey    = "nfsVolume"
	nfsExportKey    = "nfsExport"
	busParameter    = "bus"
	serialParameter = "serial"
)

var ciliumNetworkPolicyGVR = schema.GroupVersionResource{
	Group:    "cilium.io",
	Version:  "v2",
	Resource: "ciliumnetworkpolicies",
}

var _ csi.ControllerServer = &WrappedControllerService{}

// WrappedControllerService embeds the upstream ControllerService and adds RWX Filesystem (NFS) support.
type WrappedControllerService struct {
	*service.ControllerService
	infraClient             kubernetes.Interface
	dynamicClient           dynamic.Interface
	virtClient              kubevirtclient.Client
	infraNamespace          string
	infraClusterLabels      map[string]string
	storageClassEnforcement util.StorageClassEnforcement
}

// isRWXFilesystem checks if the volume capabilities request RWX access with filesystem mode.
func isRWXFilesystem(caps []*csi.VolumeCapability) bool {
	hasRWX := false
	hasMount := false
	for _, cap := range caps {
		if cap == nil {
			continue
		}
		if cap.GetMount() != nil {
			hasMount = true
		}
		if am := cap.GetAccessMode(); am != nil && am.Mode == csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER {
			hasRWX = true
		}
	}
	return hasRWX && hasMount
}

// CreateVolume intercepts RWX Filesystem requests and creates a DataVolume in the infra
// cluster with AccessMode=RWX and VolumeMode=Filesystem. Upstream rejects RWX+Filesystem,
// so we handle DataVolume creation ourselves. Using DataVolume (not bare PVC) preserves
// compatibility with upstream snapshot and clone operations.
// For all other requests, delegates to upstream.
func (w *WrappedControllerService) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if !isRWXFilesystem(req.GetVolumeCapabilities()) {
		return w.ControllerService.CreateVolume(ctx, req)
	}

	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "missing request")
	}
	if len(req.GetName()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "name missing in request")
	}

	// Storage class enforcement
	storageClassName := req.Parameters[kubevirtclient.InfraStorageClassNameParameter]
	if !w.storageClassEnforcement.AllowAll {
		if storageClassName == "" {
			if !w.storageClassEnforcement.AllowDefault {
				return nil, status.Error(codes.InvalidArgument, "infraStorageclass is not in the allowed list")
			}
		} else if !util.Contains(w.storageClassEnforcement.AllowList, storageClassName) {
			return nil, status.Error(codes.InvalidArgument, "infraStorageclass is not in the allowed list")
		}
	}

	storageSize := req.GetCapacityRange().GetRequiredBytes()
	dvName := req.Name

	// Determine DataVolume source (blank, snapshot, or clone)
	source, err := w.determineDvSource(ctx, req)
	if err != nil {
		return nil, err
	}

	// Handle CSI clone: CDI doesn't allow cloning PVCs in use by a pod,
	// so use DataSourceRef instead (same approach as upstream)
	sourcePVCName := ""
	if source.PVC != nil {
		sourcePVCName = source.PVC.Name
		source = nil
	}

	volumeMode := corev1.PersistentVolumeFilesystem
	dv := &cdiv1.DataVolume{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DataVolume",
			APIVersion: cdiv1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      dvName,
			Namespace: w.infraNamespace,
			Labels:    w.infraClusterLabels,
			Annotations: map[string]string{
				"cdi.kubevirt.io/storage.deleteAfterCompletion":    "false",
				"cdi.kubevirt.io/storage.bind.immediate.requested": "true",
			},
		},
		Spec: cdiv1.DataVolumeSpec{
			Storage: &cdiv1.StorageSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
				VolumeMode:  &volumeMode,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: *resource.NewScaledQuantity(storageSize, 0),
					},
				},
			},
			Source: source,
		},
	}

	if sourcePVCName != "" {
		dv.Spec.Storage.DataSourceRef = &corev1.TypedObjectReference{
			Kind: "PersistentVolumeClaim",
			Name: sourcePVCName,
		}
	}

	if storageClassName != "" {
		dv.Spec.Storage.StorageClassName = &storageClassName
	}

	// Idempotency: check if DataVolume already exists
	if existingDv, err := w.virtClient.GetDataVolume(ctx, w.infraNamespace, dvName); errors.IsNotFound(err) {
		klog.Infof("Creating NFS DataVolume %s/%s", w.infraNamespace, dvName)
		dv, err = w.virtClient.CreateDataVolume(ctx, w.infraNamespace, dv)
		if err != nil {
			klog.Errorf("Failed creating NFS DataVolume %s: %v", dvName, err)
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		if existingDv != nil && existingDv.Spec.Storage != nil {
			existingRequest := existingDv.Spec.Storage.Resources.Requests[corev1.ResourceStorage]
			newRequest := dv.Spec.Storage.Resources.Requests[corev1.ResourceStorage]
			if newRequest.Cmp(existingRequest) != 0 {
				return nil, status.Error(codes.AlreadyExists, "requested storage size does not match existing size")
			}
			dv = existingDv
		}
	}

	serial := string(dv.GetUID())

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			CapacityBytes: storageSize,
			VolumeId:      dvName,
			VolumeContext: map[string]string{
				busParameter:    "scsi",
				serialParameter: serial,
				nfsVolumeKey:    "true",
			},
			ContentSource: req.GetVolumeContentSource(),
		},
	}, nil
}

// determineDvSource determines the DataVolume source from the CSI request content source.
// Mirrors upstream logic for blank, snapshot, and clone sources.
func (w *WrappedControllerService) determineDvSource(ctx context.Context, req *csi.CreateVolumeRequest) (*cdiv1.DataVolumeSource, error) {
	res := &cdiv1.DataVolumeSource{}
	if req.GetVolumeContentSource() != nil {
		source := req.GetVolumeContentSource()
		switch source.Type.(type) {
		case *csi.VolumeContentSource_Snapshot:
			snapshot, err := w.virtClient.GetVolumeSnapshot(ctx, w.infraNamespace, source.GetSnapshot().GetSnapshotId())
			if errors.IsNotFound(err) {
				return nil, status.Errorf(codes.NotFound, "source snapshot %s not found", source.GetSnapshot().GetSnapshotId())
			} else if err != nil {
				return nil, err
			}
			if snapshot != nil {
				res.Snapshot = &cdiv1.DataVolumeSourceSnapshot{
					Name:      snapshot.Name,
					Namespace: w.infraNamespace,
				}
			}
		case *csi.VolumeContentSource_Volume:
			volume, err := w.virtClient.GetDataVolume(ctx, w.infraNamespace, source.GetVolume().GetVolumeId())
			if errors.IsNotFound(err) {
				return nil, status.Errorf(codes.NotFound, "source volume %s not found", source.GetVolume().GetVolumeId())
			} else if err != nil {
				return nil, err
			}
			if volume != nil {
				res.PVC = &cdiv1.DataVolumeSourcePVC{
					Name:      volume.Name,
					Namespace: w.infraNamespace,
				}
			}
		default:
			return nil, status.Error(codes.InvalidArgument, "unknown content type")
		}
	} else {
		res.Blank = &cdiv1.DataVolumeBlankImage{}
	}
	return res, nil
}

// ControllerPublishVolume for NFS volumes: annotates infra PVC for WFFC binding,
// waits for PVC bound, extracts NFS export from PV, and creates CiliumNetworkPolicy.
// For RWO volumes, delegates to upstream (hotplug SCSI).
func (w *WrappedControllerService) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	if req.GetVolumeContext()[nfsVolumeKey] != "true" {
		return w.ControllerService.ControllerPublishVolume(ctx, req)
	}

	if len(req.GetVolumeId()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume id missing in request")
	}
	if len(req.GetNodeId()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "node id missing in request")
	}

	dvName := req.GetVolumeId()
	vmNamespace, vmName, err := cache.SplitMetaNamespaceKey(req.GetNodeId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to parse node ID %q: %v", req.GetNodeId(), err)
	}

	klog.V(3).Infof("Publishing NFS volume %s to node %s/%s", dvName, vmNamespace, vmName)

	// Get VMI for CiliumNetworkPolicy ownerReference
	vmi, err := w.virtClient.GetVirtualMachine(ctx, w.infraNamespace, vmName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get VMI %s: %v", vmName, err)
	}

	// Wait for PVC to be bound (CDI handles immediate binding via annotation)
	klog.V(3).Infof("Waiting for PVC %s to be bound", dvName)
	if err := wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		p, err := w.infraClient.CoreV1().PersistentVolumeClaims(w.infraNamespace).Get(ctx, dvName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return p.Status.Phase == corev1.ClaimBound, nil
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "timed out waiting for PVC %s to be bound: %v", dvName, err)
	}

	// Read PV to get NFS export
	pvc, err := w.infraClient.CoreV1().PersistentVolumeClaims(w.infraNamespace).Get(ctx, dvName, metav1.GetOptions{})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to re-read PVC %s: %v", dvName, err)
	}
	pv, err := w.infraClient.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get PV %s: %v", pvc.Spec.VolumeName, err)
	}
	nfsExport, err := getNFSExport(pv)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to extract NFS export from PV %s: %v", pv.Name, err)
	}
	klog.V(3).Infof("NFS export for volume %s: %s", dvName, nfsExport)

	// Parse NFS URL for CiliumNetworkPolicy port
	_, port, _, err := parseNFSExport(nfsExport)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to parse NFS export URL: %v", err)
	}

	// Create or update CiliumNetworkPolicy allowing egress to NFS server
	cnpName := fmt.Sprintf("csi-nfs-%s", dvName)
	vmiOwnerRef := map[string]interface{}{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "VirtualMachineInstance",
		"name":       vmName,
		"uid":        string(vmi.UID),
	}
	cnp := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "cilium.io/v2",
			"kind":       "CiliumNetworkPolicy",
			"metadata": map[string]interface{}{
				"name":            cnpName,
				"namespace":       vmNamespace,
				"ownerReferences": []interface{}{vmiOwnerRef},
			},
			"spec": map[string]interface{}{
				"endpointSelector": map[string]interface{}{},
				"egress": []interface{}{
					map[string]interface{}{
						"toEndpoints": []interface{}{
							map[string]interface{}{
								"matchLabels": map[string]interface{}{
									"k8s:app.kubernetes.io/component": "linstor-csi-nfs-server",
									"k8s:io.kubernetes.pod.namespace": "cozy-linstor",
								},
							},
						},
						"toPorts": []interface{}{
							map[string]interface{}{
								"ports": []interface{}{
									map[string]interface{}{
										"port":     port,
										"protocol": "TCP",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	if _, err := w.dynamicClient.Resource(ciliumNetworkPolicyGVR).Namespace(vmNamespace).Create(ctx, cnp, metav1.CreateOptions{}); err != nil {
		if !errors.IsAlreadyExists(err) {
			return nil, status.Errorf(codes.Internal, "failed to create CiliumNetworkPolicy %s: %v", cnpName, err)
		}
		// CNP exists — add ownerReference for this VMI
		if err := w.addCNPOwnerReference(ctx, vmNamespace, cnpName, vmiOwnerRef); err != nil {
			return nil, err
		}
	}

	klog.V(3).Infof("Successfully published NFS volume %s", dvName)
	return &csi.ControllerPublishVolumeResponse{
		PublishContext: map[string]string{
			nfsExportKey: nfsExport,
		},
	}, nil
}

// ControllerUnpublishVolume for NFS volumes: deletes CiliumNetworkPolicy.
// For RWO volumes, delegates to upstream (hotplug removal).
func (w *WrappedControllerService) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	dvName := req.GetVolumeId()

	// Determine if NFS by checking infra PVC access modes
	pvc, err := w.infraClient.CoreV1().PersistentVolumeClaims(w.infraNamespace).Get(ctx, dvName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return &csi.ControllerUnpublishVolumeResponse{}, nil
		}
		return nil, err
	}

	if !hasRWXAccessMode(pvc) {
		return w.ControllerService.ControllerUnpublishVolume(ctx, req)
	}

	// NFS volume: remove VMI ownerReference from CiliumNetworkPolicy
	vmNamespace, vmName, err := cache.SplitMetaNamespaceKey(req.GetNodeId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to parse node ID %q: %v", req.GetNodeId(), err)
	}

	cnpName := fmt.Sprintf("csi-nfs-%s", dvName)
	klog.V(3).Infof("Removing VMI %s ownerReference from CiliumNetworkPolicy %s/%s", vmName, vmNamespace, cnpName)
	if err := w.removeCNPOwnerReference(ctx, vmNamespace, cnpName, vmName); err != nil {
		return nil, err
	}

	klog.V(3).Infof("Successfully unpublished NFS volume %s", dvName)
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

// ControllerExpandVolume delegates to upstream for the actual DataVolume/PVC resize.
// For NFS volumes, LINSTOR handles NFS server resize automatically, so no node expansion is needed.
func (w *WrappedControllerService) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	resp, err := w.ControllerService.ControllerExpandVolume(ctx, req)
	if err != nil {
		return nil, err
	}

	// For NFS volumes, no node-side expansion is needed
	pvc, err := w.infraClient.CoreV1().PersistentVolumeClaims(w.infraNamespace).Get(ctx, req.GetVolumeId(), metav1.GetOptions{})
	if err == nil && hasRWXAccessMode(pvc) {
		resp.NodeExpansionRequired = false
	}

	return resp, nil
}

// addCNPOwnerReference adds a VMI ownerReference to an existing CiliumNetworkPolicy.
func (w *WrappedControllerService) addCNPOwnerReference(ctx context.Context, namespace, cnpName string, ownerRef map[string]interface{}) error {
	existing, err := w.dynamicClient.Resource(ciliumNetworkPolicyGVR).Namespace(namespace).Get(ctx, cnpName, metav1.GetOptions{})
	if err != nil {
		return status.Errorf(codes.Internal, "failed to get CiliumNetworkPolicy %s: %v", cnpName, err)
	}

	ownerRefs, _, _ := unstructured.NestedSlice(existing.Object, "metadata", "ownerReferences")
	uid, _, _ := unstructured.NestedString(ownerRef, "uid")
	for _, ref := range ownerRefs {
		if refMap, ok := ref.(map[string]interface{}); ok {
			if refMap["uid"] == uid {
				return nil // already present
			}
		}
	}

	ownerRefs = append(ownerRefs, ownerRef)
	if err := unstructured.SetNestedSlice(existing.Object, ownerRefs, "metadata", "ownerReferences"); err != nil {
		return status.Errorf(codes.Internal, "failed to set ownerReferences: %v", err)
	}
	if _, err := w.dynamicClient.Resource(ciliumNetworkPolicyGVR).Namespace(namespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return status.Errorf(codes.Internal, "failed to update CiliumNetworkPolicy %s: %v", cnpName, err)
	}
	klog.V(3).Infof("Added ownerReference to CiliumNetworkPolicy %s", cnpName)
	return nil
}

// removeCNPOwnerReference removes a VMI ownerReference from a CiliumNetworkPolicy.
// Deletes the CNP if no ownerReferences remain.
func (w *WrappedControllerService) removeCNPOwnerReference(ctx context.Context, namespace, cnpName, vmName string) error {
	existing, err := w.dynamicClient.Resource(ciliumNetworkPolicyGVR).Namespace(namespace).Get(ctx, cnpName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return status.Errorf(codes.Internal, "failed to get CiliumNetworkPolicy %s: %v", cnpName, err)
	}

	ownerRefs, _, _ := unstructured.NestedSlice(existing.Object, "metadata", "ownerReferences")
	var remaining []interface{}
	for _, ref := range ownerRefs {
		if refMap, ok := ref.(map[string]interface{}); ok {
			if refMap["name"] == vmName {
				continue
			}
		}
		remaining = append(remaining, ref)
	}

	if len(remaining) == 0 {
		// Last owner — delete CNP
		if err := w.dynamicClient.Resource(ciliumNetworkPolicyGVR).Namespace(namespace).Delete(ctx, cnpName, metav1.DeleteOptions{}); err != nil {
			if !errors.IsNotFound(err) {
				return status.Errorf(codes.Internal, "failed to delete CiliumNetworkPolicy %s: %v", cnpName, err)
			}
		}
		klog.V(3).Infof("Deleted CiliumNetworkPolicy %s (no more owners)", cnpName)
		return nil
	}

	if err := unstructured.SetNestedSlice(existing.Object, remaining, "metadata", "ownerReferences"); err != nil {
		return status.Errorf(codes.Internal, "failed to set ownerReferences: %v", err)
	}
	if _, err := w.dynamicClient.Resource(ciliumNetworkPolicyGVR).Namespace(namespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return status.Errorf(codes.Internal, "failed to update CiliumNetworkPolicy %s: %v", cnpName, err)
	}
	klog.V(3).Infof("Removed VMI %s ownerReference from CiliumNetworkPolicy %s", vmName, cnpName)
	return nil
}

func hasRWXAccessMode(pvc *corev1.PersistentVolumeClaim) bool {
	for _, mode := range pvc.Spec.AccessModes {
		if mode == corev1.ReadWriteMany {
			return true
		}
	}
	return false
}

// getNFSExport extracts the NFS export URL from a PersistentVolume.
// Supports both native NFS PVs and CSI PVs with nfs-export volume attribute.
func getNFSExport(pv *corev1.PersistentVolume) (string, error) {
	if pv.Spec.NFS != nil {
		return fmt.Sprintf("nfs://%s:2049%s", pv.Spec.NFS.Server, pv.Spec.NFS.Path), nil
	}
	if pv.Spec.CSI != nil && pv.Spec.CSI.VolumeAttributes != nil {
		if export, ok := pv.Spec.CSI.VolumeAttributes["linstor.csi.linbit.com/nfs-export"]; ok {
			return export, nil
		}
	}
	return "", fmt.Errorf("no NFS export info found in PV %s", pv.Name)
}

// parseNFSExport parses an NFS URL of the form nfs://host:port/path.
func parseNFSExport(nfsURL string) (host, port, path string, err error) {
	u, err := url.Parse(nfsURL)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to parse NFS URL %q: %w", nfsURL, err)
	}
	host = u.Hostname()
	port = u.Port()
	if port == "" {
		port = "2049"
	}
	path = u.Path
	if path == "" {
		path = "/"
	}
	return host, port, path, nil
}
