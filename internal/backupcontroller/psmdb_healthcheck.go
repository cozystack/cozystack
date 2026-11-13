// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/psmdbtypes"
)

// The operator's ready status can predate the restore. Authenticate from a
// separate Pod, using the internal users Secret the pinned PSMDB operator uses.
func (r *RestoreJobReconciler) ensureMongoDBHealthCheck(ctx context.Context, rj *backupsv1alpha1.RestoreJob, cluster *psmdbtypes.PerconaServerMongoDB, timeout time.Duration) (*batchv1.Job, error) {
	if cluster.Namespace != rj.Namespace || cluster.Spec.Image == "" || cluster.Spec.Secrets == nil || cluster.Spec.Secrets.SSL == "" {
		return nil, fmt.Errorf("MongoDB health check requires the target image and TLS Secret in the RestoreJob namespace")
	}
	// The chart's internal service is covered by its certificate. status.host
	// can contain external LoadBalancer IPs, which are not certificate SANs.
	service := cluster.Name + "-rs0"
	if cluster.Spec.Sharding != nil && cluster.Spec.Sharding.Enabled {
		service = cluster.Name + "-mongos"
	}
	users := "internal-" + cluster.Name + "-users"
	pod := &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		AutomountServiceAccountToken: ptr.To(false),
		ActiveDeadlineSeconds:        ptr.To(int64(timeout.Seconds())),
		Containers: []corev1.Container{{
			Name: "check", Image: cluster.Spec.Image,
			Command: []string{"/bin/bash", "-ec"},
			Args: []string{`umask 077
cat /tls/tls.key /tls/tls.crt > /tmp/client.pem
until mongosh "mongodb://${MONGODB_HOST}/admin?serverSelectionTimeoutMS=5000" \
  --tls --tlsCAFile /tls/ca.crt --tlsCertificateKeyFile /tmp/client.pem --quiet \
  --username "$MONGODB_CLUSTER_ADMIN_USER" --password "$MONGODB_CLUSTER_ADMIN_PASSWORD" \
  --eval 'if (db.runCommand({ping: 1}).ok !== 1) quit(1)'; do sleep 5; done`},
			Env: []corev1.EnvVar{
				{Name: "MONGODB_HOST", Value: service + "." + cluster.Namespace},
				{Name: "MONGODB_CLUSTER_ADMIN_USER", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: users}, Key: "MONGODB_CLUSTER_ADMIN_USER"}}},
				{Name: "MONGODB_CLUSTER_ADMIN_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: users}, Key: "MONGODB_CLUSTER_ADMIN_PASSWORD"}}},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
				Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
			},
			VolumeMounts: []corev1.VolumeMount{{Name: "tls", MountPath: "/tls", ReadOnly: true}},
		}},
		Volumes: []corev1.Volume{{Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: cluster.Spec.Secrets.SSL,
			Items: []corev1.KeyToPath{
				{Key: "ca.crt", Path: "ca.crt"},
				{Key: "tls.crt", Path: "tls.crt"},
				{Key: "tls.key", Path: "tls.key"},
			},
		}}}},
	}}
	return ensureBackupBatchJob(ctx, r.Client, r.Scheme, rj, rj.Namespace, "mongodb-health-"+string(rj.UID), nil, pod)
}
