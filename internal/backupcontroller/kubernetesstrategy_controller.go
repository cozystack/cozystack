// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/template"
)

const (
	kubernetesAppKind       = "Kubernetes"
	kubernetesReleasePrefix = "kubernetes-"

	kubernetesLabelMode   = "kubernetes.strategy.backups.cozystack.io/mode"
	kubernetesModeBackup  = "backup"
	kubernetesModeRestore = "restore"
	kubernetesModeCleanup = "cleanup"
	// kubernetesModeVerify checks that the dump object exists before a restore
	// changes anything.
	kubernetesModeVerify = "verify"

	// kubernetesCleanupAttemptsAnnotation counts failed cleanup Jobs on the
	// Backup, so a persistently failing delete (revoked credentials, a removed
	// bucket) ends in a released Backup and a Warning rather than a Job storm.
	kubernetesCleanupAttemptsAnnotation = "kubernetes.strategy.backups.cozystack.io/cleanup-attempts"
	kubernetesCleanupMaxAttempts        = 5

	kubernetesMetaPrefix                = "kubernetes.strategy.backups.cozystack.io/"
	kubernetesObjectMetaKey             = kubernetesMetaPrefix + "object"
	kubernetesDatastorePrefixMetaKey    = kubernetesMetaPrefix + "datastore-prefix"
	kubernetesDatastoreEndpointsMetaKey = kubernetesMetaPrefix + "datastore-endpoints"
	kubernetesHelmReleasesMetaKey       = kubernetesMetaPrefix + "helmreleases"
	kubernetesDataVolumesMetaKey        = kubernetesMetaPrefix + "datavolumes"
	kubernetesCertWarningsMetaKey       = kubernetesMetaPrefix + "certificate-warnings"
	kubernetesParamPrefix               = kubernetesMetaPrefix + "parameter/"

	// kubernetesCondCertificates is set on the BackupJob once the control-plane
	// certificates have been checked; Status=True means at least one expires
	// inside the warning window.
	kubernetesCondCertificates = "CertificatesExpiring"

	kubernetesPollInterval = 5 * time.Second
	// Leaf certificates are rotated by Kamaji and cert-manager, so a near
	// expiry is only worth a warning when it is this close.
	kubernetesDefaultCertThreshold = 720 * time.Hour
	// A CA must outlive every backup that may still be restored, so its
	// window is the Velero backup TTL; this is the fallback when none is set.
	kubernetesDefaultBackupTTL = 720 * time.Hour

	helmReleaseNameAnnotation = "meta.helm.sh/release-name"
	clusterNameLabel          = "cluster.x-k8s.io/cluster-name"
	helmInstanceLabel         = "app.kubernetes.io/instance"
	kamajiNameLabel           = "kamaji.clastix.io/name"
)

var (
	tenantControlPlaneGVR = schema.GroupVersionResource{Group: "kamaji.clastix.io", Version: "v1alpha1", Resource: "tenantcontrolplanes"}
	kamajiDataStoreGVR    = schema.GroupVersionResource{Group: "kamaji.clastix.io", Version: "v1alpha1", Resource: "datastores"}
	kamajiControlPlaneGVR = schema.GroupVersionResource{Group: "controlplane.cluster.x-k8s.io", Version: "v1alpha1", Resource: "kamajicontrolplanes"}
	machineDeploymentGVR  = schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta1", Resource: "machinedeployments"}
	machineGVR            = schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta1", Resource: "machines"}
	deploymentGVR         = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
)

// kubernetesCertificateSecrets lists, by suffix after the release name, the
// Secrets whose certificates a backup checks. Kamaji and cert-manager rotate
// the leaves; the CAs are irreplaceable, so a restore needs them valid.
var kubernetesCertificateSecrets = []string{
	"ca",
	"front-proxy-ca-certificate",
	"api-server-certificate",
	"api-server-kubelet-client-certificate",
	"front-proxy-client-certificate",
	"talos-ca",
	"talos-tls-cert",
}

// kubernetesUnlabelledSecrets are issued by cert-manager without the Helm
// instance label the Velero selector relies on; the driver adds it before the
// backup so the Talos PKI is not silently left out.
var kubernetesUnlabelledSecrets = []string{"talos-ca", "talos-tls-cert"}

func kubernetesReleaseName(appName string) string {
	return kubernetesReleasePrefix + appName
}

func validateKubernetesApplicationRef(ref corev1.TypedLocalObjectReference) error {
	if ref.Kind != kubernetesAppKind {
		return fmt.Errorf("kubernetes strategy supports applicationRef.kind %s, got %q", kubernetesAppKind, ref.Kind)
	}
	if ref.APIGroup != nil && *ref.APIGroup != "" && *ref.APIGroup != backupsv1alpha1.DefaultApplicationAPIGroup {
		return fmt.Errorf("kubernetes strategy supports applicationRef.apiGroup=%q, got %q", backupsv1alpha1.DefaultApplicationAPIGroup, *ref.APIGroup)
	}
	return nil
}

func kubernetesObjectKey(namespace, appName, backupName string) string {
	return fmt.Sprintf("%s/%s/%s.etcd.json", namespace, appName, backupName)
}

// kubernetesDatastore is where the tenant cluster keeps its state: a key
// prefix in the etcd DataStore shared by every cluster of the tenant.
type kubernetesDatastore struct {
	Prefix            string
	Endpoints         []string
	CertificateSecret string
	// SourcePrefix is the prefix the restored dump was taken under. It
	// differs from Prefix when a deleted cluster is rebuilt: Kamaji keys a new
	// TenantControlPlane's schema on its UID.
	SourcePrefix string
}

func (d kubernetesDatastore) templateContext() map[string]any {
	source := d.SourcePrefix
	if source == "" {
		source = d.Prefix
	}
	return map[string]any{
		"Prefix":            d.Prefix,
		"SourcePrefix":      source,
		"Endpoints":         strings.Join(d.Endpoints, ","),
		"CertificateSecret": d.CertificateSecret,
	}
}

// etcdEndpointURL gives a scheme to a DataStore endpoint: Kamaji lists them as
// host:port and always talks TLS to an etcd DataStore.
func etcdEndpointURL(endpoint string) string {
	if strings.Contains(endpoint, "://") {
		return endpoint
	}
	return "https://" + endpoint
}

// resolveKubernetesDatastore reads the cluster's key prefix and client
// certificate from its TenantControlPlane and the client URLs from the
// DataStore it points at.
func resolveKubernetesDatastore(ctx context.Context, dyn dynamic.Interface, namespace, appName string) (*kubernetesDatastore, error) {
	release := kubernetesReleaseName(appName)
	tcp, err := dyn.Resource(tenantControlPlaneGVR).Namespace(namespace).Get(ctx, release, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if driver, _, _ := unstructured.NestedString(tcp.Object, "status", "storage", "driver"); driver != "" && driver != "etcd" {
		return nil, fmt.Errorf("TenantControlPlane %s/%s uses the %q datastore driver; only etcd is supported", namespace, release, driver)
	}
	prefix, _, _ := unstructured.NestedString(tcp.Object, "status", "storage", "setup", "schema")
	if prefix == "" {
		prefix, _, _ = unstructured.NestedString(tcp.Object, "spec", "dataStoreSchema")
	}
	if prefix == "" {
		return nil, fmt.Errorf("TenantControlPlane %s/%s reports no datastore schema yet", namespace, release)
	}
	dsName, _, _ := unstructured.NestedString(tcp.Object, "status", "storage", "dataStoreName")
	if dsName == "" {
		dsName, _, _ = unstructured.NestedString(tcp.Object, "spec", "dataStore")
	}
	if dsName == "" {
		return nil, fmt.Errorf("TenantControlPlane %s/%s names no DataStore", namespace, release)
	}
	certSecret, _, _ := unstructured.NestedString(tcp.Object, "status", "storage", "certificate", "secretName")
	if certSecret == "" {
		certSecret = release + "-datastore-certificate"
	}
	ds, err := dyn.Resource(kamajiDataStoreGVR).Get(ctx, dsName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get DataStore %s: %w", dsName, err)
	}
	raw, _, _ := unstructured.NestedStringSlice(ds.Object, "spec", "endpoints")
	if len(raw) == 0 {
		return nil, fmt.Errorf("DataStore %s lists no endpoints", dsName)
	}
	endpoints := make([]string, 0, len(raw))
	for _, e := range raw {
		endpoints = append(endpoints, etcdEndpointURL(e))
	}
	return &kubernetesDatastore{Prefix: prefix, Endpoints: endpoints, CertificateSecret: certSecret}, nil
}

// kubernetesCertificateWarnings reports every certificate in the given Secrets
// that expires inside its window: caWindow for CAs, leafWindow for the rest.
// Output is sorted so a condition message is stable across reconciles.
func kubernetesCertificateWarnings(secrets []corev1.Secret, now time.Time, caWindow, leafWindow time.Duration) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range secrets {
		for key, data := range s.Data {
			if !strings.HasSuffix(key, ".crt") {
				continue
			}
			for rest := data; ; {
				var block *pem.Block
				block, rest = pem.Decode(rest)
				if block == nil {
					break
				}
				if block.Type != "CERTIFICATE" {
					continue
				}
				cert, err := x509.ParseCertificate(block.Bytes)
				if err != nil {
					continue
				}
				window := leafWindow
				kind := "certificate"
				if cert.IsCA {
					window = caWindow
					kind = "CA"
				}
				if !cert.NotAfter.Before(now.Add(window)) {
					continue
				}
				msg := fmt.Sprintf("%s %s/%s (CN=%s) expires %s", kind, s.Name, key, cert.Subject.CommonName, cert.NotAfter.UTC().Format(time.RFC3339))
				if !seen[msg] {
					seen[msg] = true
					out = append(out, msg)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// kubernetesHelmReleaseNames returns the HelmRelease of the cluster followed
// by those of its worker pools. Pools live in separate kubernetes-nodes
// releases; the MachineDeployments of the cluster name them, which also
// covers the older layout where the cluster release owned its pools.
func kubernetesHelmReleaseNames(mds []unstructured.Unstructured, release string) []string {
	names := []string{release}
	for _, md := range mds {
		if cluster, _, _ := unstructured.NestedString(md.Object, "spec", "clusterName"); cluster != release {
			continue
		}
		if owner := md.GetAnnotations()[helmReleaseNameAnnotation]; owner != "" && !slices.Contains(names, owner) {
			names = append(names, owner)
		}
	}
	return names
}

// sanitizeHelmRelease keeps what is needed to recreate a HelmRelease and
// drops server-set metadata and status.
func sanitizeHelmRelease(hr *unstructured.Unstructured) map[string]any {
	labels := map[string]string{}
	for k, v := range hr.GetLabels() {
		if !strings.HasPrefix(k, "velero.io/") {
			labels[k] = v
		}
	}
	meta := map[string]any{
		"name":      hr.GetName(),
		"namespace": hr.GetNamespace(),
	}
	if len(labels) > 0 {
		meta["labels"] = labels
	}
	spec, _, _ := unstructured.NestedMap(hr.Object, "spec")
	delete(spec, "suspend")
	return map[string]any{
		"apiVersion": hr.GetAPIVersion(),
		"kind":       hr.GetKind(),
		"metadata":   meta,
		"spec":       spec,
	}
}

func renderKubernetesTemplate(
	tmpl corev1.PodTemplateSpec,
	appName, namespace, mode, objectKey string,
	ds kubernetesDatastore,
	parameters map[string]string,
) (*corev1.PodTemplateSpec, error) {
	return template.Template(&tmpl, map[string]any{
		"Release": map[string]string{
			"Name":      appName,
			"Namespace": namespace,
			"Prefix":    kubernetesReleasePrefix,
		},
		"Mode":       mode,
		"ObjectKey":  objectKey,
		"Datastore":  ds.templateContext(),
		"Parameters": parameters,
	})
}

func kubernetesStrategyParameters(b *backupsv1alpha1.Backup) map[string]string {
	out := map[string]string{}
	for k, v := range b.Spec.DriverMetadata {
		if p := strings.TrimPrefix(k, kubernetesParamPrefix); p != k && p != "" {
			out[p] = v
		}
	}
	return out
}

func kubernetesBackupDatastore(b *backupsv1alpha1.Backup) kubernetesDatastore {
	ds := kubernetesDatastore{
		Prefix:            b.Spec.DriverMetadata[kubernetesDatastorePrefixMetaKey],
		CertificateSecret: kubernetesReleaseName(b.Spec.ApplicationRef.Name) + "-datastore-certificate",
	}
	if e := b.Spec.DriverMetadata[kubernetesDatastoreEndpointsMetaKey]; e != "" {
		ds.Endpoints = strings.Split(e, ",")
	}
	return ds
}

func kubernetesBackupTTL(s *strategyv1alpha1.Kubernetes) time.Duration {
	if s.Spec.Velero.Spec.TTL.Duration > 0 {
		return s.Spec.Velero.Spec.TTL.Duration
	}
	return kubernetesDefaultBackupTTL
}

func kubernetesCertThreshold(s *strategyv1alpha1.Kubernetes) time.Duration {
	if s.Spec.CertificateWarningThreshold != nil && s.Spec.CertificateWarningThreshold.Duration > 0 {
		return s.Spec.CertificateWarningThreshold.Duration
	}
	return kubernetesDefaultCertThreshold
}

// ---------------------------------------------------------------------------
// BackupJob path
// ---------------------------------------------------------------------------

// reconcileKubernetes drives a BackupJob for a tenant Kubernetes application in
// three steps, each observed on later reconciles: check the control-plane
// certificates, dump the cluster's etcd prefix with a Job, then back up the
// host-side Secrets and volumes with Velero. The Backup records what a restore
// needs to put both halves back.
func (r *BackupJobReconciler) reconcileKubernetes(ctx context.Context, j *backupsv1alpha1.BackupJob, resolved *ResolvedBackupConfig) (ctrl.Result, error) {
	logger := getLogger(ctx)
	logger.Debug("reconciling Kubernetes strategy", "backupjob", j.Name, "phase", j.Status.Phase)

	if j.Status.Phase == backupsv1alpha1.BackupJobPhaseSucceeded ||
		j.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		return ctrl.Result{}, nil
	}
	if err := validateKubernetesApplicationRef(j.Spec.ApplicationRef); err != nil {
		return r.markBackupJobFailed(ctx, j, err.Error())
	}

	if j.Status.StartedAt == nil {
		fresh := &backupsv1alpha1.BackupJob{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: j.Namespace, Name: j.Name}, fresh); err != nil {
			return ctrl.Result{}, err
		}
		if fresh.Status.StartedAt == nil {
			base := fresh.DeepCopy()
			now := metav1.Now()
			fresh.Status.StartedAt = &now
			fresh.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
			if err := r.Status().Patch(ctx, fresh, client.MergeFrom(base)); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: kubernetesPollInterval}, nil
		}
		j.Status.StartedAt = fresh.Status.StartedAt
		j.Status.Phase = fresh.Status.Phase
	}

	strategy := &strategyv1alpha1.Kubernetes{}
	if err := r.Get(ctx, client.ObjectKey{Name: resolved.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.requeueStrategyNotReady(ctx, j, resolved.StrategyRef.Name)
		}
		return ctrl.Result{}, err
	}

	app, err := r.getApplicationUnstructured(ctx, j.Namespace, j.Spec.ApplicationRef)
	if err != nil {
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("Kubernetes application not found: %s/%s", j.Namespace, j.Spec.ApplicationRef.Name))
		}
		return ctrl.Result{}, err
	}

	appName := j.Spec.ApplicationRef.Name
	release := kubernetesReleaseName(appName)
	ds, err := resolveKubernetesDatastore(ctx, r.Interface, j.Namespace, appName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.markBackupJobFailed(ctx, j, fmt.Sprintf("Kubernetes application %s/%s has no TenantControlPlane: %v", j.Namespace, appName, err))
		}
		return r.markBackupJobFailed(ctx, j, err.Error())
	}

	// A restore that failed partway leaves the control plane stopped on a
	// possibly partial prefix, marked with the replica count it had. Backing
	// that up would turn the damage into a recovery point, so a run that has
	// not dumped anything yet refuses.
	if err := r.Get(ctx, client.ObjectKey{Namespace: j.Namespace, Name: jobNameForBackupJob(j)}, &batchv1.Job{}); apierrors.IsNotFound(err) {
		kcp, kerr := r.Interface.Resource(kamajiControlPlaneGVR).Namespace(j.Namespace).Get(ctx, release, metav1.GetOptions{})
		if kerr != nil && !apierrors.IsNotFound(kerr) && !apimeta.IsNoMatchError(kerr) {
			return ctrl.Result{}, kerr
		}
		if kerr == nil {
			if _, stopped := kcp.GetAnnotations()[kubernetesReplicasBeforeAnnotation]; stopped {
				return r.markBackupJobFailed(ctx, j, fmt.Sprintf("Kubernetes application %s/%s is stopped by an unfinished restore; finish or repair the restore before backing it up", j.Namespace, appName))
			}
		}
	} else if err != nil {
		return ctrl.Result{}, err
	}

	if apimeta.FindStatusCondition(j.Status.Conditions, kubernetesCondCertificates) == nil {
		return r.checkKubernetesCertificates(ctx, j, strategy, release)
	}

	objectKey := kubernetesObjectKey(j.Namespace, appName, j.Name)
	rendered, err := renderKubernetesTemplate(strategy.Spec.Template, appName, j.Namespace, kubernetesModeBackup, objectKey, *ds, resolved.Parameters)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to template Kubernetes strategy: %v", err))
	}
	dump, err := ensureBackupBatchJob(ctx, r.Client, r.Scheme, j, j.Namespace, jobNameForBackupJob(j),
		map[string]string{
			kubernetesLabelMode:                     kubernetesModeBackup,
			backupsv1alpha1.OwningJobNameLabel:      j.Name,
			backupsv1alpha1.OwningJobNamespaceLabel: j.Namespace,
		}, rendered)
	if err != nil {
		return r.markBackupJobFailed(ctx, j, fmt.Sprintf("failed to ensure etcd dump Job: %v", err))
	}
	switch jobConditionState(dump) {
	case batchv1.JobFailed:
		msg := jobFailureMessage(dump)
		if msg == "" {
			msg = "etcd dump Job reported Failed"
		}
		return r.markBackupJobFailed(ctx, j, msg)
	case batchv1.JobComplete:
	default:
		return ctrl.Result{RequeueAfter: kubernetesPollInterval}, nil
	}

	veleroBackups := &velerov1.BackupList{}
	if err := r.List(ctx, veleroBackups, client.InNamespace(veleroNamespace), client.MatchingLabels{
		backupsv1alpha1.OwningJobNamespaceLabel: j.Namespace,
		backupsv1alpha1.OwningJobNameLabel:      j.Name,
	}); err != nil {
		return ctrl.Result{}, err
	}
	if len(veleroBackups.Items) == 0 {
		if err := r.labelKubernetesSecrets(ctx, j.Namespace, release); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.createKubernetesVeleroBackup(ctx, j, strategy, app, resolved); err != nil {
			return r.failKubernetesBackupAfterDump(ctx, j, strategy, *ds, objectKey, resolved, fmt.Sprintf("failed to create Velero Backup: %v", err))
		}
		return ctrl.Result{RequeueAfter: kubernetesPollInterval}, nil
	}
	if len(veleroBackups.Items) > 1 {
		return r.markBackupJobFailed(ctx, j, "more than one Velero Backup references this BackupJob")
	}
	vb := &veleroBackups.Items[0]
	switch vb.Status.Phase {
	case velerov1.BackupPhaseCompleted:
	case velerov1.BackupPhaseFailed, velerov1.BackupPhasePartiallyFailed, velerov1.BackupPhaseFailedValidation:
		return r.failKubernetesBackupAfterDump(ctx, j, strategy, *ds, objectKey, resolved, formatVeleroBackupFailureMessageForBackupJob(ctx, r.Client, vb))
	default:
		return ctrl.Result{RequeueAfter: kubernetesPollInterval}, nil
	}

	if j.Status.BackupRef != nil {
		return ctrl.Result{}, nil
	}
	// A failed create is retried rather than failing the run: the dump and
	// the Velero backup already exist, and no Backup would own them.
	artifact, err := r.createKubernetesBackupArtifact(ctx, j, resolved, vb, *ds, objectKey)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("create Backup artifact: %w", err)
	}
	now := metav1.Now()
	j.Status.BackupRef = &corev1.LocalObjectReference{Name: artifact.Name}
	j.Status.CompletedAt = &now
	j.Status.Phase = backupsv1alpha1.BackupJobPhaseSucceeded
	apimeta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionTrue,
		Reason:  "BackupCompleted",
		Message: "etcd prefix and Velero backup completed",
	})
	return ctrl.Result{}, r.Status().Update(ctx, j)
}

// failKubernetesBackupAfterDump fails a run whose etcd dump is already in the
// bucket. No Backup will own the dump, so a cleanup-mode Job owned by the
// BackupJob removes it; the dump holds every Secret of the cluster.
func (r *BackupJobReconciler) failKubernetesBackupAfterDump(ctx context.Context, j *backupsv1alpha1.BackupJob, strategy *strategyv1alpha1.Kubernetes, ds kubernetesDatastore, objectKey string, resolved *ResolvedBackupConfig, msg string) (ctrl.Result, error) {
	rendered, err := renderKubernetesTemplate(strategy.Spec.Template, j.Spec.ApplicationRef.Name, j.Namespace, kubernetesModeCleanup, objectKey, ds, resolved.Parameters)
	if err == nil {
		_, err = ensureBackupBatchJob(ctx, r.Client, r.Scheme, j, j.Namespace, j.Name+"-discard",
			map[string]string{
				kubernetesLabelMode:                     kubernetesModeCleanup,
				backupsv1alpha1.OwningJobNameLabel:      j.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: j.Namespace,
			}, rendered)
	}
	if err != nil {
		msg = fmt.Sprintf("%s; the etcd dump %s could not be scheduled for deletion: %v", msg, objectKey, err)
	}
	return r.markBackupJobFailed(ctx, j, msg)
}

// checkKubernetesCertificates records on the BackupJob whether any
// control-plane certificate expires inside its window. It never fails the
// run: an expiring certificate is a reason to act, not to skip the backup.
func (r *BackupJobReconciler) checkKubernetesCertificates(ctx context.Context, j *backupsv1alpha1.BackupJob, strategy *strategyv1alpha1.Kubernetes, release string) (ctrl.Result, error) {
	var secrets []corev1.Secret
	for _, suffix := range kubernetesCertificateSecrets {
		s := corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{Namespace: j.Namespace, Name: release + "-" + suffix}, &s)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return ctrl.Result{}, err
		}
		secrets = append(secrets, s)
	}
	warnings := kubernetesCertificateWarnings(secrets, time.Now(), kubernetesBackupTTL(strategy), kubernetesCertThreshold(strategy))
	cond := metav1.Condition{
		Type:    kubernetesCondCertificates,
		Status:  metav1.ConditionFalse,
		Reason:  "CertificatesValid",
		Message: "no control-plane certificate expires within the warning window",
	}
	if len(warnings) > 0 {
		cond.Status = metav1.ConditionTrue
		cond.Reason = "CertificatesExpiring"
		cond.Message = strings.Join(warnings, "; ")
		if r.Recorder != nil {
			r.Recorder.Event(j, corev1.EventTypeWarning, "CertificatesExpiring", cond.Message)
		}
	}
	apimeta.SetStatusCondition(&j.Status.Conditions, cond)
	if err := r.Status().Update(ctx, j); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: kubernetesPollInterval}, nil
}

func (r *BackupJobReconciler) labelKubernetesSecrets(ctx context.Context, namespace, release string) error {
	for _, suffix := range kubernetesUnlabelledSecrets {
		s := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: release + "-" + suffix}, s)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if s.Labels[helmInstanceLabel] == release {
			continue
		}
		base := s.DeepCopy()
		if s.Labels == nil {
			s.Labels = map[string]string{}
		}
		s.Labels[helmInstanceLabel] = release
		if err := r.Patch(ctx, s, client.MergeFrom(base)); err != nil {
			return fmt.Errorf("label Secret %s/%s: %w", namespace, s.Name, err)
		}
	}
	return nil
}

func (r *BackupJobReconciler) createKubernetesVeleroBackup(ctx context.Context, j *backupsv1alpha1.BackupJob, strategy *strategyv1alpha1.Kubernetes, app map[string]any, resolved *ResolvedBackupConfig) error {
	spec, err := template.Template(&strategy.Spec.Velero.Spec, map[string]any{
		"Application": app,
		"Parameters":  resolved.Parameters,
	})
	if err != nil {
		return err
	}
	// The volumes an in-place restore deletes and replaces are listed before
	// Velero starts: each of them is then in the Velero backup unless it was
	// deleted first, and a volume created while the backup runs, which Velero
	// may have missed, is never deleted by a restore of it.
	volumes, err := r.kubernetesDataVolumeNames(ctx, j.Namespace, kubernetesReleaseName(j.Spec.ApplicationRef.Name))
	if err != nil {
		return err
	}
	vb := &velerov1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s.%s-", j.Namespace, j.Name),
			Namespace:    veleroNamespace,
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      j.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: j.Namespace,
			},
			Annotations: map[string]string{kubernetesDataVolumesMetaKey: strings.Join(volumes, ",")},
		},
		Spec: *spec,
	}
	if err := r.Create(ctx, vb); err != nil {
		return err
	}
	if r.Recorder != nil {
		r.Recorder.Event(j, corev1.EventTypeNormal, "VeleroBackupCreated", fmt.Sprintf("Created Velero Backup %s/%s", veleroNamespace, vb.Name))
	}
	return nil
}

// kubernetesDataVolumeNames lists the claims backing the cluster's
// PersistentVolumes: the DataVolumes and PVCs the CSI driver labels with the
// cluster name. A PVC is listed even without a DataVolume, so an in-place
// restore clears it too rather than leaving Velero to skip it.
func (r *BackupJobReconciler) kubernetesDataVolumeNames(ctx context.Context, ns, release string) ([]string, error) {
	selector := clusterNameLabel + "=" + release
	seen := map[string]bool{}
	dvs, err := r.Interface.Resource(dataVolumeGVR).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	for _, dv := range dvs.Items {
		seen[dv.GetName()] = true
	}
	pvcs := &corev1.PersistentVolumeClaimList{}
	if err := r.List(ctx, pvcs, client.InNamespace(ns), client.MatchingLabels{clusterNameLabel: release}); err != nil {
		return nil, err
	}
	for _, pvc := range pvcs.Items {
		seen[pvc.Name] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// createKubernetesBackupArtifact records, besides the Velero backup and the
// etcd object, the cluster's HelmReleases and data DataVolumes. The
// HelmReleases are kept here rather than in Velero because selecting them by
// application label would also select the worker system disks, which the
// lineage webhook labels after the same application.
func (r *BackupJobReconciler) createKubernetesBackupArtifact(
	ctx context.Context,
	j *backupsv1alpha1.BackupJob,
	resolved *ResolvedBackupConfig,
	vb *velerov1.Backup,
	ds kubernetesDatastore,
	objectKey string,
) (*backupsv1alpha1.Backup, error) {
	release := kubernetesReleaseName(j.Spec.ApplicationRef.Name)
	mds, err := r.Interface.Resource(machineDeploymentGVR).Namespace(j.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil && !apierrors.IsNotFound(err) && !apimeta.IsNoMatchError(err) {
		return nil, err
	}
	var mdItems []unstructured.Unstructured
	if mds != nil {
		mdItems = mds.Items
	}
	var releases []map[string]any
	for _, name := range kubernetesHelmReleaseNames(mdItems, release) {
		hr, err := r.Interface.Resource(helmReleaseGVR).Namespace(j.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		releases = append(releases, sanitizeHelmRelease(hr))
	}
	releasesJSON, err := json.Marshal(releases)
	if err != nil {
		return nil, err
	}

	driverMD := map[string]string{
		veleroBackupNameMetadataKey:         vb.Name,
		veleroBackupNamespaceMetadataKey:    vb.Namespace,
		kubernetesObjectMetaKey:             objectKey,
		kubernetesDatastorePrefixMetaKey:    ds.Prefix,
		kubernetesDatastoreEndpointsMetaKey: strings.Join(ds.Endpoints, ","),
		kubernetesHelmReleasesMetaKey:       string(releasesJSON),
		kubernetesDataVolumesMetaKey:        vb.Annotations[kubernetesDataVolumesMetaKey],
	}
	if c := apimeta.FindStatusCondition(j.Status.Conditions, kubernetesCondCertificates); c != nil && c.Status == metav1.ConditionTrue {
		driverMD[kubernetesCertWarningsMetaKey] = c.Message
	}
	for k, v := range resolved.Parameters {
		driverMD[kubernetesParamPrefix+k] = v
	}

	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: j.Name, Namespace: j.Namespace},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: j.Spec.ApplicationRef,
			StrategyRef:    resolved.StrategyRef,
			TakenAt:        metav1.Now(),
			DriverMetadata: driverMD,
			PlanRef:        j.Spec.PlanRef,
		},
		Status: backupsv1alpha1.BackupStatus{
			Phase:    backupsv1alpha1.BackupPhaseReady,
			Artifact: &backupsv1alpha1.BackupArtifact{URI: fmt.Sprintf("velero://%s/%s", vb.Namespace, vb.Name)},
		},
	}
	if err := r.Create(ctx, backup); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		existing := &backupsv1alpha1.Backup{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: backup.Name}, existing); err != nil {
			return nil, err
		}
		return existing, nil
	}
	return backup, nil
}

// ---------------------------------------------------------------------------
// Backup deletion cleanup
// ---------------------------------------------------------------------------

// cleanupKubernetesBackup removes both halves of a deleted Backup: the Velero
// backup through a DeleteBackupRequest, and the etcd dump object through a
// cleanup-mode Job, waiting for the Job before the Backup is released. The
// escape hatches match the Redis driver's.
func (r *BackupReconciler) cleanupKubernetesBackup(ctx context.Context, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	if err := r.cleanupVeleroBackup(ctx, backup); err != nil {
		return ctrl.Result{}, err
	}

	jobName := backup.Name + "-cleanup"
	if backup.Annotations[redisSkipArtifactCleanupAnnotation] == "true" {
		existing := &batchv1.Job{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: jobName}, existing); err == nil {
			_ = r.deleteRedisCleanupJob(ctx, existing)
		}
		return ctrl.Result{}, nil
	}
	objectKey := backup.Spec.DriverMetadata[kubernetesObjectMetaKey]
	if objectKey == "" {
		return ctrl.Result{}, nil
	}

	strategy := &strategyv1alpha1.Kubernetes{}
	if err := r.Get(ctx, client.ObjectKey{Name: backup.Spec.StrategyRef.Name}, strategy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.releaseRedisCleanup(ctx, backup, objectKey, "strategy CR is gone"), nil
		}
		return ctrl.Result{}, err
	}

	job := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: jobName}, job)
	switch {
	case apierrors.IsNotFound(err):
		terminating, nsErr := r.namespaceTerminating(ctx, backup.Namespace)
		if nsErr != nil {
			return ctrl.Result{}, nsErr
		}
		if terminating {
			return r.releaseRedisCleanup(ctx, backup, objectKey, "namespace is terminating"), nil
		}
		if perr := ProjectBackupCredentials(ctx, r.Client, r.CredentialsConfig, backup.Namespace); perr != nil {
			return r.releaseRedisCleanup(ctx, backup, objectKey, fmt.Sprintf("cannot project credentials: %v", perr)), nil
		}
		rendered, rerr := renderKubernetesTemplate(strategy.Spec.Template, backup.Spec.ApplicationRef.Name, backup.Namespace,
			kubernetesModeCleanup, objectKey, kubernetesBackupDatastore(backup), kubernetesStrategyParameters(backup))
		if rerr != nil {
			return r.releaseRedisCleanup(ctx, backup, objectKey, fmt.Sprintf("cleanup template render failed: %v", rerr)), nil
		}
		cleanup := buildRedisCleanupJob(backup.Namespace, jobName, rendered)
		cleanup.Labels = map[string]string{kubernetesLabelMode: kubernetesModeCleanup}
		delete(cleanup.Spec.Template.Labels, redisLabelMode)
		maps.Copy(cleanup.Spec.Template.Labels, cleanup.Labels)
		if cerr := r.Create(ctx, cleanup); cerr != nil && !apierrors.IsAlreadyExists(cerr) {
			if apierrors.IsForbidden(cerr) || apierrors.IsInvalid(cerr) {
				return r.releaseRedisCleanup(ctx, backup, objectKey, fmt.Sprintf("cannot create cleanup Job: %v", cerr)), nil
			}
			return ctrl.Result{}, cerr
		}
		return ctrl.Result{RequeueAfter: kubernetesPollInterval}, nil
	case err != nil:
		return ctrl.Result{}, err
	}

	if !job.DeletionTimestamp.IsZero() {
		return ctrl.Result{RequeueAfter: kubernetesPollInterval}, nil
	}
	switch jobConditionState(job) {
	case batchv1.JobComplete:
		_ = r.deleteRedisCleanupJob(ctx, job)
		return ctrl.Result{}, nil
	case batchv1.JobFailed:
		attempts, _ := strconv.Atoi(backup.Annotations[kubernetesCleanupAttemptsAnnotation])
		attempts++
		if attempts >= kubernetesCleanupMaxAttempts {
			_ = r.deleteRedisCleanupJob(ctx, job)
			return r.releaseRedisCleanup(ctx, backup, objectKey, fmt.Sprintf("the cleanup Job failed %d times: %s", attempts, jobFailureMessage(job))), nil
		}
		base := backup.DeepCopy()
		if backup.Annotations == nil {
			backup.Annotations = map[string]string{}
		}
		backup.Annotations[kubernetesCleanupAttemptsAnnotation] = strconv.Itoa(attempts)
		if err := r.Patch(ctx, backup, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
		_ = r.deleteRedisCleanupJob(ctx, job)
		return ctrl.Result{RequeueAfter: time.Duration(attempts) * 30 * time.Second}, nil
	default:
		return ctrl.Result{RequeueAfter: kubernetesPollInterval}, nil
	}
}
