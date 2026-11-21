// SPDX-License-Identifier: Apache-2.0

// Package account is what the Cozystack API, the CredentialTemplate controller
// and the migration tooling share about account Secrets: their names, the
// labels and annotations that carry the record, and how the stored value
// reaches a chart through valuesFrom. It writes nothing itself.
package account

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

const (
	// AccountLabel marks a Secret the API writes for one account, so the
	// Credential view can list by label without matching any other Secret of
	// the application.
	AccountLabel = "credentials.cozystack.io/account"
	// OriginAnnotation holds the CredentialOrigin of the live password.
	OriginAnnotation = "credentials.cozystack.io/origin"
	// AtAnnotation holds when the live password was set, RFC 3339.
	AtAnnotation = "credentials.cozystack.io/at"
	// ByAnnotation holds who set the live password.
	ByAnnotation = "credentials.cozystack.io/by"
	// HistoryAnnotation holds the last HistoryLimit operations as a JSON list,
	// newest first.
	HistoryAnnotation = "credentials.cozystack.io/history"
	// HistoryLimit bounds the history kept on the Secret; the audit log keeps
	// every operation.
	HistoryLimit = 10
	// FormatAnnotation names the format the Secret's password is stored in, so
	// the controller sees when a template's format changed.
	FormatAnnotation = "credentials.cozystack.io/format"
	// SeedAnnotation on a HelmRelease lets the controller seed the account
	// Secrets of its templates. The API sets it on every release it creates,
	// the conversion on a release whose Secrets it wrote; a release without it
	// keeps passwords the platform never saw, so its accounts are left for a
	// mint.
	SeedAnnotation = "credentials.cozystack.io/seed"
	// ReleaseNameAnnotation is what Helm puts on every object it renders,
	// naming the release; it ties a template to its HelmRelease.
	ReleaseNameAnnotation = "meta.helm.sh/release-name"
	// SecretSuffix follows the template name in the account Secret's name.
	SecretSuffix = ".account"
	// UsernameKey and PasswordKey are the two data keys of every account
	// Secret, whatever the format.
	UsernameKey = "username"
	PasswordKey = "password"
)

var nonNameChars = regexp.MustCompile(`[^a-z0-9-]`)

// Account is one tenant account of an application.
type Account struct {
	// Username as the application declares it and the engine knows it.
	Username string
	// Mapped is Username lowercased with every character outside [a-z0-9-]
	// replaced by "-"; it names the Secret and the Credential.
	Mapped string
}

// MapName returns the object-name form of a user name, or an error when the
// form is not a valid DNS label, which no Secret or Credential name can carry.
func MapName(username string) (string, error) {
	mapped := nonNameChars.ReplaceAllString(strings.ToLower(username), "-")
	if errs := validation.IsDNS1123Label(mapped); len(errs) > 0 {
		return "", fmt.Errorf("user name %q maps to %q, which is not a valid name: %s", username, mapped, strings.Join(errs, "; "))
	}
	return mapped, nil
}

// SecretName is the account Secret of a user of a release.
func SecretName(release, mapped string) string {
	return release + "." + mapped + SecretSuffix
}

// CredentialName is the Credential of a user of a release.
func CredentialName(release, mapped string) string {
	return release + "." + mapped
}

// SplitCredentialName returns the release and mapped user of a Credential
// name. Release names carry no dot, so the first one separates them.
func SplitCredentialName(name string) (release, mapped string, ok bool) {
	release, mapped, ok = strings.Cut(name, ".")
	if !ok || release == "" || mapped == "" || strings.Contains(mapped, ".") {
		return "", "", false
	}
	return release, mapped, true
}

// Record is the per-account record an account Secret carries in its
// annotations and the Credential view shows.
type Record struct {
	Origin  corev1alpha1.CredentialOrigin
	At      metav1.Time
	By      string
	History []corev1alpha1.CredentialHistoryEntry
}

// ReadRecord decodes the record of an account Secret. A Secret with no
// annotations yields an empty record rather than an error, so a Secret
// written by hand still lists.
func ReadRecord(sec *corev1.Secret) Record {
	var r Record
	ann := sec.GetAnnotations()
	r.Origin = corev1alpha1.CredentialOrigin(ann[OriginAnnotation])
	r.By = ann[ByAnnotation]
	if at := ann[AtAnnotation]; at != "" {
		if t, err := parseTime(at); err == nil {
			r.At = t
		}
	}
	if h := ann[HistoryAnnotation]; h != "" {
		_ = json.Unmarshal([]byte(h), &r.History)
	}
	return r
}

// WriteRecord encodes the record into the Secret's annotations.
func WriteRecord(sec *corev1.Secret, r Record) {
	if sec.Annotations == nil {
		sec.Annotations = map[string]string{}
	}
	sec.Annotations[OriginAnnotation] = string(r.Origin)
	sec.Annotations[ByAnnotation] = r.By
	sec.Annotations[AtAnnotation] = r.At.UTC().Format(timeLayout)
	history, _ := json.Marshal(r.History)
	sec.Annotations[HistoryAnnotation] = string(history)
}

// Apply records an operation: the origin becomes the operation's, At and By
// are the operation's, and the entry goes to the front of the history, which
// keeps HistoryLimit entries.
func (r Record) Apply(op corev1alpha1.CredentialOperation, at metav1.Time, by string) Record {
	r.Origin = originOf(op)
	r.At = at
	r.By = by
	entry := corev1alpha1.CredentialHistoryEntry{Operation: op, At: at, By: by}
	history := make([]corev1alpha1.CredentialHistoryEntry, 0, len(r.History)+1)
	history = append(history, entry)
	history = append(history, r.History...)
	if len(history) > HistoryLimit {
		history = history[:HistoryLimit]
	}
	r.History = history
	return r
}

func originOf(op corev1alpha1.CredentialOperation) corev1alpha1.CredentialOrigin {
	switch op {
	case corev1alpha1.CredentialOperationMigrate:
		return corev1alpha1.CredentialOriginMigrated
	case corev1alpha1.CredentialOperationMint:
		return corev1alpha1.CredentialOriginMinted
	case corev1alpha1.CredentialOperationRevoke:
		return corev1alpha1.CredentialOriginRevoked
	}
	return corev1alpha1.CredentialOriginNotIssued
}

const timeLayout = "2006-01-02T15:04:05Z"

func parseTime(s string) (metav1.Time, error) {
	var t metav1.Time
	err := t.UnmarshalQueryParameter(s)
	return t, err
}

// Labels are the labels an account Secret carries: those its template names,
// and the account marker, which a template cannot override.
func Labels(templateLabels map[string]string) map[string]string {
	labels := make(map[string]string, len(templateLabels)+1)
	for k, v := range templateLabels {
		labels[k] = v
	}
	labels[AccountLabel] = "true"
	return labels
}

// DigestKey is the values key one account's stored value lands under: the
// SHA-256 of the user name, which fits a valuesFrom targetPath whatever the
// name holds.
func DigestKey(username string) string {
	sum := sha256.Sum256([]byte(username))
	return "u" + hex.EncodeToString(sum[:])
}

// ValuesFrom is the HelmRelease valuesFrom entry that carries an account's
// stored value into <valuesPath>.<DigestKey>. It is optional, so the release
// renders while the account has no Secret yet.
func ValuesFrom(secretName, valuesPath, username string) helmv2.ValuesReference {
	return helmv2.ValuesReference{
		Kind:       "Secret",
		Name:       secretName,
		ValuesKey:  PasswordKey,
		TargetPath: valuesPath + "." + DigestKey(username),
		Optional:   true,
	}
}

// IsAccountValuesFrom reports whether a valuesFrom entry is one ValuesFrom
// builds, which the controller owns and the Application API keeps.
func IsAccountValuesFrom(ref helmv2.ValuesReference) bool {
	return ref.Kind == "Secret" && ref.ValuesKey == PasswordKey && strings.HasSuffix(ref.Name, SecretSuffix)
}

// IsAccountSecret reports whether a Secret is one the API writes for an
// account.
func IsAccountSecret(sec *corev1.Secret) bool {
	return sec != nil && sec.Labels[AccountLabel] == "true"
}
