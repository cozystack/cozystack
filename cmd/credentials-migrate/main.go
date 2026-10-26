// SPDX-License-Identifier: Apache-2.0

// Command credentials-migrate converts the accounts of an application kind
// to platform-minted credentials: "seed" from the pre-upgrade hook, "finish"
// from a Job once the charts have switched. Both read the kind's credentials
// block from flags, because the hook runs while the old ApplicationDefinitions
// are still live. See design-proposals/user-secrets-api in cozystack/community.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
	"github.com/cozystack/cozystack/pkg/credentials/format"
	"github.com/cozystack/cozystack/pkg/credentials/store"
)

const (
	reasonSkipped = "CredentialsMigrationSkipped"
	defaultActor  = "system:serviceaccount:cozy-system:cozystack-migration-hook"
)

// options is the kind's credentials block and the migration's identity, as
// the hook and the Job pass them.
type options struct {
	migration      int
	kind           string
	formatName     string
	accountsPath   string
	labels         labelFlags
	sourceSuffix   string
	platformKeys   stringFlags
	stateNamespace string
	actor          string
	timeout        time.Duration
}

type labelFlags map[string]string

func (l labelFlags) String() string { return fmt.Sprint(map[string]string(l)) }
func (l labelFlags) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return fmt.Errorf("label %q is not key=value", v)
	}
	l[k] = val
	return nil
}

type stringFlags []string

func (s *stringFlags) String() string     { return strings.Join(*s, ",") }
func (s *stringFlags) Set(v string) error { *s = append(*s, v); return nil }

func (o *options) block() *cozyv1alpha1.ApplicationDefinitionCredentials {
	return &cozyv1alpha1.ApplicationDefinitionCredentials{
		Format:       cozyv1alpha1.CredentialFormat{Name: o.formatName},
		SecretLabels: o.labels,
		Accounts:     []cozyv1alpha1.CredentialAccountSource{{Kind: cozyv1alpha1.CredentialAccountsValuesMap, Path: o.accountsPath}},
	}
}

func (o *options) stateName() string {
	return fmt.Sprintf("credentials-migration-%d", o.migration)
}

// state is what the migration keeps between the hook and the Job, in a
// ConfigMap in cozy-system named for the migration.
type state struct {
	// Releases maps namespace/name to the record of the release.
	Releases map[string]*releaseState `json:"releases"`
	// Skipped maps namespace/name to the reason and time it was left out on
	// the last run. A release leaves the map once a later run finishes it.
	Skipped map[string]string `json:"skipped,omitempty"`
}

type releaseState struct {
	// ChartVersion the release rendered when the hook recorded it; the Job
	// acts once the release is Ready on another one.
	ChartVersion string `json:"chartVersion"`
	Done         bool   `json:"done,omitempty"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "credentials-migrate:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: credentials-migrate seed|finish [flags]")
	}
	cmd := args[0]
	o := &options{labels: labelFlags{}}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.IntVar(&o.migration, "migration", 0, "number of the platform migration that converts the kind")
	fs.StringVar(&o.kind, "kind", "", "application kind, as the HelmRelease label names it")
	fs.StringVar(&o.formatName, "format", "", "password format of the kind's credentials block")
	fs.StringVar(&o.accountsPath, "accounts-path", "users", "values map whose keys are the tenant accounts")
	fs.Var(o.labels, "label", "label every account Secret carries, key=value; repeatable")
	fs.StringVar(&o.sourceSuffix, "source-suffix", "-credentials", "suffix of the chart-generated Secret holding the plaintext passwords, after the release name")
	fs.Var(&o.platformKeys, "platform-key", "key of the source Secret that belongs to a platform account and stays; repeatable")
	fs.StringVar(&o.stateNamespace, "state-namespace", "cozy-system", "namespace of the state ConfigMap")
	fs.StringVar(&o.actor, "actor", envOr("CREDENTIALS_MIGRATE_ACTOR", defaultActor), "who the record names for the migrated password")
	fs.DurationVar(&o.timeout, "timeout", 10*time.Minute, "finish: how long to wait per release for the switched chart to be Ready")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if o.migration == 0 || o.kind == "" || !format.Known(o.formatName) {
		return errors.New("--migration, --kind and a built-in --format are required")
	}
	if err := account.Validate(o.block()); err != nil {
		return fmt.Errorf("credentials block: %w", err)
	}

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return err
	}
	if err := helmv2.AddToScheme(scheme); err != nil {
		return err
	}
	c, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("build client: %w", err)
	}
	m := &migrator{c: c, o: o, now: time.Now}
	ctx := context.Background()
	switch cmd {
	case "seed":
		return m.seed(ctx)
	case "finish":
		return m.finish(ctx)
	}
	return fmt.Errorf("unknown command %q: want seed or finish", cmd)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type migrator struct {
	c   client.Client
	o   *options
	now func() time.Time
}

// seed records every release of the kind and writes the account Secrets it
// still lacks from the chart-generated passwords. A release whose accounts
// cannot be resolved is left out with an Event; one whose source Secret is
// gone is recorded, since the Job still has to switch it, and its accounts
// get their Secrets from the first mint.
func (m *migrator) seed(ctx context.Context) error {
	st, err := m.loadState(ctx)
	if err != nil {
		return err
	}
	releases, err := m.releases(ctx)
	if err != nil {
		return err
	}
	for i := range releases {
		hr := &releases[i]
		key := hr.Namespace + "/" + hr.Name
		rec, known := st.Releases[key]
		if !known {
			rec = &releaseState{ChartVersion: chartVersion(hr)}
			st.Releases[key] = rec
		}
		if err := m.seedRelease(ctx, hr); err != nil {
			m.skip(ctx, st, hr, err)
			continue
		}
		delete(st.Skipped, key)
		fmt.Printf("%s: recorded chart version %q\n", key, rec.ChartVersion)
	}
	return m.saveState(ctx, st)
}

// finish converts every recorded release once its chart has switched, and
// exits non-zero while any is left out, so one place shows what remains.
func (m *migrator) finish(ctx context.Context) error {
	st, err := m.loadState(ctx)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(st.Releases))
	for k := range st.Releases {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		rec := st.Releases[key]
		if rec.Done {
			continue
		}
		ns, name, _ := strings.Cut(key, "/")
		hr := &helmv2.HelmRelease{}
		if err := m.c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, hr); err != nil {
			if apierrors.IsNotFound(err) {
				// Deleted since the hook: nothing left to convert.
				rec.Done = true
				delete(st.Skipped, key)
				continue
			}
			return err
		}
		if err := m.finishRelease(ctx, hr, rec); err != nil {
			m.skip(ctx, st, hr, err)
			if err := m.saveState(ctx, st); err != nil {
				return err
			}
			continue
		}
		rec.Done = true
		delete(st.Skipped, key)
		fmt.Printf("%s: converted\n", key)
		if err := m.saveState(ctx, st); err != nil {
			return err
		}
	}
	if err := m.saveState(ctx, st); err != nil {
		return err
	}
	if len(st.Skipped) > 0 {
		var left []string
		for k, why := range st.Skipped {
			left = append(left, k+": "+why)
		}
		sort.Strings(left)
		return fmt.Errorf("%d release(s) left out:\n  %s", len(left), strings.Join(left, "\n  "))
	}
	return nil
}

func (m *migrator) finishRelease(ctx context.Context, hr *helmv2.HelmRelease, rec *releaseState) error {
	// Seed again first: an account whose Secret is still missing, as after an
	// interrupted hook, gets it here while the plaintext still exists.
	if err := m.seedRelease(ctx, hr); err != nil {
		return err
	}
	deadline := m.now().Add(m.o.timeout)
	for {
		if apimeta.IsStatusConditionTrue(hr.Status.Conditions, "Ready") && chartVersion(hr) != "" && chartVersion(hr) != rec.ChartVersion {
			break
		}
		if m.now().After(deadline) {
			return fmt.Errorf("not Ready on a chart version other than %q after %s (current %q)", rec.ChartVersion, m.o.timeout, chartVersion(hr))
		}
		time.Sleep(10 * time.Second)
		if err := m.c.Get(ctx, client.ObjectKeyFromObject(hr), hr); err != nil {
			return err
		}
	}
	if err := m.stripPasswords(ctx, hr); err != nil {
		return fmt.Errorf("strip passwords from values: %w", err)
	}
	return m.retireSource(ctx, hr)
}

// seedRelease writes the account Secrets the release lacks. It fails only
// where the accounts themselves cannot be resolved; a missing plaintext leaves
// that account to the first mint.
func (m *migrator) seedRelease(ctx context.Context, hr *helmv2.HelmRelease) error {
	block := m.o.block()
	accounts, err := store.ResolveAccounts(block, nil, hr.Spec.Values)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		return nil
	}
	app := store.App{
		Namespace: hr.Namespace, Release: hr.Name,
		Group: hr.Labels[appsv1alpha1.ApplicationGroupLabel],
		Kind:  hr.Labels[appsv1alpha1.ApplicationKindLabel],
		Name:  hr.Labels[appsv1alpha1.ApplicationNameLabel],
		Block: block,
	}
	source := &corev1.Secret{}
	if err := m.c.Get(ctx, types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name + m.o.sourceSuffix}, source); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		fmt.Printf("%s/%s: %s%s is gone; its accounts get a Secret at their first mint\n", hr.Namespace, hr.Name, hr.Name, m.o.sourceSuffix)
		return nil
	}
	owner := store.OwnerReference(hr)
	now := metav1.NewTime(m.now())
	for _, acc := range accounts {
		name := account.SecretName(hr.Name, acc.Mapped)
		cur := &corev1.Secret{}
		err := m.c.Get(ctx, types.NamespacedName{Namespace: hr.Namespace, Name: name}, cur)
		switch {
		case err == nil && store.Theirs(cur, acc, &owner):
			continue
		case err == nil:
			if err := m.c.Delete(ctx, cur, client.Preconditions{UID: &cur.UID}); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("replace stale %s: %w", name, err)
			}
		case !apierrors.IsNotFound(err):
			return err
		}
		plaintext, ok := source.Data[acc.Username]
		if !ok {
			fmt.Printf("%s/%s: no password for %q in %s%s; the account gets a Secret at its first mint\n", hr.Namespace, hr.Name, acc.Username, hr.Name, m.o.sourceSuffix)
			continue
		}
		stored, err := format.Encode(m.o.formatName, string(plaintext))
		if err != nil {
			return err
		}
		sec, err := store.SeedSecret(app, acc, &owner, now, m.o.actor)
		if err != nil {
			return err
		}
		sec.Data[account.PasswordKey] = []byte(stored)
		account.WriteRecord(sec, account.Record{}.Apply(corev1alpha1.CredentialOperationMigrate, now, m.o.actor))
		if err := m.c.Create(ctx, sec); err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
		fmt.Printf("%s/%s: migrated account %q into %s\n", hr.Namespace, hr.Name, acc.Username, name)
	}
	return nil
}

// stripPasswords removes a password left under any key of the accounts map in
// the release's values.
func (m *migrator) stripPasswords(ctx context.Context, hr *helmv2.HelmRelease) error {
	if hr.Spec.Values == nil || len(hr.Spec.Values.Raw) == 0 {
		return nil
	}
	var values map[string]any
	if err := json.Unmarshal(hr.Spec.Values.Raw, &values); err != nil {
		return err
	}
	users, _ := account.Lookup(values, m.o.accountsPath).(map[string]any)
	changed := false
	for name, raw := range users {
		u, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if _, has := u["password"]; has {
			delete(u, "password")
			changed = true
			fmt.Printf("%s/%s: removed %s.%s.password from the values\n", hr.Namespace, hr.Name, m.o.accountsPath, name)
		}
	}
	if !changed {
		return nil
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return err
	}
	patch := client.MergeFrom(hr.DeepCopy())
	hr.Spec.Values.Raw = raw
	return m.c.Patch(ctx, hr, patch)
}

// retireSource removes the tenant keys from the chart-generated Secret, which
// stay in its data otherwise, and the Secret itself when no platform key
// remains.
func (m *migrator) retireSource(ctx context.Context, hr *helmv2.HelmRelease) error {
	source := &corev1.Secret{}
	if err := m.c.Get(ctx, types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name + m.o.sourceSuffix}, source); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	keep := map[string]bool{}
	for _, k := range m.o.platformKeys {
		keep[k] = true
	}
	remaining := map[string][]byte{}
	for k, v := range source.Data {
		if keep[k] {
			remaining[k] = v
		}
	}
	if len(remaining) == 0 {
		if err := m.c.Delete(ctx, source, client.Preconditions{UID: &source.UID}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		fmt.Printf("%s/%s: deleted %s\n", hr.Namespace, hr.Name, source.Name)
		return nil
	}
	if len(remaining) == len(source.Data) {
		return nil
	}
	patch := client.MergeFrom(source.DeepCopy())
	source.Data = remaining
	if err := m.c.Patch(ctx, source, patch); err != nil {
		return err
	}
	fmt.Printf("%s/%s: removed the tenant keys from %s\n", hr.Namespace, hr.Name, source.Name)
	return nil
}

func (m *migrator) releases(ctx context.Context) ([]helmv2.HelmRelease, error) {
	list := &helmv2.HelmReleaseList{}
	if err := m.c.List(ctx, list, client.MatchingLabels{
		appsv1alpha1.ApplicationGroupLabel: appsv1alpha1.GroupName,
		appsv1alpha1.ApplicationKindLabel:  m.o.kind,
	}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// skip leaves a release out: logged, recorded in the state, and announced
// with an Event on the HelmRelease, so the cause is visible where the
// release is.
func (m *migrator) skip(ctx context.Context, st *state, hr *helmv2.HelmRelease, cause error) {
	key := hr.Namespace + "/" + hr.Name
	why := fmt.Sprintf("%s at %s", cause.Error(), m.now().UTC().Format(time.RFC3339))
	st.Skipped[key] = why
	fmt.Fprintf(os.Stderr, "%s: left out: %v\n", key, cause)
	ev := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{GenerateName: hr.Name + ".", Namespace: hr.Namespace},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: helmv2.GroupVersion.String(), Kind: helmv2.HelmReleaseKind,
			Name: hr.Name, Namespace: hr.Namespace, UID: hr.UID,
		},
		Reason:         reasonSkipped,
		Message:        fmt.Sprintf("credentials migration %d left this release out: %v", m.o.migration, cause),
		Type:           corev1.EventTypeWarning,
		Source:         corev1.EventSource{Component: "credentials-migrate"},
		FirstTimestamp: metav1.NewTime(m.now()),
		LastTimestamp:  metav1.NewTime(m.now()),
		Count:          1,
	}
	if err := m.c.Create(ctx, ev); err != nil {
		fmt.Fprintf(os.Stderr, "%s: failed to record event: %v\n", key, err)
	}
}

func (m *migrator) loadState(ctx context.Context) (*state, error) {
	st := &state{Releases: map[string]*releaseState{}, Skipped: map[string]string{}}
	cm := &corev1.ConfigMap{}
	err := m.c.Get(ctx, types.NamespacedName{Namespace: m.o.stateNamespace, Name: m.o.stateName()}, cm)
	if apierrors.IsNotFound(err) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if raw := cm.Data["state"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), st); err != nil {
			return nil, fmt.Errorf("decode state ConfigMap %s: %w", cm.Name, err)
		}
	}
	if st.Releases == nil {
		st.Releases = map[string]*releaseState{}
	}
	if st.Skipped == nil {
		st.Skipped = map[string]string{}
	}
	return st, nil
}

func (m *migrator) saveState(ctx context.Context, st *state) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: m.o.stateNamespace, Name: m.o.stateName()}}
	_, err = ctrl.CreateOrUpdate(ctx, m.c, cm, func() error {
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data["state"] = string(raw)
		return nil
	})
	return err
}

func chartVersion(hr *helmv2.HelmRelease) string {
	if s := hr.Status.History.Latest(); s != nil {
		return s.ChartVersion
	}
	return ""
}
