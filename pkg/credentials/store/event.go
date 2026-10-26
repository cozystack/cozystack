// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"fmt"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

// Event reasons the API records on an application for its credentials.
const (
	ReasonCredentialIssued  = "CredentialIssued"
	ReasonCredentialRevoked = "CredentialRevoked"
)

// Emit records an Event on the application naming the account and the caller,
// never a value. Best effort: a failure is logged and does not fail the
// operation, which the audit log records in any case.
func Emit(ctx context.Context, c client.Client, hr *helmv2.HelmRelease, reason, username, by string) {
	now := metav1.NewTime(time.Now())
	verb := "issued"
	if reason == ReasonCredentialRevoked {
		verb = "revoked"
	}
	ev := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: hr.Labels[appsv1alpha1.ApplicationNameLabel] + ".",
			Namespace:    hr.Namespace,
		},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: appsv1alpha1.SchemeGroupVersion.String(),
			Kind:       hr.Labels[appsv1alpha1.ApplicationKindLabel],
			Name:       hr.Labels[appsv1alpha1.ApplicationNameLabel],
			Namespace:  hr.Namespace,
			UID:        hr.UID,
		},
		Reason:         reason,
		Message:        fmt.Sprintf("password of user %s %s by %s", username, verb, by),
		Type:           corev1.EventTypeNormal,
		Source:         corev1.EventSource{Component: "cozystack-api"},
		FirstTimestamp: now,
		LastTimestamp:  now,
		Count:          1,
	}
	if err := c.Create(ctx, ev); err != nil {
		klog.Warningf("failed to record %s event for %s/%s: %v", reason, hr.Namespace, hr.Name, err)
	}
}
