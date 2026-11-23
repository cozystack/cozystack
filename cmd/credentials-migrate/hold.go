// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// HeldByAnnotation on a HelmRelease names the migration that suspended it
// until the components the converted chart relies on have rolled out, so
// only that suspension is lifted. It is not SuspendedByAnnotation: lifting a
// hold must not pass for the forced upgrade a rotation still owes.
const HeldByAnnotation = "credentials.cozystack.io/held-by-migration"

// Why the hold exists: every application chart is copied out of the one
// cozystack-packages source, so all of them switch to the new version the
// moment the platform chart moves that source, while the components they
// depend on roll out on their own schedule. A Package dependsOn does not
// order this: tenant releases carry no dependsOn, and Flux's dependsOn only
// asks that the dependency be Ready, which a release still on its old
// revision is. A converted chart that the old cozystack-controller or an old
// operator acts on can lock clients out or roll the database the old way, so
// the hook suspends the kind's releases and the finish Job resumes them once
// those components run their new revision. Neither the old controller nor
// the old cozystack-api touches spec.suspend.

// parseObjectKey reads a namespace/name pair.
func parseObjectKey(s string) (types.NamespacedName, error) {
	ns, name, ok := strings.Cut(s, "/")
	if !ok || ns == "" || name == "" || strings.Contains(name, "/") {
		return types.NamespacedName{}, fmt.Errorf("%q is not namespace/name", s)
	}
	return types.NamespacedName{Namespace: ns, Name: name}, nil
}

// templateImages is what a Deployment's pods run: the images of its pod
// template, in order. An upgrade that crosses a conversion always changes the
// image of a component the converted chart needs, so a change of this value
// tells the new revision from the old one without knowing either tag.
func templateImages(d *appsv1.Deployment) string {
	var imgs []string
	for _, c := range d.Spec.Template.Spec.InitContainers {
		imgs = append(imgs, c.Image)
	}
	for _, c := range d.Spec.Template.Spec.Containers {
		imgs = append(imgs, c.Image)
	}
	return strings.Join(imgs, ",")
}

// rolledOut reports whether every pod of the Deployment runs its current
// template and none of the previous one is left.
func rolledOut(d *appsv1.Deployment) bool {
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	s := d.Status
	return s.ObservedGeneration >= d.Generation &&
		s.UpdatedReplicas == want && s.Replicas == want && s.AvailableReplicas == want
}

// recordRollouts keeps, for each Deployment the hold waits for, the images it
// runs before the upgrade; an empty value stands for a Deployment that does
// not exist yet. A rerun of the hook keeps the first record, which is the one
// taken before anything switched.
func (m *migrator) recordRollouts(ctx context.Context, st *state) error {
	for _, key := range m.o.holdRollouts {
		if _, ok := st.Rollouts[key]; ok {
			continue
		}
		nn, err := parseObjectKey(key)
		if err != nil {
			return err
		}
		d := &appsv1.Deployment{}
		images := ""
		if err := m.c.Get(ctx, nn, d); err != nil {
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("read Deployment %s: %w", key, err)
			}
		} else {
			images = templateImages(d)
		}
		if st.Rollouts == nil {
			st.Rollouts = map[string]string{}
		}
		st.Rollouts[key] = images
		fmt.Printf("recorded Deployment %s on %q\n", key, images)
	}
	return nil
}

// hold suspends a release until the recorded Deployments have rolled out. A
// release already suspended is left as it is: either this hook held it on an
// earlier run, or someone else suspended it, and that is not ours to lift.
func (m *migrator) hold(ctx context.Context, hr *helmv2.HelmRelease) error {
	if len(m.o.holdRollouts) == 0 || hr.Spec.Suspend {
		return nil
	}
	patch := client.MergeFrom(hr.DeepCopy())
	hr.Spec.Suspend = true
	if hr.Annotations == nil {
		hr.Annotations = map[string]string{}
	}
	hr.Annotations[HeldByAnnotation] = strconv.Itoa(m.o.migration)
	if err := m.c.Patch(ctx, hr, patch); err != nil {
		return fmt.Errorf("hold until the new components run: %w", err)
	}
	fmt.Printf("%s/%s: suspended until %s roll out\n", hr.Namespace, hr.Name, strings.Join(m.o.holdRollouts, ", "))
	return nil
}

// releaseHolds waits for every recorded Deployment to run a new revision,
// then resumes the releases this migration held. Before resuming one it seeds
// it again, which puts back the valuesFrom entries the old controller may
// have stripped, so the first render of the converted chart carries them. If
// a Deployment does not roll out in time the releases stay suspended and are
// reported left out: their workloads keep running on the old chart, which is
// safer than a converted chart the old components would act on.
func (m *migrator) releaseHolds(ctx context.Context, st *state) error {
	if len(st.Rollouts) == 0 {
		return nil
	}
	mark := strconv.Itoa(m.o.migration)
	keys := make([]string, 0, len(st.Releases))
	for k, rec := range st.Releases {
		if !rec.Done {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var held []*helmv2.HelmRelease
	for _, key := range keys {
		ns, name, _ := strings.Cut(key, "/")
		hr := &helmv2.HelmRelease{}
		if err := m.c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, hr); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
		if hr.Spec.Suspend && hr.Annotations[HeldByAnnotation] == mark {
			held = append(held, hr)
		}
	}
	if len(held) == 0 {
		return nil
	}
	if err := m.waitRollouts(ctx, st); err != nil {
		for _, hr := range held {
			m.skip(ctx, st, hr, fmt.Errorf("still suspended: %w; resume it by running this Job again once they have, or by removing %s and setting spec.suspend to false", err, HeldByAnnotation))
		}
		return errors.Join(err, m.saveState(ctx, st))
	}
	for _, hr := range held {
		key := hr.Namespace + "/" + hr.Name
		if err := m.seedRelease(ctx, hr); err != nil && !errors.Is(err, errUnresolvable) {
			return fmt.Errorf("%s: seed before resuming: %w", key, err)
		}
		patch := client.MergeFrom(hr.DeepCopy())
		hr.Spec.Suspend = false
		delete(hr.Annotations, HeldByAnnotation)
		if err := m.c.Patch(ctx, hr, patch); err != nil {
			return fmt.Errorf("%s: resume: %w", key, err)
		}
		delete(st.Skipped, key)
		fmt.Printf("%s: resumed\n", key)
	}
	return m.saveState(ctx, st)
}

// waitRollouts waits until every recorded Deployment runs images other than
// the recorded ones on all of its pods.
func (m *migrator) waitRollouts(ctx context.Context, st *state) error {
	keys := make([]string, 0, len(st.Rollouts))
	for k := range st.Rollouts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	deadline := m.now().Add(m.o.holdTimeout)
	for {
		var pending []string
		for _, key := range keys {
			nn, err := parseObjectKey(key)
			if err != nil {
				return err
			}
			d := &appsv1.Deployment{}
			if err := m.c.Get(ctx, nn, d); err != nil {
				if !apierrors.IsNotFound(err) {
					return fmt.Errorf("read Deployment %s: %w", key, err)
				}
				pending = append(pending, key+" (absent)")
				continue
			}
			if templateImages(d) == st.Rollouts[key] {
				pending = append(pending, key+" (old revision)")
				continue
			}
			if !rolledOut(d) {
				pending = append(pending, key+" (rolling out)")
			}
		}
		if len(pending) == 0 {
			return nil
		}
		if m.now().After(deadline) {
			return fmt.Errorf("not rolled out to a new revision after %s: %s", m.o.holdTimeout, strings.Join(pending, ", "))
		}
		time.Sleep(10 * time.Second)
	}
}
