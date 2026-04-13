package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	cosiv1alpha1 "sigs.k8s.io/container-object-storage-interface-api/apis/objectstorage/v1alpha1"
)

// WorkloadMonitorReconciler reconciles a WorkloadMonitor object
type WorkloadMonitorReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// PrometheusURL is the base URL of a Prometheus-compatible API for querying
	// SeaweedFS bucket metrics. If empty, bucket size metrics are not collected.
	PrometheusURL string
}

// +kubebuilder:rbac:groups=cozystack.io,resources=workloadmonitors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cozystack.io,resources=workloadmonitors/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cozystack.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cozystack.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups=objectstorage.k8s.io,resources=bucketclaims,verbs=get;list;watch

// isBucketClaimReady checks if the BucketClaim has been provisioned.
func (r *WorkloadMonitorReconciler) isBucketClaimReady(bc *cosiv1alpha1.BucketClaim) bool {
	return bc.Status.BucketReady
}

// isServiceReady checks if the service has an external IP bound
func (r *WorkloadMonitorReconciler) isServiceReady(svc *corev1.Service) bool {
	return len(svc.Status.LoadBalancer.Ingress) > 0
}

// isPVCReady checks if the PVC is bound
func (r *WorkloadMonitorReconciler) isPVCReady(pvc *corev1.PersistentVolumeClaim) bool {
	return pvc.Status.Phase == corev1.ClaimBound
}

// isPodReady checks if the Pod is in the Ready condition.
func (r *WorkloadMonitorReconciler) isPodReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// updateOwnerReferences adds the given monitor as a new owner reference to the object if not already present.
// It then sorts the owner references to enforce a consistent order.
func updateOwnerReferences(obj metav1.Object, monitor client.Object) {
	// Retrieve current owner references
	owners := obj.GetOwnerReferences()

	// Check if current monitor is already in owner references
	var alreadyOwned bool
	for _, ownerRef := range owners {
		if ownerRef.UID == monitor.GetUID() {
			alreadyOwned = true
			break
		}
	}

	runtimeObj, ok := monitor.(runtime.Object)
	if !ok {
		return
	}
	gvk := runtimeObj.GetObjectKind().GroupVersionKind()

	// If not already present, add new owner reference without controller flag
	if !alreadyOwned {
		newOwnerRef := metav1.OwnerReference{
			APIVersion: gvk.GroupVersion().String(),
			Kind:       gvk.Kind,
			Name:       monitor.GetName(),
			UID:        monitor.GetUID(),
			// Set Controller to false to avoid conflict as multiple controllers are not allowed
			Controller:         pointer.BoolPtr(false),
			BlockOwnerDeletion: pointer.BoolPtr(true),
		}
		owners = append(owners, newOwnerRef)
	}

	// Sort owner references to enforce a consistent order by UID
	sort.SliceStable(owners, func(i, j int) bool {
		return owners[i].UID < owners[j].UID
	})

	// Update the owner references of the object
	obj.SetOwnerReferences(owners)
}

// queryBucketSizeBytes queries Prometheus for the logical size of a SeaweedFS bucket.
// Returns 0 if the metric is not available or PrometheusURL is not configured.
func (r *WorkloadMonitorReconciler) queryBucketSizeBytes(ctx context.Context, seaweedBucketName string) int64 {
	if r.PrometheusURL == "" || seaweedBucketName == "" {
		return 0
	}
	logger := log.FromContext(ctx)

	query := fmt.Sprintf(`SeaweedFS_s3_bucket_size_bytes{bucket="%s"}`, seaweedBucketName)
	u, err := url.Parse(r.PrometheusURL + "/api/v1/query")
	if err != nil {
		logger.Error(err, "Failed to parse Prometheus URL")
		return 0
	}
	u.RawQuery = url.Values{"query": {query}}.Encode()

	httpCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		logger.Error(err, "Failed to create Prometheus request")
		return 0
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.V(1).Info("Failed to query Prometheus for bucket size", "bucket", seaweedBucketName, "error", err)
		return 0
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Error(err, "Failed to read Prometheus response")
		return 0
	}

	// Parse Prometheus instant query response:
	// {"status":"success","data":{"resultType":"vector","result":[{"metric":{...},"value":[timestamp,"value"]}]}}
	var promResp struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Value [2]json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &promResp); err != nil {
		logger.Error(err, "Failed to parse Prometheus response")
		return 0
	}
	if promResp.Status != "success" || len(promResp.Data.Result) == 0 {
		return 0
	}

	var valueStr string
	if err := json.Unmarshal(promResp.Data.Result[0].Value[1], &valueStr); err != nil {
		logger.Error(err, "Failed to parse Prometheus metric value")
		return 0
	}

	qty, err := resource.ParseQuantity(valueStr)
	if err != nil {
		logger.Error(err, "Failed to parse metric value as quantity", "value", valueStr)
		return 0
	}
	return qty.Value()
}

// reconcileBucketClaimForMonitor creates or updates a Workload object for the given BucketClaim and WorkloadMonitor.
func (r *WorkloadMonitorReconciler) reconcileBucketClaimForMonitor(
	ctx context.Context,
	monitor *cozyv1alpha1.WorkloadMonitor,
	bc cosiv1alpha1.BucketClaim,
) error {
	logger := log.FromContext(ctx)
	workload := &cozyv1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("bucket-%s", bc.Name),
			Namespace: bc.Namespace,
			Labels:    make(map[string]string, len(bc.Labels)),
		},
	}

	resources := make(map[string]resource.Quantity)
	resources["s3-buckets"] = resource.MustParse("1")

	// Query actual bucket size from SeaweedFS metrics via Prometheus.
	// bc.Status.BucketName is the COSI Bucket name, which the COSI driver
	// uses directly as the SeaweedFS bucket name.
	if sizeBytes := r.queryBucketSizeBytes(ctx, bc.Status.BucketName); sizeBytes > 0 {
		resources["s3-storage-bytes"] = *resource.NewQuantity(sizeBytes, resource.BinarySI)
	}

	_, err := ctrl.CreateOrUpdate(ctx, r.Client, workload, func() error {
		updateOwnerReferences(workload.GetObjectMeta(), &bc)

		for k, v := range bc.Labels {
			workload.Labels[k] = v
		}
		workload.Labels["workloads.cozystack.io/monitor"] = monitor.Name

		workload.Status.Kind = monitor.Spec.Kind
		workload.Status.Type = monitor.Spec.Type
		workload.Status.Resources = resources
		workload.Status.Operational = r.isBucketClaimReady(&bc)

		return nil
	})
	if err != nil {
		logger.Error(err, "Failed to CreateOrUpdate Workload", "workload", workload.Name)
		return err
	}

	return nil
}

// reconcileServiceForMonitor creates or updates a Workload object for the given Service and WorkloadMonitor.
func (r *WorkloadMonitorReconciler) reconcileServiceForMonitor(
	ctx context.Context,
	monitor *cozyv1alpha1.WorkloadMonitor,
	svc corev1.Service,
) error {
	logger := log.FromContext(ctx)
	workload := &cozyv1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("svc-%s", svc.Name),
			Namespace: svc.Namespace,
			Labels:    make(map[string]string, len(svc.Labels)),
		},
	}

	resources := make(map[string]resource.Quantity)

	quantity := resource.MustParse("0")

	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if ing.IP != "" {
			quantity.Add(resource.MustParse("1"))
		}
	}

	var resourceLabel string
	if svc.Annotations != nil {
		var ok bool
		resourceLabel, ok = svc.Annotations["metallb.universe.tf/ip-allocated-from-pool"]
		if !ok {
			resourceLabel = "default"
		}
	}
	resourceLabel = fmt.Sprintf("%s.ipaddresspool.metallb.io/requests.ipaddresses", resourceLabel)
	resources[resourceLabel] = quantity

	_, err := ctrl.CreateOrUpdate(ctx, r.Client, workload, func() error {
		// Update owner references with the new monitor
		updateOwnerReferences(workload.GetObjectMeta(), &svc)

		for k, v := range svc.Labels {
			workload.Labels[k] = v
		}
		workload.Labels["workloads.cozystack.io/monitor"] = monitor.Name

		// Fill Workload status fields:
		workload.Status.Kind = monitor.Spec.Kind
		workload.Status.Type = monitor.Spec.Type
		workload.Status.Resources = resources
		workload.Status.Operational = r.isServiceReady(&svc)

		return nil
	})
	if err != nil {
		logger.Error(err, "Failed to CreateOrUpdate Workload", "workload", workload.Name)
		return err
	}

	return nil
}

// reconcilePVCForMonitor creates or updates a Workload object for the given PVC and WorkloadMonitor.
func (r *WorkloadMonitorReconciler) reconcilePVCForMonitor(
	ctx context.Context,
	monitor *cozyv1alpha1.WorkloadMonitor,
	pvc corev1.PersistentVolumeClaim,
) error {
	logger := log.FromContext(ctx)
	workload := &cozyv1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("pvc-%s", pvc.Name),
			Namespace: pvc.Namespace,
			Labels:    make(map[string]string, len(pvc.Labels)),
		},
	}

	resources := make(map[string]resource.Quantity)

	for resourceName, resourceQuantity := range pvc.Status.Capacity {
		storageClass := "default"
		if pvc.Spec.StorageClassName != nil || *pvc.Spec.StorageClassName == "" {
			storageClass = *pvc.Spec.StorageClassName
		}
		resourceLabel := fmt.Sprintf("%s.storageclass.storage.k8s.io/requests.%s", storageClass, resourceName.String())
		resources[resourceLabel] = resourceQuantity
	}

	_, err := ctrl.CreateOrUpdate(ctx, r.Client, workload, func() error {
		// Update owner references with the new monitor
		updateOwnerReferences(workload.GetObjectMeta(), &pvc)

		for k, v := range pvc.Labels {
			workload.Labels[k] = v
		}
		workload.Labels["workloads.cozystack.io/monitor"] = monitor.Name

		// Fill Workload status fields:
		workload.Status.Kind = monitor.Spec.Kind
		workload.Status.Type = monitor.Spec.Type
		workload.Status.Resources = resources
		workload.Status.Operational = r.isPVCReady(&pvc)

		return nil
	})
	if err != nil {
		logger.Error(err, "Failed to CreateOrUpdate Workload", "workload", workload.Name)
		return err
	}

	return nil
}

// reconcilePodForMonitor creates or updates a Workload object for the given Pod and WorkloadMonitor.
func (r *WorkloadMonitorReconciler) reconcilePodForMonitor(
	ctx context.Context,
	monitor *cozyv1alpha1.WorkloadMonitor,
	pod corev1.Pod,
) error {
	logger := log.FromContext(ctx)

	// totalResources will store the sum of all container resource requests
	totalResources := make(map[string]resource.Quantity)

	// Iterate over all containers to aggregate their requests
	for _, container := range pod.Spec.Containers {
		for name, qty := range container.Resources.Requests {
			if existing, exists := totalResources[name.String()]; exists {
				existing.Add(qty)
				totalResources[name.String()] = existing
			} else {
				totalResources[name.String()] = qty.DeepCopy()
			}
		}
	}

	// If annotation "workload.cozystack.io/resources" is present, parse and merge
	if resourcesStr, ok := pod.Annotations["workload.cozystack.io/resources"]; ok {
		annRes := map[string]string{}
		if err := json.Unmarshal([]byte(resourcesStr), &annRes); err != nil {
			logger.Error(err, "Failed to parse resources annotation", "pod", pod.Name)
		} else {
			for k, v := range annRes {
				parsed, err := resource.ParseQuantity(v)
				if err != nil {
					logger.Error(err, "Failed to parse resource quantity from annotation", "key", k, "value", v)
					continue
				}
				totalResources[k] = parsed
			}
		}
	}

	workload := &cozyv1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("pod-%s", pod.Name),
			Namespace: pod.Namespace,
			Labels:    make(map[string]string, len(pod.Labels)),
		},
	}

	metaLabels := r.getWorkloadMetadata(&pod)
	_, err := ctrl.CreateOrUpdate(ctx, r.Client, workload, func() error {
		// Update owner references with the new monitor
		updateOwnerReferences(workload.GetObjectMeta(), &pod)

		for k, v := range pod.Labels {
			workload.Labels[k] = v
		}
		workload.Labels["workloads.cozystack.io/monitor"] = monitor.Name

		// Add workload meta to labels
		for k, v := range metaLabels {
			workload.Labels[k] = v
		}

		// Fill Workload status fields:
		workload.Status.Kind = monitor.Spec.Kind
		workload.Status.Type = monitor.Spec.Type
		workload.Status.Resources = totalResources
		workload.Status.Operational = r.isPodReady(&pod)

		return nil
	})
	if err != nil {
		logger.Error(err, "Failed to CreateOrUpdate Workload", "workload", workload.Name)
		return err
	}

	return nil
}

// Reconcile is the main reconcile loop.
// 1. It reconciles WorkloadMonitor objects themselves (create/update/delete).
// 2. It also reconciles Pod events mapped to WorkloadMonitor via label selector.
func (r *WorkloadMonitorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the WorkloadMonitor object if it exists
	monitor := &cozyv1alpha1.WorkloadMonitor{}
	err := r.Get(ctx, req.NamespacedName, monitor)
	if err != nil {
		// If the resource is not found, it may be a Pod event (mapFunc).
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Unable to fetch WorkloadMonitor")
		return ctrl.Result{}, err
	}

	// List Pods that match the WorkloadMonitor's selector
	podList := &corev1.PodList{}
	if err := r.List(
		ctx,
		podList,
		client.InNamespace(monitor.Namespace),
		client.MatchingLabels(monitor.Spec.Selector),
	); err != nil {
		logger.Error(err, "Unable to list Pods for WorkloadMonitor", "monitor", monitor.Name)
		return ctrl.Result{}, err
	}

	var observedReplicas, availableReplicas int32

	// For each matching Pod, reconcile the corresponding Workload
	for _, pod := range podList.Items {
		observedReplicas++
		if err := r.reconcilePodForMonitor(ctx, monitor, pod); err != nil {
			logger.Error(err, "Failed to reconcile Workload for Pod", "pod", pod.Name)
			continue
		}
		if r.isPodReady(&pod) {
			availableReplicas++
		}
	}

	pvcList := &corev1.PersistentVolumeClaimList{}
	if err := r.List(
		ctx,
		pvcList,
		client.InNamespace(monitor.Namespace),
		client.MatchingLabels(monitor.Spec.Selector),
	); err != nil {
		logger.Error(err, "Unable to list PVCs for WorkloadMonitor", "monitor", monitor.Name)
		return ctrl.Result{}, err
	}

	for _, pvc := range pvcList.Items {
		if err := r.reconcilePVCForMonitor(ctx, monitor, pvc); err != nil {
			logger.Error(err, "Failed to reconcile Workload for PVC", "PVC", pvc.Name)
			continue
		}
	}

	svcList := &corev1.ServiceList{}
	if err := r.List(
		ctx,
		svcList,
		client.InNamespace(monitor.Namespace),
		client.MatchingLabels(monitor.Spec.Selector),
	); err != nil {
		logger.Error(err, "Unable to list Services for WorkloadMonitor", "monitor", monitor.Name)
		return ctrl.Result{}, err
	}

	for _, svc := range svcList.Items {
		if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
			continue
		}
		if err := r.reconcileServiceForMonitor(ctx, monitor, svc); err != nil {
			logger.Error(err, "Failed to reconcile Workload for Service", "Service", svc.Name)
			continue
		}
	}

	bucketClaimList := &cosiv1alpha1.BucketClaimList{}
	if err := r.List(
		ctx,
		bucketClaimList,
		client.InNamespace(monitor.Namespace),
		client.MatchingLabels(monitor.Spec.Selector),
	); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "Unable to list BucketClaims for WorkloadMonitor", "monitor", monitor.Name)
			return ctrl.Result{}, err
		}
	}

	for _, bc := range bucketClaimList.Items {
		if err := r.reconcileBucketClaimForMonitor(ctx, monitor, bc); err != nil {
			logger.Error(err, "Failed to reconcile Workload for BucketClaim", "BucketClaim", bc.Name)
			continue
		}
	}

	// Update WorkloadMonitor status based on observed pods
	monitor.Status.ObservedReplicas = observedReplicas
	monitor.Status.AvailableReplicas = availableReplicas

	// Update the WorkloadMonitor status in the cluster
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		fresh := &cozyv1alpha1.WorkloadMonitor{}
		if err := r.Get(ctx, req.NamespacedName, fresh); err != nil {
			return err
		}
		fresh.Status.ObservedReplicas = observedReplicas
		fresh.Status.AvailableReplicas = availableReplicas

		// Default to operational = true, but check MinReplicas if set.
		// Use fresh.Spec to avoid making decisions based on a stale cached copy
		// when the spec was updated between the initial read and this retry.
		fresh.Status.Operational = pointer.Bool(true)
		if fresh.Spec.MinReplicas != nil && availableReplicas < *fresh.Spec.MinReplicas {
			fresh.Status.Operational = pointer.Bool(false)
		}
		return r.Status().Update(ctx, fresh)
	})
	if err != nil {
		logger.Error(err, "unable to update WorkloadMonitor status after retries")
		return ctrl.Result{}, err
	}

	// Requeue periodically if there are BucketClaims to keep sizes up to date.
	// Bucket sizes come from Prometheus metrics that update every 60s.
	if len(bucketClaimList.Items) > 0 && r.PrometheusURL != "" {
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

// SetupWithManager registers our controller with the Manager and sets up watches.
func (r *WorkloadMonitorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Watch WorkloadMonitor objects
		For(&cozyv1alpha1.WorkloadMonitor{}).
		// Also watch Pod objects and map them back to WorkloadMonitor if labels match
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(mapObjectToMonitor(&corev1.Pod{}, r.Client)),
		).
		// Watch PVCs as well
		Watches(
			&corev1.PersistentVolumeClaim{},
			handler.EnqueueRequestsFromMapFunc(mapObjectToMonitor(&corev1.PersistentVolumeClaim{}, r.Client)),
		).
		// Watch BucketClaims for S3 bucket billing
		Watches(
			&cosiv1alpha1.BucketClaim{},
			handler.EnqueueRequestsFromMapFunc(mapObjectToMonitor(&cosiv1alpha1.BucketClaim{}, r.Client)),
		).
		// Watch for changes to Workload objects we create (owned by WorkloadMonitor)
		Owns(&cozyv1alpha1.Workload{}).
		Complete(r)
}

func mapObjectToMonitor[T client.Object](_ T, c client.Client) func(ctx context.Context, obj client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		concrete, ok := obj.(T)
		if !ok {
			return nil
		}

		var monitorList cozyv1alpha1.WorkloadMonitorList
		// List all WorkloadMonitors in the same namespace
		if err := c.List(ctx, &monitorList, client.InNamespace(concrete.GetNamespace())); err != nil {
			return nil
		}

		labels := concrete.GetLabels()
		// Match each monitor's selector with the Pod's labels
		var requests []reconcile.Request
		for _, m := range monitorList.Items {
			matches := true
			for k, v := range m.Spec.Selector {
				if labelVal, exists := labels[k]; !exists || labelVal != v {
					matches = false
					break
				}
			}
			if matches {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: m.Namespace,
						Name:      m.Name,
					},
				})
			}
		}
		return requests
	}
}

func (r *WorkloadMonitorReconciler) getWorkloadMetadata(obj client.Object) map[string]string {
	labels := make(map[string]string)
	annotations := obj.GetAnnotations()
	if instanceType, ok := annotations["kubevirt.io/cluster-instancetype-name"]; ok {
		labels["workloads.cozystack.io/kubevirt-vmi-instance-type"] = instanceType
	}
	if instanceProfile, ok := annotations["kubevirt.io/cluster-instanceprofile-name"]; ok {
		labels["workloads.cozystack.io/kubevirt-vmi-instance-profile"] = instanceProfile
	}
	return labels
}
