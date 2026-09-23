package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	cosiv1alpha1 "sigs.k8s.io/container-object-storage-interface-api/apis/objectstorage/v1alpha1"
)

const (
	// namespaceMonitoringLabel is the namespace label that indicates which tenant
	// namespace hosts the monitoring stack (VictoriaMetrics/Prometheus).
	namespaceMonitoringLabel = "namespace.cozystack.io/monitoring"
	workloadLabelPrefix      = "workloads.cozystack.io/"
	// workloadMonitorLabel is reserved: it names the WorkloadMonitor that owns
	// the Workload and is always set by the reconciler, so it is never copied
	// from monitor labels.
	workloadMonitorLabel = workloadLabelPrefix + "monitor"
	// vmSelectService is the well-known service name for VictoriaMetrics vmselect
	// within a monitoring namespace. Port 8481, path /select/0/prometheus.
	vmSelectService = "vmselect-shortterm"
	vmSelectPort    = "8481"
	vmSelectPath    = "/select/0/prometheus"
	// Resource keys reported on bucket Workloads.
	resourceS3StorageBytes         = "s3-storage-bytes"
	resourceS3PhysicalStorageBytes = "s3-physical-storage-bytes"
)

// prometheusHTTPClient is a dedicated HTTP client for Prometheus queries,
// avoiding the shared http.DefaultClient global.
var prometheusHTTPClient = &http.Client{Timeout: 10 * time.Second}

// WorkloadMonitorReconciler reconciles a WorkloadMonitor object
type WorkloadMonitorReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	// SeaweedfsMetricsEndpoint, when non-empty, is the base URL of the
	// Prometheus-compatible query API to fetch SeaweedFS bucket size metrics
	// from, instead of discovering the monitoring stack via the
	// namespace.cozystack.io/monitoring label. Used when SeaweedFS and the
	// stack that scrapes it run outside this cluster.
	SeaweedfsMetricsEndpoint string
	// DataVolumeReader reads CDI DataVolumes. Nil until the DataVolume kind is
	// served, and until then DataVolumes do not count towards Operational.
	DataVolumeReader client.Reader
	dataVolumeMu     sync.RWMutex
	// reconciled is closed by the first Reconcile. controller-runtime starts the
	// workers only after the controller has started its sources
	// (pkg/internal/controller/controller.go, Controller.Start), so from then on
	// Watch starts a new source itself instead of queueing it for Start.
	reconciled     chan struct{}
	reconciledOnce sync.Once
}

const (
	// dataVolumeAPIPollInterval is how often the controller looks for the
	// DataVolume kind until CDI is installed.
	dataVolumeAPIPollInterval = 30 * time.Second
	// dataVolumeSyncTimeout bounds the wait for the DataVolume informer to sync.
	// A reconcile context has no deadline, so a reader installed on an informer
	// that never syncs would park every WorkloadMonitor reconcile for good.
	dataVolumeSyncTimeout = time.Minute
)

func (r *WorkloadMonitorReconciler) dataVolumeReader() client.Reader {
	r.dataVolumeMu.RLock()
	defer r.dataVolumeMu.RUnlock()
	return r.DataVolumeReader
}

func (r *WorkloadMonitorReconciler) reconciledCh() chan struct{} {
	r.dataVolumeMu.Lock()
	defer r.dataVolumeMu.Unlock()
	if r.reconciled == nil {
		r.reconciled = make(chan struct{})
	}
	return r.reconciled
}

var dataVolumeGVK = schema.GroupVersionKind{Group: "cdi.kubevirt.io", Version: "v1beta1", Kind: "DataVolume"}

// dataVolumeInFlightPhases are the DataVolumePhase values in which CDI still has
// work to do on the disk (containerized-data-importer-api pkg/apis/core/v1beta1).
// A phase outside this set counts as settled, so a CDI release that adds or
// renames a phase cannot turn working disks non-operational; the price is that
// a renamed in-flight phase reads as settled until this list follows it.
// PendingPopulation and WaitForFirstConsumer are where a disk on a
// WaitForFirstConsumer class rests until a VM consumes it, so neither is in
// flight. UploadReady is in flight although it lasts until someone uploads:
// CDI holds it from the moment the upload server is ready until the transfer
// succeeds (pkg/controller/datavolume/upload-controller.go), so it covers an
// empty disk and a half-uploaded one alike.
// Paused is where a multi-stage (checkpoint) import waits for its next
// checkpoint, which only an outside actor supplies; the
// vm-disk chart renders no checkpoint source, so no disk of it gets there.
var dataVolumeInFlightPhases = map[string]bool{
	"Pending":                           true,
	"PVCBound":                          true,
	"ImportScheduled":                   true,
	"ImportInProgress":                  true,
	"CloneScheduled":                    true,
	"CloneInProgress":                   true,
	"SnapshotForSmartCloneInProgress":   true,
	"CloneFromSnapshotSourceInProgress": true,
	"SmartClonePVCInProgress":           true,
	"CSICloneInProgress":                true,
	"PrepClaimInProgress":               true,
	"RebindInProgress":                  true,
	"ExpansionInProgress":               true,
	"NamespaceTransferInProgress":       true,
	"UploadScheduled":                   true,
	"UploadReady":                       true,
}

// isDataVolumeReady reports whether CDI is done with the disk. Failed and
// Unknown are not ready, and neither is a DataVolume without a phase: CDI
// leaves one there when it could not render the PVC spec.
func isDataVolumeReady(dv *unstructured.Unstructured) bool {
	phase, _, _ := unstructured.NestedString(dv.Object, "status", "phase")
	switch phase {
	case "", "Failed", "Unknown":
		return false
	}
	return !dataVolumeInFlightPhases[phase]
}

// dataVolumeAPIServed treats a NoMatch as not served and returns any other
// discovery error, so the caller retries instead of concluding CDI is absent.
func dataVolumeAPIServed(mapper meta.RESTMapper) (bool, error) {
	_, err := mapper.RESTMapping(dataVolumeGVK.GroupKind(), dataVolumeGVK.Version)
	if meta.IsNoMatchError(err) {
		return false, nil
	}
	return err == nil, err
}

type sourceWatcher interface {
	Watch(src source.Source) error
}

// tryStartDataVolumeWatch registers the DataVolume watch and reader once the
// kind is served, and reports whether it did. The reader is set only after the
// source has synced: the cache blocks a List on an unsynced informer until the
// caller's context ends. A source that does not sync in time is cancelled by
// WaitForSync, and the next call starts a fresh one.
func (r *WorkloadMonitorReconciler) tryStartDataVolumeWatch(ctx context.Context, mapper meta.RESTMapper, c sourceWatcher, informers cache.Cache, syncTimeout time.Duration) (bool, error) {
	served, err := dataVolumeAPIServed(mapper)
	if err != nil || !served {
		return false, err
	}
	dv := &unstructured.Unstructured{}
	dv.SetGroupVersionKind(dataVolumeGVK)
	src := source.Kind[client.Object](informers, dv,
		handler.EnqueueRequestsFromMapFunc(mapObjectToMonitor(client.Object(dv), r.Client)))
	if err := c.Watch(src); err != nil {
		return false, fmt.Errorf("watching DataVolumes: %w", err)
	}
	syncCtx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()
	if err := src.WaitForSync(syncCtx); err != nil {
		return false, fmt.Errorf("waiting for the DataVolume informer to sync: %w", err)
	}
	// WaitForSync returns nil when ctx itself is cancelled.
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.dataVolumeMu.Lock()
	defer r.dataVolumeMu.Unlock()
	// The manager's client reads unstructured objects straight from the API
	// server; the cache shares the informer the watch above starts.
	r.DataVolumeReader = informers
	return true, nil
}

// watchDataVolumes adds the DataVolume watch once the first reconcile has run
// and the kind is served, checking discovery again every interval until it is.
// Starting on the first reconcile rather than on the next tick keeps a disk
// created right after a controller restart from reading populated until then.
func (r *WorkloadMonitorReconciler) watchDataVolumes(ctx context.Context, mapper meta.RESTMapper, c sourceWatcher, informers cache.Cache, interval time.Duration) {
	select {
	case <-ctx.Done():
		return
	case <-r.reconciledCh():
	}
	logger := log.FromContext(ctx)
	// The condition never returns an error, so the poll ends only when the
	// watch is added or the manager stops.
	_ = wait.PollUntilContextCancel(ctx, interval, true, func(ctx context.Context) (bool, error) {
		done, err := r.tryStartDataVolumeWatch(ctx, mapper, c, informers, dataVolumeSyncTimeout)
		if err != nil {
			logger.Error(err, "Unable to start the DataVolume watch, retrying")
		}
		return done, nil
	})
}

// dataVolumesMessage names every DataVolume the monitor selects that is not
// ready, with its phase, and is empty when there is none. A NoMatch from the
// reader counts as no DataVolumes.
func (r *WorkloadMonitorReconciler) dataVolumesMessage(ctx context.Context, monitor *cozyv1alpha1.WorkloadMonitor) (string, error) {
	reader := r.dataVolumeReader()
	if reader == nil {
		return "", nil
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(dataVolumeGVK.GroupVersion().WithKind(dataVolumeGVK.Kind + "List"))
	if err := reader.List(
		ctx,
		list,
		client.InNamespace(monitor.Namespace),
		client.MatchingLabels(monitor.Spec.Selector),
	); err != nil {
		if meta.IsNoMatchError(err) {
			return "", nil
		}
		return "", err
	}
	var stuck []string
	for i := range list.Items {
		dv := &list.Items[i]
		if isDataVolumeReady(dv) {
			continue
		}
		phase, _, _ := unstructured.NestedString(dv.Object, "status", "phase")
		if phase == "" {
			stuck = append(stuck, fmt.Sprintf("DataVolume %s has no phase", dv.GetName()))
		} else {
			stuck = append(stuck, fmt.Sprintf("DataVolume %s is %s", dv.GetName(), phase))
		}
	}
	sort.Strings(stuck)
	return strings.Join(stuck, "; "), nil
}

// +kubebuilder:rbac:groups=cozystack.io,resources=workloadmonitors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cozystack.io,resources=workloadmonitors/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cozystack.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cozystack.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=objectstorage.k8s.io,resources=bucketclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups=cdi.kubevirt.io,resources=datavolumes,verbs=get;list;watch

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

// ParseMetricsEndpointURL validates an administrator-supplied metrics endpoint
// URL and normalizes it to a base without a trailing slash, so that appending
// /api/v1/query never doubles or drops a slash. Userinfo is allowed: net/http
// turns it into basic auth on every request.
func ParseMetricsEndpointURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("URL must be absolute with a host")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("URL must not carry a query string or fragment")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// resolvePrometheusURL returns the Prometheus-compatible API base URL for the given namespace.
// The SeaweedfsMetricsEndpoint override, when configured, takes precedence; otherwise the
// namespace.cozystack.io/monitoring label names the monitoring namespace and the well-known
// vmselect URL inside it is constructed. Returns empty string if monitoring is not configured.
func (r *WorkloadMonitorReconciler) resolvePrometheusURL(ctx context.Context, namespace string) (string, error) {
	if r.SeaweedfsMetricsEndpoint != "" {
		return r.SeaweedfsMetricsEndpoint, nil
	}
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: namespace}, ns); err != nil {
		// The namespace is the monitor's own, so NotFound only happens while
		// it is being deleted; there is no monitoring to resolve then.
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading namespace %s: %w", namespace, err)
	}
	monitoringNS := ns.Labels[namespaceMonitoringLabel]
	if monitoringNS == "" {
		return "", nil
	}
	return fmt.Sprintf("http://%s.%s.svc:%s%s", vmSelectService, monitoringNS, vmSelectPort, vmSelectPath), nil
}

// bucketMetrics holds size metrics for a single bucket, keyed by metric name.
type bucketMetrics struct {
	LogicalSize  int64
	PhysicalSize int64
	HasLogical   bool
	HasPhysical  bool
}

// queryAllBucketMetrics fetches SeaweedFS bucket size metrics for the given
// bucket names in a single Prometheus query and returns them keyed by bucket
// name. The query is scoped to only the requested buckets to avoid fetching
// metrics for buckets belonging to other WorkloadMonitors.
//
// Any failure to obtain a well-formed answer is returned as an error, never as
// an empty result: the sizes feed billing, and the caller must be able to tell
// "the endpoint said the bucket is empty" apart from "the endpoint could not
// be queried".
func (r *WorkloadMonitorReconciler) queryAllBucketMetrics(ctx context.Context, prometheusBaseURL string, bucketNames []string) (map[string]*bucketMetrics, error) {
	result := make(map[string]*bucketMetrics)
	if prometheusBaseURL == "" || len(bucketNames) == 0 {
		return result, nil
	}

	query := fmt.Sprintf(`{__name__=~"SeaweedFS_s3_bucket_(size|physical_size)_bytes",bucket=~"%s"}`, strings.Join(bucketNames, "|"))
	u, err := url.Parse(strings.TrimRight(prometheusBaseURL, "/") + "/api/v1/query")
	if err != nil {
		return nil, fmt.Errorf("parsing Prometheus URL: %w", err)
	}
	u.RawQuery = url.Values{"query": {query}}.Encode()

	httpCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating Prometheus request: %w", err)
	}

	resp, err := prometheusHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("querying Prometheus: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Prometheus returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("reading Prometheus response: %w", err)
	}

	var promResp struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string  `json:"metric"`
				Value  [2]json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &promResp); err != nil {
		return nil, fmt.Errorf("parsing Prometheus response: %w", err)
	}
	if promResp.Status != "success" {
		return nil, fmt.Errorf("Prometheus query finished with status %q", promResp.Status)
	}

	for _, r := range promResp.Data.Result {
		bucket := r.Metric["bucket"]
		metricName := r.Metric["__name__"]
		if bucket == "" || metricName == "" {
			continue
		}

		var valueStr string
		if err := json.Unmarshal(r.Value[1], &valueStr); err != nil {
			continue
		}
		val, err := strconv.ParseFloat(valueStr, 64)
		if err != nil {
			continue
		}

		bm, ok := result[bucket]
		if !ok {
			bm = &bucketMetrics{}
			result[bucket] = bm
		}

		switch metricName {
		case "SeaweedFS_s3_bucket_size_bytes":
			bm.LogicalSize = int64(val)
			bm.HasLogical = true
		case "SeaweedFS_s3_bucket_physical_size_bytes":
			bm.PhysicalSize = int64(val)
			bm.HasPhysical = true
		}
	}

	return result, nil
}

// reconcileBucketClaimForMonitor creates or updates a Workload object for the given BucketClaim and WorkloadMonitor.
func (r *WorkloadMonitorReconciler) reconcileBucketClaimForMonitor(
	ctx context.Context,
	monitor *cozyv1alpha1.WorkloadMonitor,
	bc cosiv1alpha1.BucketClaim,
	allMetrics map[string]*bucketMetrics,
) error {
	logger := log.FromContext(ctx)
	workload := &cozyv1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("bucket-%s", bc.Name),
			Namespace: bc.Namespace,
			Labels:    make(map[string]string, len(bc.Labels)),
		},
	}

	monitorLabels := r.getMonitorLabels(monitor)
	_, err := ctrl.CreateOrUpdate(ctx, r.Client, workload, func() error {
		updateOwnerReferences(workload.GetObjectMeta(), &bc)

		if workload.Labels == nil {
			workload.Labels = make(map[string]string)
		}
		// Apply monitor-level labels first so source-object labels can override on conflict
		for k, v := range monitorLabels {
			workload.Labels[k] = v
		}
		for k, v := range bc.Labels {
			workload.Labels[k] = v
		}
		workload.Labels[workloadMonitorLabel] = monitor.Name

		// Start from the sizes already recorded on the Workload: when the
		// metrics endpoint is unreachable or reports nothing for this bucket,
		// the last known good values must survive rather than collapse to
		// "no size", which billing would read as an empty bucket.
		resources := make(map[string]resource.Quantity)
		for _, key := range []string{resourceS3StorageBytes, resourceS3PhysicalStorageBytes} {
			if q, ok := workload.Status.Resources[key]; ok {
				resources[key] = q
			}
		}

		// Look up pre-fetched bucket metrics by the SeaweedFS bucket name.
		// bc.Status.BucketName is the COSI Bucket name, which the COSI driver
		// uses directly as the SeaweedFS bucket name.
		if bm, ok := allMetrics[bc.Status.BucketName]; ok {
			if bm.HasLogical {
				resources[resourceS3StorageBytes] = *resource.NewQuantity(bm.LogicalSize, resource.BinarySI)
			}
			if bm.HasPhysical {
				resources[resourceS3PhysicalStorageBytes] = *resource.NewQuantity(bm.PhysicalSize, resource.BinarySI)
			}
		}

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

	monitorLabels := r.getMonitorLabels(monitor)
	_, err := ctrl.CreateOrUpdate(ctx, r.Client, workload, func() error {
		// Update owner references with the new monitor
		updateOwnerReferences(workload.GetObjectMeta(), &svc)

		// Apply monitor-level labels first so source-object labels can override on conflict
		if workload.Labels == nil {
			workload.Labels = make(map[string]string)
		}
		for k, v := range monitorLabels {
			workload.Labels[k] = v
		}
		for k, v := range svc.Labels {
			workload.Labels[k] = v
		}
		workload.Labels[workloadMonitorLabel] = monitor.Name

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

	monitorLabels := r.getMonitorLabels(monitor)
	_, err := ctrl.CreateOrUpdate(ctx, r.Client, workload, func() error {
		// Update owner references with the new monitor
		updateOwnerReferences(workload.GetObjectMeta(), &pvc)

		// Apply monitor-level labels first so source-object labels can override on conflict
		if workload.Labels == nil {
			workload.Labels = make(map[string]string)
		}
		for k, v := range monitorLabels {
			workload.Labels[k] = v
		}
		for k, v := range pvc.Labels {
			workload.Labels[k] = v
		}
		workload.Labels[workloadMonitorLabel] = monitor.Name

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
	monitorLabels := r.getMonitorLabels(monitor)
	_, err := ctrl.CreateOrUpdate(ctx, r.Client, workload, func() error {
		// Update owner references with the new monitor
		updateOwnerReferences(workload.GetObjectMeta(), &pod)

		// Apply monitor-level labels first so source-object labels can override on conflict
		if workload.Labels == nil {
			workload.Labels = make(map[string]string)
		}
		for k, v := range monitorLabels {
			workload.Labels[k] = v
		}
		for k, v := range pod.Labels {
			workload.Labels[k] = v
		}
		workload.Labels[workloadMonitorLabel] = monitor.Name

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
	r.reconciledOnce.Do(func() { close(r.reconciledCh()) })
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
		logger.Error(err, "Unable to list BucketClaims for WorkloadMonitor", "monitor", monitor.Name)
		return ctrl.Result{}, err
	}

	var bucketMetricsErr error
	if len(bucketClaimList.Items) > 0 {
		var bucketNames []string
		for _, bc := range bucketClaimList.Items {
			if bc.Status.BucketName != "" {
				bucketNames = append(bucketNames, bc.Status.BucketName)
			}
		}
		var allBucketMetrics map[string]*bucketMetrics
		bucketPromURL, err := r.resolvePrometheusURL(ctx, monitor.Namespace)
		if err == nil {
			allBucketMetrics, err = r.queryAllBucketMetrics(ctx, bucketPromURL, bucketNames)
		}
		if err != nil {
			// Never let a metrics failure degrade into "bucket size = 0":
			// the Workloads below are still reconciled, but they keep their
			// last known sizes, and the failure is surfaced via an event,
			// the log, and the returned error.
			bucketMetricsErr = fmt.Errorf("fetching bucket size metrics: %w", err)
			logger.Error(bucketMetricsErr, "Retaining last known bucket sizes", "monitor", monitor.Name)
			if r.Recorder != nil {
				r.Recorder.Eventf(monitor, corev1.EventTypeWarning, "BucketMetricsUnavailable",
					"Failed to fetch bucket size metrics, retaining last known values: %v", err)
			}
		}
		for _, bc := range bucketClaimList.Items {
			if err := r.reconcileBucketClaimForMonitor(ctx, monitor, bc, allBucketMetrics); err != nil {
				logger.Error(err, "Failed to reconcile Workload for BucketClaim", "BucketClaim", bc.Name)
				continue
			}
		}
	}

	dataVolumesMessage, err := r.dataVolumesMessage(ctx, monitor)
	if err != nil {
		logger.Error(err, "Unable to list DataVolumes for WorkloadMonitor", "monitor", monitor.Name)
		return ctrl.Result{}, err
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
		fresh.Status.Message = dataVolumesMessage
		if dataVolumesMessage != "" {
			fresh.Status.Operational = pointer.Bool(false)
		}
		return r.Status().Update(ctx, fresh)
	})
	if err != nil {
		logger.Error(err, "unable to update WorkloadMonitor status after retries")
		return ctrl.Result{}, err
	}

	// Returning the metrics error makes the failure count in controller-runtime's
	// reconcile error metrics and retries with backoff instead of quietly waiting
	// for the next periodic requeue.
	if bucketMetricsErr != nil {
		return ctrl.Result{}, bucketMetricsErr
	}

	// Requeue periodically if there are BucketClaims to keep sizes up to date.
	// Bucket sizes come from Prometheus metrics that update every 60s.
	if len(bucketClaimList.Items) > 0 {
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

// SetupWithManager registers our controller with the Manager and sets up watches.
func (r *WorkloadMonitorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	builder := ctrl.NewControllerManagedBy(mgr).
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
		Owns(&cozyv1alpha1.Workload{})
	c, err := builder.Build(r)
	if err != nil {
		return err
	}

	// A watch on a kind whose CRD is absent never syncs, and controller-runtime
	// does not start a controller with unsynced caches. CDI ships with the IaaS
	// bundle only and can be installed while this controller is running, so
	// the DataVolume watch is added once the kind appears rather than at setup.
	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		r.watchDataVolumes(ctx, mgr.GetRESTMapper(), c, mgr.GetCache(), dataVolumeAPIPollInterval)
		return nil
	}))
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

// getMonitorLabels extracts workloads.cozystack.io/* labels from a WorkloadMonitor
// so they can be propagated onto Workload objects created for pods, PVCs, services,
// or bucket claims. The monitor label "workloads.cozystack.io/monitor" is reserved
// and set separately per Workload, so it is excluded here.
func (r *WorkloadMonitorReconciler) getMonitorLabels(monitor *cozyv1alpha1.WorkloadMonitor) map[string]string {
	labels := make(map[string]string)
	for k, v := range monitor.GetLabels() {
		if !strings.HasPrefix(k, workloadLabelPrefix) {
			continue
		}
		if k == workloadMonitorLabel {
			continue
		}
		labels[k] = v
	}
	return labels
}
