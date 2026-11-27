// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// reasonRetrying names an Event for a release that hit a transient error and
// is converted on a later attempt, unlike reasonSkipped, which needs a person.
const reasonRetrying = "CredentialsMigrationRetrying"

// transientAttempts and transientDelay bound the in-process retries of a
// release that hit a transient error, before the Job's own restart retries it.
var (
	transientAttempts = 3
	transientDelay    = 10 * time.Second
)

// isTransient reports whether err comes from the API server or the
// connection to it rather than from the release: retrying the same release
// can succeed without anyone changing anything.
func isTransient(err error) bool {
	switch {
	case apierrors.IsServerTimeout(err), apierrors.IsTimeout(err), apierrors.IsTooManyRequests(err),
		apierrors.IsServiceUnavailable(err), apierrors.IsInternalError(err), apierrors.IsUnexpectedServerError(err),
		apierrors.IsConflict(err):
		return true
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.EPIPE):
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// client-go wraps a body read that failed mid-response this way.
	return strings.Contains(err.Error(), "unexpected error when reading response body")
}

// finishReleaseRetrying runs finishRelease, retrying a transient error on a
// fresh read of the release.
func (m *migrator) finishReleaseRetrying(ctx context.Context, hr *helmv2.HelmRelease, rec *releaseState) error {
	var err error
	for attempt := 1; ; attempt++ {
		if err = m.finishRelease(ctx, hr, rec); err == nil || !isTransient(err) || attempt >= transientAttempts {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s/%s: transient error, retrying: %v\n", hr.Namespace, hr.Name, err)
		time.Sleep(transientDelay)
		if gerr := m.c.Get(ctx, client.ObjectKeyFromObject(hr), hr); gerr != nil && !isTransient(gerr) {
			return gerr
		}
	}
}

// leaveOut records a release finish could not convert. A transient error is
// recorded for the alert like any other, but announced as a retry rather
// than as a release left out, since the Job's restart converts it.
func (m *migrator) leaveOut(ctx context.Context, st *state, hr *helmv2.HelmRelease, cause error) {
	if !isTransient(cause) {
		m.skip(ctx, st, hr, cause)
		return
	}
	key := hr.Namespace + "/" + hr.Name
	st.Skipped[key] = fmt.Sprintf("transient error, retried by the Job: %s at %s", cause.Error(), m.now().UTC().Format(time.RFC3339))
	fmt.Fprintf(os.Stderr, "%s: transient error, retried at the next attempt: %v\n", key, cause)
	ev := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{GenerateName: hr.Name + ".", Namespace: hr.Namespace},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: helmv2.GroupVersion.String(), Kind: helmv2.HelmReleaseKind,
			Name: hr.Name, Namespace: hr.Namespace, UID: hr.UID,
		},
		Reason:         reasonRetrying,
		Message:        fmt.Sprintf("credentials migration %d hit a transient error on this release and retries it: %v", m.o.migration, cause),
		Type:           corev1.EventTypeNormal,
		Source:         corev1.EventSource{Component: "credentials-migrate"},
		FirstTimestamp: metav1.NewTime(m.now()),
		LastTimestamp:  metav1.NewTime(m.now()),
		Count:          1,
	}
	if err := m.c.Create(ctx, ev); err != nil {
		fmt.Fprintf(os.Stderr, "%s: failed to record event: %v\n", key, err)
	}
}
