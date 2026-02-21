package main

import (
	"context"
	"flag"
	"fmt"
	"sync"
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

	// Run controller
	ctx := context.Background()
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

		// Convert to typed VM (we'll work with unstructured for simplicity)
		vms = append(vms, kubevirtv1.VirtualMachine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      item.GetName(),
				Namespace: item.GetNamespace(),
				Labels:    item.GetLabels(),
			},
			// We'll extract spec when needed
		})
	}

	return vms, nil
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
		klog.Warningf("Failed to get Plan %s/%s: %v (defaulting to adoption enabled)", namespace, planName, err)
		return true // Default to enabled if we can't check
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
	if err != nil || !found {
		return fmt.Errorf("failed to get VM spec: %w", err)
	}

	// Extract running state
	running, _, _ := unstructured.NestedBool(spec, "running")

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

	// Extract disks with safe type assertions
	template, _, _ := unstructured.NestedMap(spec, "template")
	templateSpec, _, _ := unstructured.NestedMap(template, "spec")
	volumes, _, _ := unstructured.NestedSlice(templateSpec, "volumes")

	var disks []interface{}
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

		diskName, ok := dvNameRaw.(string)
		if !ok {
			klog.V(2).Infof("VM %s/%s: skipping volume %d: dataVolume name has unexpected type %T", vm.Namespace, vm.Name, i, dvNameRaw)
			continue
		}

		// Extract disk name (remove vm-disk- prefix if present)
		if len(diskName) > 8 && diskName[:8] == "vm-disk-" {
			diskName = diskName[8:]
		}

		disks = append(disks, map[string]interface{}{
			"name": diskName,
			"bus":  "virtio",
		})
		klog.V(3).Infof("VM %s/%s: added disk %s", vm.Namespace, vm.Name, diskName)
	}

	klog.Infof("VM %s/%s: extracted %d disk(s), instanceType=%s, preference=%s, running=%v",
		vm.Namespace, vm.Name, len(disks), instanceType, preference, running)

	// Create VMInstance name
	vmInstanceName := vm.Name
	if namePrefix != "" {
		vmInstanceName = namePrefix + vm.Name
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
		return c.labelVMAsAdopted(ctx, vm.Namespace, vm.Name, vmInstanceName)
	}

	// Create VMInstance
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
				"running":         running,
				"instanceType":    instanceType,
				"instanceProfile": preference,
				"disks":           disks,
				"external":        false,
				"externalMethod":  "PortList",
				"externalPorts":   []int{22},
				"gpus":            []interface{}{},
				"resources":       map[string]interface{}{},
				"sshKeys":         []interface{}{},
				"subnets":         []interface{}{},
				"cloudInit":       "",
				"cloudInitSeed":   "imported-vm",
			},
		},
	}

	_, err = c.dynamicClient.Resource(vmInstanceGVR).Namespace(vm.Namespace).Create(ctx, vmInstance, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create VMInstance: %w", err)
	}

	klog.Infof("✓ Created VMInstance %s/%s", vm.Namespace, vmInstanceName)

	// Label the original VM as adopted (CRITICAL: rollback on failure)
	if err := c.labelVMAsAdopted(ctx, vm.Namespace, vm.Name, vmInstanceName); err != nil {
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

		return fmt.Errorf("adoption failed: could not label VM: %w", err)
	}

	klog.Infof("✓ Successfully adopted VM %s/%s", vm.Namespace, vm.Name)
	return nil
}

func (c *AdoptionController) labelVMAsAdopted(ctx context.Context, namespace, vmName, vmInstanceName string) error {
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
	vm.SetLabels(labels)

	_, err = c.dynamicClient.Resource(vmGVR).Namespace(namespace).Update(ctx, vm, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update VM labels: %w", err)
	}

	klog.Infof("✓ Labeled VM %s/%s as adopted by %s", namespace, vmName, vmInstanceName)
	return nil
}
