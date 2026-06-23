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
	// Forklift labels
	forkliftPlanLabel = "forklift.konveyor.io/plan"
	forkliftVMLabel   = "forklift.konveyor.io/vm-name"

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

		// Check adoption enabled annotation on the Plan
		planName := labels[forkliftPlanLabel]
		if planName == "" {
			klog.V(2).Infof("VM %s/%s has no forklift plan label, skipping", item.GetNamespace(), item.GetName())
			continue
		}

		if !c.isAdoptionEnabled(ctx, item.GetNamespace(), planName) {
			klog.V(2).Infof("VM %s/%s: adoption disabled on plan %s, skipping", item.GetNamespace(), item.GetName(), planName)
			continue
		}

		// Check if the Forklift migration is complete before adopting
		if !c.isMigrationComplete(ctx, item.GetNamespace(), planName) {
			klog.V(2).Infof("VM %s/%s: migration not complete for plan %s, skipping", item.GetNamespace(), item.GetName(), planName)
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

	// Extract instance type
	instanceType, _, _ := unstructured.NestedString(spec, "instancetype", "name")
	if instanceType == "" {
		instanceType = defaultInstanceType
		klog.Infof("VM %s/%s: using default instanceType=%s", vm.Namespace, vm.Name, defaultInstanceType)
	}

	// Extract preference
	preference, _, _ := unstructured.NestedString(spec, "preference", "name")
	if preference == "" {
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

		dv, hasDV := volMap["dataVolume"]
		if !hasDV {
			klog.V(3).Infof("VM %s/%s: skipping volume %d: no dataVolume", vm.Namespace, vm.Name, i)
			continue
		}

		dvMap, ok := dv.(map[string]interface{})
		if !ok {
			klog.V(2).Infof("VM %s/%s: skipping volume %d: dataVolume has unexpected type %T", vm.Namespace, vm.Name, i, dv)
			continue
		}

		dvNameRaw, hasName := dvMap["name"]
		if !hasName {
			klog.V(2).Infof("VM %s/%s: skipping volume %d: dataVolume has no name", vm.Namespace, vm.Name, i)
			continue
		}

		dvName, ok := dvNameRaw.(string)
		if !ok {
			klog.V(2).Infof("VM %s/%s: skipping volume %d: dataVolume name has unexpected type %T", vm.Namespace, vm.Name, i, dvNameRaw)
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

	// Check if VMInstance already exists
	vmInstanceGVR := schema.GroupVersionResource{
		Group:    vmInstanceGroup,
		Version:  vmInstanceVersion,
		Resource: "vminstances",
	}

	_, err = c.dynamicClient.Resource(vmInstanceGVR).Namespace(vm.Namespace).Get(ctx, vmInstanceName, metav1.GetOptions{})
	if err == nil {
		klog.Infof("VMInstance %s/%s already exists, ensuring VM is labeled", vm.Namespace, vmInstanceName)
		return c.labelVMAsAdopted(ctx, vm.Namespace, vm.Name, vmInstanceName, helmReleaseName)
	}

	// STEP 1: Label the existing Forklift VM and its DataVolumes with Helm
	// metadata so that Helm will adopt them instead of trying to create new ones.
	if err := c.prepareVMForHelmAdoption(ctx, vm.Namespace, vm.Name, helmReleaseName); err != nil {
		return fmt.Errorf("failed to prepare VM for Helm adoption: %w", err)
	}

	if err := c.prepareDataVolumesForHelmAdoption(ctx, vm.Namespace, dvNames, helmReleaseName); err != nil {
		// Rollback: remove Helm labels from the VM
		if rbErr := c.removeHelmLabelsFromVM(ctx, vm.Namespace, vm.Name); rbErr != nil {
			klog.Errorf("Failed to rollback Helm labels from VM %s/%s: %v", vm.Namespace, vm.Name, rbErr)
		}
		return fmt.Errorf("failed to prepare DataVolumes for Helm adoption: %w", err)
	}

	// STEP 2: Create VMInstance CRD which triggers HelmRelease creation.
	// fullnameOverride ensures the chart generates a VirtualMachine with the
	// same name as the existing Forklift VM, so Helm adopts it.
	vmInstance := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": fmt.Sprintf("%s/%s", vmInstanceGroup, vmInstanceVersion),
			"kind":       vmInstanceKind,
			"metadata": map[string]interface{}{
				"name":      vmInstanceName,
				"namespace": vm.Namespace,
				"labels": map[string]interface{}{
					adoptedSourceLabel: "vm-import",
					forkliftPlanLabel:  vm.Labels[forkliftPlanLabel],
				},
				"annotations": map[string]interface{}{
					"vm-import.cozystack.io/original-vm-name": vm.Name,
					"vm-import.cozystack.io/adopted-at":       time.Now().Format(time.RFC3339),
				},
			},
			"spec": map[string]interface{}{
				"fullnameOverride": vm.Name,
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
				"networks":         mappedNetworks,
				"cloudInit":        "",
				"cloudInitSeed":    "",
			},
		},
	}

	// Carry the source boot firmware through to the VMInstance (uefi/bios).
	if firmware != nil {
		if specMap, ok := vmInstance.Object["spec"].(map[string]interface{}); ok {
			specMap["firmware"] = firmware
		}
	}

	_, err = c.dynamicClient.Resource(vmInstanceGVR).Namespace(vm.Namespace).Create(ctx, vmInstance, metav1.CreateOptions{})
	if err != nil {
		// Rollback: remove Helm labels from VM and DataVolumes
		if rbErr := c.removeHelmLabelsFromVM(ctx, vm.Namespace, vm.Name); rbErr != nil {
			klog.Errorf("Failed to rollback Helm labels from VM %s/%s: %v", vm.Namespace, vm.Name, rbErr)
		}
		if rbErr := c.removeHelmLabelsFromDataVolumes(ctx, vm.Namespace, dvNames); rbErr != nil {
			klog.Errorf("Failed to rollback Helm labels from DataVolumes: %v", rbErr)
		}
		return fmt.Errorf("failed to create VMInstance: %w", err)
	}

	klog.Infof("Created VMInstance %s/%s", vm.Namespace, vmInstanceName)

	// STEP 3: Mark the VM as adopted
	if err := c.labelVMAsAdopted(ctx, vm.Namespace, vm.Name, vmInstanceName, helmReleaseName); err != nil {
		klog.Errorf("Failed to label VM %s/%s as adopted: %v, rolling back VMInstance", vm.Namespace, vm.Name, err)

		// Rollback: delete the VMInstance we just created
		deleteErr := c.dynamicClient.Resource(vmInstanceGVR).
			Namespace(vm.Namespace).
			Delete(ctx, vmInstanceName, metav1.DeleteOptions{})
		if deleteErr != nil {
			klog.Errorf("Failed to delete VMInstance %s/%s during rollback: %v", vm.Namespace, vmInstanceName, deleteErr)
		} else {
			klog.Infof("Rolled back VMInstance %s/%s", vm.Namespace, vmInstanceName)
		}

		// Also rollback Helm labels
		if rbErr := c.removeHelmLabelsFromVM(ctx, vm.Namespace, vm.Name); rbErr != nil {
			klog.Errorf("Failed to rollback Helm labels from VM %s/%s: %v", vm.Namespace, vm.Name, rbErr)
		}
		if rbErr := c.removeHelmLabelsFromDataVolumes(ctx, vm.Namespace, dvNames); rbErr != nil {
			klog.Errorf("Failed to rollback Helm labels from DataVolumes: %v", rbErr)
		}

		return fmt.Errorf("adoption failed: could not label VM: %w", err)
	}

	klog.Infof("Successfully adopted VM %s/%s", vm.Namespace, vm.Name)
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
