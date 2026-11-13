// SPDX-License-Identifier: Apache-2.0

// Package account is what the Cozystack API and the migration tooling share
// about account Secrets: their names, the labels and annotations that carry
// the record, and how a kind's credentials block yields the accounts of one
// application. It writes nothing itself.
package account

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/format"
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
	// SecretSuffix follows the mapped account name in the Secret's name.
	SecretSuffix = ".account"
	// UsernameKey and PasswordKey are the two data keys of every account
	// Secret, whatever the format.
	UsernameKey = "username"
	PasswordKey = "password"
)

// ErrNotBuilt marks a declared format or source kind the API cannot serve yet.
var ErrNotBuilt = errors.New("declared but not built")

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

// Validate reports whether the API can serve a kind with this block: a format
// or a source kind that is declared but not built, an unknown built-in format,
// or a source missing the field its kind needs, refuses the whole kind.
func Validate(c *cozyv1alpha1.ApplicationDefinitionCredentials) error {
	if c == nil {
		return nil
	}
	switch c.Format.Kind {
	case "", cozyv1alpha1.CredentialFormatBuiltIn:
		if !format.Known(c.Format.Name) {
			return fmt.Errorf("format %q is not a built-in format", c.Format.Name)
		}
	case cozyv1alpha1.CredentialFormatWebhook:
		return fmt.Errorf("format kind Webhook: %w", ErrNotBuilt)
	default:
		return fmt.Errorf("format kind %q is unknown", c.Format.Kind)
	}
	if c.ValuesPath != "" && !strings.HasPrefix(c.ValuesPath, "_") {
		return fmt.Errorf("valuesPath %q must start with \"_\"", c.ValuesPath)
	}
	if len(c.Accounts) == 0 {
		return errors.New("accounts is empty")
	}
	for i, s := range c.Accounts {
		switch s.Kind {
		case cozyv1alpha1.CredentialAccountsValuesMap:
			if s.Path == "" {
				return fmt.Errorf("accounts[%d]: a ValuesMap source needs a path", i)
			}
		case cozyv1alpha1.CredentialAccountsStatic:
			if s.Name == "" {
				return fmt.Errorf("accounts[%d]: a Static source needs a name", i)
			}
		case cozyv1alpha1.CredentialAccountsValuesList, cozyv1alpha1.CredentialAccountsExpression, cozyv1alpha1.CredentialAccountsWebhook:
			return fmt.Errorf("accounts[%d]: source kind %s: %w", i, s.Kind, ErrNotBuilt)
		default:
			return fmt.Errorf("accounts[%d]: source kind %q is unknown", i, s.Kind)
		}
		switch s.Owner {
		case "", cozyv1alpha1.CredentialAccountOwnerTenant, cozyv1alpha1.CredentialAccountOwnerPlatform:
		default:
			return fmt.Errorf("accounts[%d]: owner %q is unknown", i, s.Owner)
		}
	}
	return nil
}

// Resolve returns the tenant accounts of an application from its defaulted
// spec values, in declaration order. Platform accounts are left out, and a
// ValuesMap key that names one is skipped. Two users that map to one name,
// or a user whose name maps to an invalid one, are an error naming them.
func Resolve(c *cozyv1alpha1.ApplicationDefinitionCredentials, values map[string]any) ([]Account, error) {
	if c == nil {
		return nil, nil
	}
	platform := map[string]bool{}
	for _, s := range c.Accounts {
		if s.Kind == cozyv1alpha1.CredentialAccountsStatic && s.Owner == cozyv1alpha1.CredentialAccountOwnerPlatform {
			platform[s.Name] = true
		}
	}
	var accounts []Account
	byMapped := map[string]string{}
	add := func(username string) error {
		mapped, err := MapName(username)
		if err != nil {
			return err
		}
		if other, dup := byMapped[mapped]; dup {
			if other == username {
				return nil
			}
			return fmt.Errorf("users %q and %q both map to the name %q; rename one of them", other, username, mapped)
		}
		byMapped[mapped] = username
		accounts = append(accounts, Account{Username: username, Mapped: mapped})
		return nil
	}
	for _, s := range c.Accounts {
		switch s.Kind {
		case cozyv1alpha1.CredentialAccountsValuesMap:
			m, _ := Lookup(values, s.Path).(map[string]any)
			for _, name := range sortedKeys(m) {
				if platform[name] {
					continue
				}
				if err := add(name); err != nil {
					return nil, err
				}
			}
		case cozyv1alpha1.CredentialAccountsStatic:
			if s.Owner == cozyv1alpha1.CredentialAccountOwnerPlatform {
				continue
			}
			if s.When != "" {
				on, _ := Lookup(values, s.When).(bool)
				if !on {
					continue
				}
			}
			if err := add(s.Name); err != nil {
				return nil, err
			}
		}
	}
	return accounts, nil
}

// PlatformAccounts returns the names of the kind's platform accounts.
func PlatformAccounts(c *cozyv1alpha1.ApplicationDefinitionCredentials) []string {
	if c == nil {
		return nil
	}
	var names []string
	for _, s := range c.Accounts {
		if s.Kind == cozyv1alpha1.CredentialAccountsStatic && s.Owner == cozyv1alpha1.CredentialAccountOwnerPlatform {
			names = append(names, s.Name)
		}
	}
	return names
}

// ValuesMapPaths returns the paths of the kind's ValuesMap sources.
func ValuesMapPaths(c *cozyv1alpha1.ApplicationDefinitionCredentials) []string {
	if c == nil {
		return nil
	}
	var paths []string
	for _, s := range c.Accounts {
		if s.Kind == cozyv1alpha1.CredentialAccountsValuesMap {
			paths = append(paths, s.Path)
		}
	}
	return paths
}

// Lookup walks a dotted path through nested maps and returns what it finds,
// or nil where the path leaves the maps.
func Lookup(values map[string]any, path string) any {
	var cur any = values
	for _, key := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = m[key]
		if !ok {
			return nil
		}
	}
	return cur
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Declaration order inside a map is the sorted order: it is what a chart's
	// range sees and what makes the seeding deterministic.
	sortStrings(keys)
	return keys
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
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

// Labels are the labels every account Secret of an application carries: the
// account marker, the application identity the view lists by, and the labels
// the kind's block names for its operator.
func Labels(c *cozyv1alpha1.ApplicationDefinitionCredentials, group, kind, name string) map[string]string {
	labels := map[string]string{
		AccountLabel:                       "true",
		appsv1alpha1.ApplicationGroupLabel: group,
		appsv1alpha1.ApplicationKindLabel:  kind,
		appsv1alpha1.ApplicationNameLabel:  name,
	}
	if c != nil {
		for k, v := range c.SecretLabels {
			labels[k] = v
		}
	}
	return labels
}

// IsAccountSecret reports whether a Secret is one the API writes for an
// account.
func IsAccountSecret(sec *corev1.Secret) bool {
	return sec != nil && sec.Labels[AccountLabel] == "true"
}
