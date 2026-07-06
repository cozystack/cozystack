package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	kubevirtv1 "kubevirt.io/api/core/v1"
)

var (
	watchNamespace      string
	watchInterval       int
	namePrefix          string
	defaultInstanceType string
	defaultPreference   string
)

const (
	// Forklift labels stamped on migrated VMs (release-2.11+).
	// NOTE: the "plan" label value is the Plan UID, not its name; resolve
	// it to a name with resolvePlan before looking up the Plan/Migration.
	forkliftPlanLabel = "plan"
	forkliftVMLabel   = "vmID"

	// Label recording the source Forklift plan name on the adopted VMInstance.
	importPlanLabel = "vm-import.cozystack.io/plan"

	// Cozystack adoption labels
	adoptedLabel       = "cozystack.io/adopted"
	adoptedSourceLabel = "cozystack.io/source"
	adoptedByLabel     = "cozystack.io/adopted-by"

	// VMInstance GVR
	vmInstanceGroup   = "apps.cozystack.io"
	vmInstanceVersion = "v1alpha1"
	vmInstanceKind    = "VMInstance"

	// VMDisk GVR
	vmDiskGroup   = "apps.cozystack.io"
	vmDiskVersion = "v1alpha1"
	vmDiskKind    = "VMDisk"

	// Cache TTL
	planCacheTTL = 5 * time.Minute
)

func main() {
	klog.InitFlags(nil)
	flag.StringVar(&watchNamespace, "namespace", "", "Namespace to watch (empty = all namespaces)")
	flag.IntVar(&watchInterval, "watch-interval", 15, "Watch interval in seconds")
	flag.StringVar(&namePrefix, "name-prefix", "", "Prefix for created VMInstance names")
	flag.StringVar(&defaultInstanceType, "default-instance-type", "u1.medium", "Default instance type if not specified in VM")
	flag.StringVar(&defaultPreference, "default-preference", "ubuntu", "Default preference if not specified in VM")
	flag.Parse()

	klog.Info("Starting VM Adoption Controller")
	klog.Infof("Watch namespace: %s (empty = all)", watchNamespace)
	klog.Infof("Watch interval: %d seconds", watchInterval)
	klog.Infof("Name prefix: %s", namePrefix)
	klog.Infof("Default instance type: %s", defaultInstanceType)
	klog.Infof("Default preference: %s", defaultPreference)

	// Create in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		klog.Fatalf("Failed to create in-cluster config: %v", err)
	}

	// Create clientsets
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatalf("Failed to create kubernetes clientset: %v", err)
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		klog.Fatalf("Failed to create dynamic client: %v", err)
	}

	controller := &AdoptionController{
		clientset:     clientset,
		dynamicClient: dynamicClient,
		planCache:     make(map[string]*PlanCacheEntry),
	}

	// Graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		klog.Infof("Received signal %v, shutting down", sig)
		cancel()
	}()

	// Health check endpoint
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		})
		mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		})
		server := &http.Server{Addr: ":8081", Handler: mux}
		go func() {
			<-ctx.Done()
			server.Shutdown(context.Background())
		}()
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			klog.Errorf("Health server error: %v", err)
		}
	}()

	// Run controller
	controller.Run(ctx)
}

type AdoptionController struct {
	clientset     *kubernetes.Clientset
	dynamicClient dynamic.Interface
	planCache     map[string]*PlanCacheEntry
	cacheMutex    sync.RWMutex
}

type PlanCacheEntry struct {
	AdoptionEnabled bool
	CachedAt        time.Time
}

func (c *AdoptionController) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(watchInterval) * time.Second)
	defer ticker.Stop()

	klog.Info("Controller started, watching for VirtualMachines created by Forklift...")

	// Run immediately on start
	c.reconcile(ctx)

	for {
		select {
		case <-ctx.Done():
			klog.Info("Shutting down controller")
			return
		case <-ticker.C:
			c.reconcile(ctx)
		}
	}
}

func (c *AdoptionController) reconcile(ctx context.Context) {
	klog.V(2).Info("Running reconciliation loop...")

	// Purge expired cache entries
	c.purgeExpiredCache()

	// Get VirtualMachines with Forklift labels
	vms, err := c.getForkliftVMs(ctx)
	if err != nil {
		klog.Errorf("Failed to list VirtualMachines: %v", err)
		return
	}

	klog.V(2).Infof("Found %d VirtualMachines pending adoption", len(vms))

	for _, vm := range vms {
		if err := c.adoptVM(ctx, vm); err != nil {
			klog.Errorf("Failed to adopt VM %s/%s: %v", vm.Namespace, vm.Name, err)
		}
	}
}

func (c *AdoptionController) purgeExpiredCache() {
	c.cacheMutex.Lock()
	defer c.cacheMutex.Unlock()

	for key, entry := range c.planCache {
		if time.Since(entry.CachedAt) >= planCacheTTL {
			delete(c.planCache, key)
		}
	}
}

func (c *AdoptionController) getForkliftVMs(ctx context.Context) ([]kubevirtv1.VirtualMachine, error) {
	// Use dynamic client to list VMs since kubevirt client is complex to set up
	gvr := schema.GroupVersionResource{
		Group:    "kubevirt.io",
		Version:  "v1",
		Resource: "virtualmachines",
	}

	var listOptions metav1.ListOptions
	listOptions.LabelSelector = forkliftPlanLabel

	var list *unstructured.UnstructuredList
	var err error

	if watchNamespace != "" {
		list, err = c.dynamicClient.Resource(gvr).Namespace(watchNamespace).List(ctx, listOptions)
	} else {
		list, err = c.dynamicClient.Resource(gvr).List(ctx, listOptions)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list VirtualMachines: %w", err)
	}

	var vms []kubevirtv1.VirtualMachine
	for _, item := range list.Items {
		// Check if already adopted
		labels := item.GetLabels()
		if labels != nil && labels[adoptedLabel] == "true" {
			klog.V(3).Infof("VM %s/%s already adopted, skipping", item.GetNamespace(), item.GetName())
			continue
		}

		// Resolve the Plan name from the Plan UID Forklift stamps on the VM.
		planUID := labels[forkliftPlanLabel]
		if planUID == "" {
			klog.V(2).Infof("VM %s/%s has no forklift plan label, skipping", item.GetNamespace(), item.GetName())
			continue
		}

		planName, planNamespace, ok := c.resolvePlan(ctx, planUID)
		if !ok {
			klog.V(2).Infof("VM %s/%s: no Plan found for UID %s, skipping", item.GetNamespace(), item.GetName(), planUID)
			continue
		}

		if !c.isAdoptionEnabled(ctx, planNamespace, planName) {
			klog.V(2).Infof("VM %s/%s: adoption disabled on plan %s/%s, skipping", item.GetNamespace(), item.GetName(), planNamespace, planName)
			continue
		}

		// Check if the Forklift migration is complete before adopting
		if !c.isMigrationComplete(ctx, planNamespace, planName) {
			klog.V(2).Infof("VM %s/%s: migration not complete for plan %s/%s, skipping", item.GetNamespace(), item.GetName(), planNamespace, planName)
			continue
		}

		// Convert to typed VM (only name/namespace/labels — full spec fetched in adoptVM)
		vms = append(vms, kubevirtv1.VirtualMachine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      item.GetName(),
				Namespace: item.GetNamespace(),
				Labels:    item.GetLabels(),
			},
		})
	}

	return vms, nil
}

// resolvePlan resolves the Forklift Plan from the Plan UID that Forklift
// stamps on migrated VMs via the "plan" label. The search is cluster-wide
// because the Plan may live in a different namespace than the migrated VM
// (e.g. when the Plan targets a tenant namespace directly via
// Plan.spec.targetNamespace). Returns the Plan name, its namespace, and true
// when a Plan with that UID exists.
func (c *AdoptionController) resolvePlan(ctx context.Context, planUID string) (string, string, bool) {
	gvr := schema.GroupVersionResource{
		Group:    "forklift.konveyor.io",
		Version:  "v1beta1",
		Resource: "plans",
	}
	list, err := c.dynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.V(2).Infof("Failed to list Plans: %v", err)
		return "", "", false
	}
	for _, p := range list.Items {
		if string(p.GetUID()) == planUID {
			return p.GetName(), p.GetNamespace(), true
		}
	}
	return "", "", false
}

// isMigrationComplete checks that the Forklift Migration for this plan has
// finished successfully. This prevents adopting VMs whose DataVolumes are
// still being transferred.
func (c *AdoptionController) isMigrationComplete(ctx context.Context, namespace, planName string) bool {
	migrationGVR := schema.GroupVersionResource{
		Group:    "forklift.konveyor.io",
		Version:  "v1beta1",
		Resource: "migrations",
	}

	migration, err := c.dynamicClient.Resource(migrationGVR).Namespace(namespace).Get(ctx, planName, metav1.GetOptions{})
	if err != nil {
		klog.V(2).Infof("Failed to get Migration %s/%s: %v (skipping adoption)", namespace, planName, err)
		return false
	}

	// Check status.conditions for type "Succeeded" with status "True"
	conditions, found, _ := unstructured.NestedSlice(migration.Object, "status", "conditions")
	if !found {
		klog.V(2).Infof("Migration %s/%s has no status conditions yet", namespace, planName)
		return false
	}

	for _, cond := range conditions {
		condMap, ok := cond.(map[string]interface{})
		if !ok {
			continue
		}
		condType, _, _ := unstructured.NestedString(condMap, "type")
		condStatus, _, _ := unstructured.NestedString(condMap, "status")
		if condType == "Succeeded" && condStatus == "True" {
			klog.V(2).Infof("Migration %s/%s is complete", namespace, planName)
			return true
		}
	}

	klog.V(2).Infof("Migration %s/%s is not yet complete", namespace, planName)
	return false
}

func (c *AdoptionController) isAdoptionEnabled(ctx context.Context, namespace, planName string) bool {
	cacheKey := fmt.Sprintf("%s/%s", namespace, planName)

	// Check cache first
	c.cacheMutex.RLock()
	if entry, ok := c.planCache[cacheKey]; ok {
		if time.Since(entry.CachedAt) < planCacheTTL {
			c.cacheMutex.RUnlock()
			klog.V(3).Infof("Plan %s adoption setting from cache: %v", cacheKey, entry.AdoptionEnabled)
			return entry.AdoptionEnabled
		}
	}
	c.cacheMutex.RUnlock()

	// Get Plan resource
	gvr := schema.GroupVersionResource{
		Group:    "forklift.konveyor.io",
		Version:  "v1beta1",
		Resource: "plans",
	}

	plan, err := c.dynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, planName, metav1.GetOptions{})
	if err != nil {
		klog.Warningf("Failed to get Plan %s/%s: %v (defaulting to adoption disabled)", namespace, planName, err)
		return false // Default to disabled if we can't check - avoid adopting VMs unintentionally
	}

	annotations := plan.GetAnnotations()
	enabled := true // Default enabled
	if annotations != nil {
		if val, exists := annotations["vm-import.cozystack.io/adoption-enabled"]; exists {
			enabled = (val == "true")
		}
	}

	// Update cache
	c.cacheMutex.Lock()
	c.planCache[cacheKey] = &PlanCacheEntry{
		AdoptionEnabled: enabled,
		CachedAt:        time.Now(),
	}
	c.cacheMutex.Unlock()

	klog.V(2).Infof("Plan %s adoption setting: %v", cacheKey, enabled)
	return enabled
}

// getTargetNamespace returns the namespace the VMInstance should be created in.
// Forklift conversion runs in a privileged system namespace, but the managed
// VMInstance must land in the user's tenant. The target tenant is recorded on
// the Plan via the `vm-import.cozystack.io/target-namespace` annotation.
// Defaults to the VM's own namespace (same-namespace adoption) when unset.
func (c *AdoptionController) getTargetNamespace(ctx context.Context, namespace, planName string) string {
	gvr := schema.GroupVersionResource{Group: "forklift.konveyor.io", Version: "v1beta1", Resource: "plans"}
	plan, err := c.dynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, planName, metav1.GetOptions{})
	if err != nil {
		return namespace
	}
	if ann := plan.GetAnnotations(); ann != nil {
		if ns := ann["vm-import.cozystack.io/target-namespace"]; ns != "" {
			return ns
		}
	}
	return namespace
}

// getPlanPreset returns the optional instance type and preference chosen on the
// VMImport, recorded on the Plan via the `vm-import.cozystack.io/instance-type`
// and `vm-import.cozystack.io/instance-profile` annotations. Empty strings mean
// "not set" (the caller then falls back to the migrated VM's values or the
// controller defaults).
func (c *AdoptionController) getPlanPreset(ctx context.Context, namespace, planName string) (string, string) {
	gvr := schema.GroupVersionResource{Group: "forklift.konveyor.io", Version: "v1beta1", Resource: "plans"}
	plan, err := c.dynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, planName, metav1.GetOptions{})
	if err != nil {
		return "", ""
	}
	ann := plan.GetAnnotations()
	if ann == nil {
		return "", ""
	}
	return ann["vm-import.cozystack.io/instance-type"], ann["vm-import.cozystack.io/instance-profile"]
}

// cloneDataVolume creates a CDI cross-namespace clone of srcName (in srcNs) into
// dstNs as dstName, sized from the source PVC. Idempotent: a no-op if the target
// DataVolume already exists.
func (c *AdoptionController) cloneDataVolume(ctx context.Context, srcNs, srcName, dstNs, dstName string) error {
	dvGVR := schema.GroupVersionResource{Group: "cdi.kubevirt.io", Version: "v1beta1", Resource: "datavolumes"}
	if _, err := c.dynamicClient.Resource(dvGVR).Namespace(dstNs).Get(ctx, dstName, metav1.GetOptions{}); err == nil {
		return nil
	}
	pvcGVR := schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}
	srcPVC, err := c.dynamicClient.Resource(pvcGVR).Namespace(srcNs).Get(ctx, srcName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("source PVC %s/%s not found: %w", srcNs, srcName, err)
	}
	size, _, _ := unstructured.NestedString(srcPVC.Object, "spec", "resources", "requests", "storage")
	sc, _, _ := unstructured.NestedString(srcPVC.Object, "spec", "storageClassName")
	volMode, _, _ := unstructured.NestedString(srcPVC.Object, "spec", "volumeMode")
	accessModes, _, _ := unstructured.NestedStringSlice(srcPVC.Object, "spec", "accessModes")
	storage := map[string]interface{}{
		"resources": map[string]interface{}{"requests": map[string]interface{}{"storage": size}},
	}
	if sc != "" {
		storage["storageClassName"] = sc
	}
	if volMode != "" {
		storage["volumeMode"] = volMode
	}
	if len(accessModes) > 0 {
		am := make([]interface{}, len(accessModes))
		for i, a := range accessModes {
			am[i] = a
		}
		storage["accessModes"] = am
	}
	dv := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cdi.kubevirt.io/v1beta1",
		"kind":       "DataVolume",
		"metadata":   map[string]interface{}{"name": dstName, "namespace": dstNs},
		"spec": map[string]interface{}{
			"source":  map[string]interface{}{"pvc": map[string]interface{}{"namespace": srcNs, "name": srcName}},
			"storage": storage,
		},
	}}
	if _, err := c.dynamicClient.Resource(dvGVR).Namespace(dstNs).Create(ctx, dv, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("failed to create clone DataVolume %s/%s: %w", dstNs, dstName, err)
	}
	klog.Infof("Cloning disk %s/%s -> %s/%s", srcNs, srcName, dstNs, dstName)
	return nil
}

// ensureVMDisk creates a Cozystack VMDisk that adopts an imported PVC by cloning
// it (source.pvc). This produces a first-class, dashboard-managed disk
// (DataVolume `vm-disk-<name>` with the apps.cozystack.io/VMDisk labels) instead
// of a raw Forklift populator PVC. Size and StorageClass are inherited from the
// source PVC. Idempotent. Returns the resulting DataVolume name to reference
// from the VMInstance (`vm-disk-<vmDiskName>`).
func (c *AdoptionController) ensureVMDisk(ctx context.Context, vmDiskNs, vmDiskName, srcPVCNs, srcPVCName string) (string, error) {
	dvName := "vm-disk-" + vmDiskName
	if len(dvName) > 63 {
		return "", fmt.Errorf("VMDisk DataVolume name %q exceeds 63 characters", dvName)
	}
	vmDiskGVR := schema.GroupVersionResource{Group: vmDiskGroup, Version: vmDiskVersion, Resource: "vmdisks"}
	if _, err := c.dynamicClient.Resource(vmDiskGVR).Namespace(vmDiskNs).Get(ctx, vmDiskName, metav1.GetOptions{}); err == nil {
		return dvName, nil // already created on a previous reconcile
	}
	// Inherit size + storageClass from the source PVC.
	pvcGVR := schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}
	srcPVC, err := c.dynamicClient.Resource(pvcGVR).Namespace(srcPVCNs).Get(ctx, srcPVCName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("source PVC %s/%s not found: %w", srcPVCNs, srcPVCName, err)
	}
	size, _, _ := unstructured.NestedString(srcPVC.Object, "spec", "resources", "requests", "storage")
	sc, _, _ := unstructured.NestedString(srcPVC.Object, "spec", "storageClassName")
	spec := map[string]interface{}{
		"source": map[string]interface{}{
			"pvc": map[string]interface{}{"name": srcPVCName, "namespace": srcPVCNs},
		},
	}
	if size != "" {
		spec["storage"] = size
	}
	if sc != "" {
		spec["storageClass"] = sc
	}
	vmDisk := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": fmt.Sprintf("%s/%s", vmDiskGroup, vmDiskVersion),
		"kind":       vmDiskKind,
		"metadata": map[string]interface{}{
			"name":      vmDiskName,
			"namespace": vmDiskNs,
			"labels":    map[string]interface{}{adoptedSourceLabel: "vm-import"},
			"annotations": map[string]interface{}{
				"vm-import.cozystack.io/source-pvc": srcPVCNs + "/" + srcPVCName,
			},
		},
		"spec": spec,
	}}
	if _, err := c.dynamicClient.Resource(vmDiskGVR).Namespace(vmDiskNs).Create(ctx, vmDisk, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("failed to create VMDisk %s/%s: %w", vmDiskNs, vmDiskName, err)
	}
	klog.Infof("Created VMDisk %s/%s cloning PVC %s/%s", vmDiskNs, vmDiskName, srcPVCNs, srcPVCName)
	return dvName, nil
}

// wrapDisksAsVMDisks turns the raw imported PVC reference of each disk into a
// managed Cozystack VMDisk (clone) in the target namespace, rewriting each
// disk's dvName to the VMDisk's DataVolume (`vm-disk-<vmInstance>-<disk>`).
func (c *AdoptionController) wrapDisksAsVMDisks(ctx context.Context, srcNs, targetNs, vmInstanceName string, disks []interface{}) error {
	for _, d := range disks {
		dm, ok := d.(map[string]interface{})
		if !ok {
			continue
		}
		srcPVC, _ := dm["dvName"].(string)
		diskName, _ := dm["name"].(string)
		if srcPVC == "" {
			continue
		}
		vmDiskName := vmInstanceName + "-" + diskName
		dvName, err := c.ensureVMDisk(ctx, targetNs, vmDiskName, srcNs, srcPVC)
		if err != nil {
			return err
		}
		dm["dvName"] = dvName
	}
	return nil
}

func (c *AdoptionController) adoptVM(ctx context.Context, vm kubevirtv1.VirtualMachine) error {
	klog.Infof("Adopting VM %s/%s into Cozystack...", vm.Namespace, vm.Name)

	// Get full VM spec
	vmGVR := schema.GroupVersionResource{
		Group:    "kubevirt.io",
		Version:  "v1",
		Resource: "virtualmachines",
	}

	vmUnstructured, err := c.dynamicClient.Resource(vmGVR).Namespace(vm.Namespace).Get(ctx, vm.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get VM details: %w", err)
	}

	// Extract VM spec
	spec, found, err := unstructured.NestedMap(vmUnstructured.Object, "spec")
	if err != nil {
		return fmt.Errorf("failed to get VM spec: %w", err)
	}
	if !found {
		return fmt.Errorf("VM %s/%s has no spec field", vm.Namespace, vm.Name)
	}

	// Resolve the Plan once (cluster-wide by UID — it may live in a different
	// namespace than the VM) and read its optional preset overrides.
	planName, planNamespace, ok := c.resolvePlan(ctx, vm.Labels[forkliftPlanLabel])
	if !ok {
		return fmt.Errorf("could not resolve Forklift Plan for UID %q", vm.Labels[forkliftPlanLabel])
	}
	presetInstanceType, presetPreference := c.getPlanPreset(ctx, planNamespace, planName)

	// Extract running state — check runStrategy first (modern), then running (deprecated)
	runStrategy := "Always"
	rs, rsFound, _ := unstructured.NestedString(spec, "runStrategy")
	if rsFound && rs != "" {
		runStrategy = rs
	} else {
		running, runningFound, _ := unstructured.NestedBool(spec, "running")
		if runningFound {
			if running {
				runStrategy = "Always"
			} else {
				runStrategy = "Halted"
			}
		}
		// If neither found, default remains "Always"
	}

	// Resolve the instance type. Precedence: VMImport preset (Plan annotation) >
	// instance type set on the migrated VM > controller default flag.
	instanceType, _, _ := unstructured.NestedString(spec, "instancetype", "name")
	if presetInstanceType != "" {
		instanceType = presetInstanceType
		klog.Infof("VM %s/%s: using VMImport preset instanceType=%s", vm.Namespace, vm.Name, instanceType)
	} else if instanceType == "" {
		instanceType = defaultInstanceType
		klog.Infof("VM %s/%s: using default instanceType=%s", vm.Namespace, vm.Name, defaultInstanceType)
	}

	// Resolve the preference with the same precedence.
	preference, _, _ := unstructured.NestedString(spec, "preference", "name")
	if presetPreference != "" {
		preference = presetPreference
		klog.Infof("VM %s/%s: using VMImport preset preference=%s", vm.Namespace, vm.Name, preference)
	} else if preference == "" {
		preference = defaultPreference
		klog.Infof("VM %s/%s: using default preference=%s", vm.Namespace, vm.Name, defaultPreference)
	}

	// Extract template.spec — fail if missing to avoid creating a VMInstance with no disks
	template, templateFound, _ := unstructured.NestedMap(spec, "template")
	if !templateFound || template == nil {
		return fmt.Errorf("VM %s/%s has no spec.template", vm.Namespace, vm.Name)
	}
	templateSpec, tsFound, _ := unstructured.NestedMap(template, "spec")
	if !tsFound || templateSpec == nil {
		return fmt.Errorf("VM %s/%s has no spec.template.spec", vm.Namespace, vm.Name)
	}

	// Extract disks with safe type assertions
	volumes, _, _ := unstructured.NestedSlice(templateSpec, "volumes")

	var disks []interface{}
	var dvNames []string // Track DataVolume names for Helm labeling
	diskNames := make(map[string]bool)
	diskIndex := 0
	for i, vol := range volumes {
		volMap, ok := vol.(map[string]interface{})
		if !ok {
			klog.V(2).Infof("VM %s/%s: skipping volume %d: unexpected type %T", vm.Namespace, vm.Name, i, vol)
			continue
		}

		// The source disk is backed either by a DataVolume (classic Forklift
		// flow) or by a PVC populated by the CDI volume populator (the flow
		// Forklift uses when it creates the VM itself). Resolve the source
		// name for both; cloneDataVolume clones from a PVC source either way.
		var dvName string
		if dv, hasDV := volMap["dataVolume"]; hasDV {
			dvMap, ok := dv.(map[string]interface{})
			if !ok {
				klog.V(2).Infof("VM %s/%s: skipping volume %d: dataVolume has unexpected type %T", vm.Namespace, vm.Name, i, dv)
				continue
			}
			dvName, ok = dvMap["name"].(string)
			if !ok || dvName == "" {
				klog.V(2).Infof("VM %s/%s: skipping volume %d: dataVolume has no name", vm.Namespace, vm.Name, i)
				continue
			}
		} else if pvc, hasPVC := volMap["persistentVolumeClaim"]; hasPVC {
			pvcMap, ok := pvc.(map[string]interface{})
			if !ok {
				klog.V(2).Infof("VM %s/%s: skipping volume %d: persistentVolumeClaim has unexpected type %T", vm.Namespace, vm.Name, i, pvc)
				continue
			}
			dvName, ok = pvcMap["claimName"].(string)
			if !ok || dvName == "" {
				klog.V(2).Infof("VM %s/%s: skipping volume %d: persistentVolumeClaim has no claimName", vm.Namespace, vm.Name, i)
				continue
			}
		} else {
			klog.V(3).Infof("VM %s/%s: skipping volume %d: no dataVolume or persistentVolumeClaim", vm.Namespace, vm.Name, i)
			continue
		}

		// Generate a unique disk name to avoid collisions in the VMInstance spec
		diskName := fmt.Sprintf("imported-%d", diskIndex)
		diskIndex++

		if diskNames[diskName] {
			klog.Warningf("VM %s/%s: duplicate disk name %s, skipping", vm.Namespace, vm.Name, diskName)
			continue
		}
		diskNames[diskName] = true

		disks = append(disks, map[string]interface{}{
			"name":   diskName,
			"dvName": dvName,
			"bus":    "virtio",
		})
		dvNames = append(dvNames, dvName)
		klog.V(3).Infof("VM %s/%s: added disk %s (dvName=%s)", vm.Namespace, vm.Name, diskName, dvName)
	}

	// Extract Multus networks from VM spec
	sourceNetworks, _, _ := unstructured.NestedSlice(templateSpec, "networks")
	var mappedNetworks []interface{}
	for i, net := range sourceNetworks {
		netMap, ok := net.(map[string]interface{})
		if !ok {
			klog.V(2).Infof("VM %s/%s: skipping network %d: unexpected type %T", vm.Namespace, vm.Name, i, net)
			continue
		}

		multus, hasMultus := netMap["multus"]
		if !hasMultus {
			// Pod network or other type — skip (pod network is always added by vm-instance template)
			continue
		}

		multusMap, ok := multus.(map[string]interface{})
		if !ok {
			klog.V(2).Infof("VM %s/%s: skipping network %d: multus has unexpected type %T", vm.Namespace, vm.Name, i, multus)
			continue
		}

		networkName, ok := multusMap["networkName"].(string)
		if !ok || networkName == "" {
			klog.V(2).Infof("VM %s/%s: skipping network %d: multus has no networkName", vm.Namespace, vm.Name, i)
			continue
		}

		// networkName format is "namespace/name" — extract just the name part
		// since the vm-instance template re-adds the namespace prefix
		netRef := networkName
		if idx := strings.LastIndex(networkName, "/"); idx >= 0 {
			netRef = networkName[idx+1:]
		}

		mappedNetworks = append(mappedNetworks, map[string]interface{}{
			"name": netRef,
		})
		klog.V(3).Infof("VM %s/%s: added network %s (from %s)", vm.Namespace, vm.Name, netRef, networkName)
	}

	// Map the source firmware (UEFI/BIOS) so guests installed in UEFI mode
	// boot correctly. Forklift sets domain.firmware.bootloader on the imported
	// VM; Helm adoption re-renders the VM from the chart, so the boot mode must
	// be carried through VMInstance.spec.firmware.
	// Depends on the vm-instance `firmware` API (cozystack/cozystack#3002);
	// older vm-instance charts ignore the field (backward compatible).
	var firmware map[string]interface{}
	if domain, ok, _ := unstructured.NestedMap(templateSpec, "domain"); ok && domain != nil {
		if bootloader, ok, _ := unstructured.NestedMap(domain, "firmware", "bootloader"); ok && bootloader != nil {
			if efi, hasEFI := bootloader["efi"]; hasEFI {
				firmware = map[string]interface{}{"bootloader": "uefi"}
				if efiMap, ok := efi.(map[string]interface{}); ok {
					if sb, ok := efiMap["secureBoot"].(bool); ok && sb {
						firmware["secureBoot"] = true
					}
				}
			} else if _, hasBIOS := bootloader["bios"]; hasBIOS {
				firmware = map[string]interface{}{"bootloader": "bios"}
			}
		}
	}

	klog.Infof("VM %s/%s: extracted %d disk(s), %d network(s), instanceType=%s, preference=%s, runStrategy=%s, firmware=%v",
		vm.Namespace, vm.Name, len(disks), len(mappedNetworks), instanceType, preference, runStrategy, firmware)

	// Create VMInstance name
	vmInstanceName := vm.Name
	if namePrefix != "" {
		vmInstanceName = namePrefix + vm.Name
	}

	// Validate Kubernetes name length
	if len(vmInstanceName) > 63 {
		return fmt.Errorf("VMInstance name %q exceeds 63 characters", vmInstanceName)
	}

	// The HelmRelease name is derived from the ApplicationDefinition prefix + VMInstance name
	helmReleaseName := "vm-instance-" + vmInstanceName

	if len(helmReleaseName) > 63 {
		return fmt.Errorf("HelmRelease name %q exceeds 63 characters", helmReleaseName)
	}

	// Adopt each imported disk as a managed Cozystack VMDisk (clone via
	// source.pvc) so it becomes a first-class, dashboard-managed resource
	// (`vm-disk-<name>`) instead of a raw Forklift populator PVC. The disks'
	// dvName is rewritten to the VMDisk's DataVolume.
	targetNamespace := c.getTargetNamespace(ctx, planNamespace, planName)
	if err := c.wrapDisksAsVMDisks(ctx, vm.Namespace, targetNamespace, vmInstanceName, disks); err != nil {
		return fmt.Errorf("failed to wrap imported disks as VMDisks: %w", err)
	}

	return c.adoptVMViaVMDisks(ctx, vm, targetNamespace, vmInstanceName,
		disks, mappedNetworks, firmware, instanceType, preference, runStrategy, planName)
}

// adoptVMViaVMDisks creates the managed VMInstance in the target namespace
// referencing the VMDisks produced by wrapDisksAsVMDisks. The Forklift-created
// VM is removed (same namespace) or labeled adopted (different namespace) so it
// is not reprocessed; the imported PVC it referenced lives on only as the clone
// source of the VMDisk.
func (c *AdoptionController) adoptVMViaVMDisks(ctx context.Context, vm kubevirtv1.VirtualMachine,
	targetNamespace, vmInstanceName string, disks, networks []interface{},
	firmware map[string]interface{}, instanceType, preference, runStrategy, planName string) error {

	vmInstanceGVR := schema.GroupVersionResource{Group: vmInstanceGroup, Version: vmInstanceVersion, Resource: "vminstances"}

	// Idempotency: if the VMInstance already exists, just mark the source adopted.
	if _, err := c.dynamicClient.Resource(vmInstanceGVR).Namespace(targetNamespace).Get(ctx, vmInstanceName, metav1.GetOptions{}); err == nil {
		klog.Infof("VMInstance %s/%s already exists, ensuring source VM is handled", targetNamespace, vmInstanceName)
		if targetNamespace == vm.Namespace {
			return nil
		}
		return c.labelVMAsAdopted(ctx, vm.Namespace, vm.Name, vmInstanceName, "vm-instance-"+vmInstanceName)
	}

	// Same-namespace adoption: the managed VMInstance renders a VirtualMachine
	// with the same name as the Forklift VM, so remove the Forklift VM first to
	// avoid a name collision. The imported PVC stays (it is the VMDisk clone
	// source); KubeVirt does not delete PVCs when a VM is deleted.
	sourceVMRemoved := false
	if targetNamespace == vm.Namespace {
		vmGVR := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
		if err := c.dynamicClient.Resource(vmGVR).Namespace(vm.Namespace).Delete(ctx, vm.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to remove Forklift VM %s/%s before adoption: %w", vm.Namespace, vm.Name, err)
		}
		sourceVMRemoved = true
		klog.Infof("Removed Forklift VM %s/%s (replaced by managed VMInstance)", vm.Namespace, vm.Name)
	}

	spec := map[string]interface{}{
		"fullnameOverride": vmInstanceName,
		"runStrategy":      runStrategy,
		"instanceType":     instanceType,
		"instanceProfile":  preference,
		"disks":            disks,
		"external":         false,
		"externalMethod":   "PortList",
		"externalPorts":    []interface{}{int64(22)},
		"gpus":             []interface{}{},
		"resources":        map[string]interface{}{},
		"sshKeys":          []interface{}{},
		"networks":         networks,
		"cloudInit":        "",
		"cloudInitSeed":    "",
	}
	if firmware != nil {
		spec["firmware"] = firmware
	}

	vmInstance := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": fmt.Sprintf("%s/%s", vmInstanceGroup, vmInstanceVersion),
		"kind":       vmInstanceKind,
		"metadata": map[string]interface{}{
			"name":      vmInstanceName,
			"namespace": targetNamespace,
			"labels": map[string]interface{}{
				adoptedSourceLabel: "vm-import",
				importPlanLabel:    planName,
			},
			"annotations": map[string]interface{}{
				"vm-import.cozystack.io/original-vm-name":      vm.Name,
				"vm-import.cozystack.io/original-vm-namespace": vm.Namespace,
				"vm-import.cozystack.io/adopted-at":            time.Now().Format(time.RFC3339),
			},
		},
		"spec": spec,
	}}

	if _, err := c.dynamicClient.Resource(vmInstanceGVR).Namespace(targetNamespace).Create(ctx, vmInstance, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("failed to create VMInstance %s/%s: %w", targetNamespace, vmInstanceName, err)
	}
	klog.Infof("Created VMInstance %s/%s (from VM %s/%s)", targetNamespace, vmInstanceName, vm.Namespace, vm.Name)

	// Mark the source VM as adopted so it is not reprocessed (only relevant when
	// it still exists, i.e. a different namespace than the managed VMInstance).
	if !sourceVMRemoved {
		if err := c.labelVMAsAdopted(ctx, vm.Namespace, vm.Name, vmInstanceName, "vm-instance-"+vmInstanceName); err != nil {
			klog.Warningf("VMInstance created but failed to label source VM %s/%s as adopted: %v", vm.Namespace, vm.Name, err)
		}
	}
	return nil
}

// prepareVMForHelmAdoption adds Helm metadata labels and annotations to an existing
// VirtualMachine so that Helm will adopt it as a managed resource instead of
// failing with "already exists".
func (c *AdoptionController) prepareVMForHelmAdoption(ctx context.Context, namespace, vmName, helmReleaseName string) error {
	vmGVR := schema.GroupVersionResource{
		Group:    "kubevirt.io",
		Version:  "v1",
		Resource: "virtualmachines",
	}

	vm, err := c.dynamicClient.Resource(vmGVR).Namespace(namespace).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get VM: %w", err)
	}

	// Add Helm ownership labels
	labels := vm.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels["app.kubernetes.io/managed-by"] = "Helm"
	vm.SetLabels(labels)

	// Add Helm release annotations
	annotations := vm.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations["meta.helm.sh/release-name"] = helmReleaseName
	annotations["meta.helm.sh/release-namespace"] = namespace
	vm.SetAnnotations(annotations)

	_, err = c.dynamicClient.Resource(vmGVR).Namespace(namespace).Update(ctx, vm, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to add Helm metadata to VM: %w", err)
	}

	klog.Infof("Prepared VM %s/%s for Helm adoption (release=%s)", namespace, vmName, helmReleaseName)
	return nil
}

// prepareDataVolumesForHelmAdoption adds Helm metadata labels and annotations
// to existing DataVolumes so that Helm will adopt them.
func (c *AdoptionController) prepareDataVolumesForHelmAdoption(ctx context.Context, namespace string, dvNames []string, helmReleaseName string) error {
	dvGVR := schema.GroupVersionResource{
		Group:    "cdi.kubevirt.io",
		Version:  "v1beta1",
		Resource: "datavolumes",
	}

	for _, dvName := range dvNames {
		dv, err := c.dynamicClient.Resource(dvGVR).Namespace(namespace).Get(ctx, dvName, metav1.GetOptions{})
		if err != nil {
			klog.Warningf("DataVolume %s/%s not found, skipping Helm labeling: %v", namespace, dvName, err)
			continue
		}

		labels := dv.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		labels["app.kubernetes.io/managed-by"] = "Helm"
		dv.SetLabels(labels)

		annotations := dv.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations["meta.helm.sh/release-name"] = helmReleaseName
		annotations["meta.helm.sh/release-namespace"] = namespace
		dv.SetAnnotations(annotations)

		_, err = c.dynamicClient.Resource(dvGVR).Namespace(namespace).Update(ctx, dv, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to add Helm metadata to DataVolume %s: %w", dvName, err)
		}

		klog.Infof("Prepared DataVolume %s/%s for Helm adoption (release=%s)", namespace, dvName, helmReleaseName)
	}

	return nil
}

// removeHelmLabelsFromVM removes Helm metadata from a VM (for rollback).
func (c *AdoptionController) removeHelmLabelsFromVM(ctx context.Context, namespace, vmName string) error {
	vmGVR := schema.GroupVersionResource{
		Group:    "kubevirt.io",
		Version:  "v1",
		Resource: "virtualmachines",
	}

	vm, err := c.dynamicClient.Resource(vmGVR).Namespace(namespace).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get VM for rollback: %w", err)
	}

	labels := vm.GetLabels()
	if labels != nil {
		delete(labels, "app.kubernetes.io/managed-by")
		vm.SetLabels(labels)
	}

	annotations := vm.GetAnnotations()
	if annotations != nil {
		delete(annotations, "meta.helm.sh/release-name")
		delete(annotations, "meta.helm.sh/release-namespace")
		vm.SetAnnotations(annotations)
	}

	_, err = c.dynamicClient.Resource(vmGVR).Namespace(namespace).Update(ctx, vm, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to remove Helm metadata from VM: %w", err)
	}

	klog.Infof("Removed Helm metadata from VM %s/%s (rollback)", namespace, vmName)
	return nil
}

// removeHelmLabelsFromDataVolumes removes Helm metadata from DataVolumes (for rollback).
func (c *AdoptionController) removeHelmLabelsFromDataVolumes(ctx context.Context, namespace string, dvNames []string) error {
	dvGVR := schema.GroupVersionResource{
		Group:    "cdi.kubevirt.io",
		Version:  "v1beta1",
		Resource: "datavolumes",
	}

	for _, dvName := range dvNames {
		dv, err := c.dynamicClient.Resource(dvGVR).Namespace(namespace).Get(ctx, dvName, metav1.GetOptions{})
		if err != nil {
			klog.Warningf("DataVolume %s/%s not found during rollback: %v", namespace, dvName, err)
			continue
		}

		labels := dv.GetLabels()
		if labels != nil {
			delete(labels, "app.kubernetes.io/managed-by")
			dv.SetLabels(labels)
		}

		annotations := dv.GetAnnotations()
		if annotations != nil {
			delete(annotations, "meta.helm.sh/release-name")
			delete(annotations, "meta.helm.sh/release-namespace")
			dv.SetAnnotations(annotations)
		}

		_, err = c.dynamicClient.Resource(dvGVR).Namespace(namespace).Update(ctx, dv, metav1.UpdateOptions{})
		if err != nil {
			klog.Warningf("Failed to remove Helm metadata from DataVolume %s/%s: %v", namespace, dvName, err)
		}
	}

	return nil
}

func (c *AdoptionController) labelVMAsAdopted(ctx context.Context, namespace, vmName, vmInstanceName, helmReleaseName string) error {
	vmGVR := schema.GroupVersionResource{
		Group:    "kubevirt.io",
		Version:  "v1",
		Resource: "virtualmachines",
	}

	vm, err := c.dynamicClient.Resource(vmGVR).Namespace(namespace).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get VM: %w", err)
	}

	labels := vm.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[adoptedLabel] = "true"
	labels[adoptedByLabel] = vmInstanceName
	labels["app.kubernetes.io/managed-by"] = "Helm"
	vm.SetLabels(labels)

	annotations := vm.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations["meta.helm.sh/release-name"] = helmReleaseName
	annotations["meta.helm.sh/release-namespace"] = namespace
	vm.SetAnnotations(annotations)

	_, err = c.dynamicClient.Resource(vmGVR).Namespace(namespace).Update(ctx, vm, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update VM labels: %w", err)
	}

	klog.Infof("Labeled VM %s/%s as adopted by %s (release=%s)", namespace, vmName, vmInstanceName, helmReleaseName)
	return nil
}
