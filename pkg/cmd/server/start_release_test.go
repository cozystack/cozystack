/*
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package server

import (
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/kustomize"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

// TestBuildResourceFromCRD_AnnotationAndReleaseWiring pins the assignments that
// join an ApplicationDefinition's annotations / typed release fields to the
// ReleaseConfig the REST layer reads. This is the step Complete() runs per CRD;
// nothing exercised it before, so a mutation dropping any of these lines
// (release.HelmServerSideApply = serverSideApply -> _ = serverSideApply) shipped
// a silent nil that reverts the field's effect on every emitted HelmRelease with
// a green suite. Highest blast radius here is HelmServerSideApply: nil restores
// helm-controller's server-side default, which force-owns the rendered
// spec.instances seed and reverts KEDA's live /scale count.
func TestBuildResourceFromCRD_AnnotationAndReleaseWiring(t *testing.T) {
	crd := v1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "postgres",
			Annotations: map[string]string{
				config.HelmServerSideApplyAnnotation:    "false",
				config.HelmInstallDisableWaitAnnotation: "true",
			},
		},
		Spec: v1alpha1.ApplicationDefinitionSpec{
			Application: v1alpha1.ApplicationDefinitionApplication{
				Kind:     "Postgres",
				Plural:   "postgreses",
				Singular: "postgres",
			},
			Release: v1alpha1.ApplicationDefinitionRelease{
				Prefix:   "postgres-",
				ChartRef: &helmv2.CrossNamespaceSourceReference{Kind: "ExternalArtifact", Name: "x", Namespace: "cozy-system"},
				HealthCheckExprs: []kustomize.CustomHealthCheck{
					{
						APIVersion:             "keda.sh/v1alpha1",
						Kind:                   "ScaledObject",
						HealthCheckExpressions: kustomize.HealthCheckExpressions{Current: "true"},
					},
				},
			},
		},
	}

	res, err := buildResourceFromCRD(crd, helmReleaseFlagValues{})
	if err != nil {
		t.Fatalf("buildResourceFromCRD returned error: %v", err)
	}

	// helm-server-side-apply: "false" must thread through as a non-nil false so
	// the REST layer forces client-side apply for the kind. A dropped assignment
	// leaves this nil (helm-controller default = server-side).
	if res.Release.HelmServerSideApply == nil {
		t.Fatalf("HelmServerSideApply is nil; the annotation-to-config assignment was dropped")
	}
	if *res.Release.HelmServerSideApply != false {
		t.Errorf("HelmServerSideApply = %v, want false", *res.Release.HelmServerSideApply)
	}

	// helm-install-disable-wait: "true" (sibling wiring on the same loop).
	if !res.Release.HelmInstallDisableWait {
		t.Errorf("HelmInstallDisableWait = false, want true")
	}

	// Typed healthCheckExprs must carry through verbatim: this is what neutralises
	// a TriggerError ScaledObject's readiness so it cannot wedge the Postgres release.
	if got := len(res.Release.HealthCheckExprs); got != 1 {
		t.Fatalf("HealthCheckExprs length = %d, want 1", got)
	}
	if hc := res.Release.HealthCheckExprs[0]; hc.Kind != "ScaledObject" || hc.APIVersion != "keda.sh/v1alpha1" || hc.Current != "true" {
		t.Errorf("HealthCheckExprs[0] = %+v, want ScaledObject/keda.sh/v1alpha1 current=true", hc)
	}
}

// TestBuildResourceFromCRD_AnnotationAbsentLeavesDefault confirms an
// ApplicationDefinition without the apply-strategy annotation leaves
// HelmServerSideApply nil (helm-controller default), so the client-side flip is
// opt-in per kind and does not silently move every other Application to
// client-side apply.
func TestBuildResourceFromCRD_AnnotationAbsentLeavesDefault(t *testing.T) {
	crd := v1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "redis"},
		Spec: v1alpha1.ApplicationDefinitionSpec{
			Application: v1alpha1.ApplicationDefinitionApplication{Kind: "Redis"},
			Release:     v1alpha1.ApplicationDefinitionRelease{Prefix: "redis-", ChartRef: &helmv2.CrossNamespaceSourceReference{Kind: "ExternalArtifact", Name: "x", Namespace: "cozy-system"}},
		},
	}

	res, err := buildResourceFromCRD(crd, helmReleaseFlagValues{})
	if err != nil {
		t.Fatalf("buildResourceFromCRD returned error: %v", err)
	}
	if res.Release.HelmServerSideApply != nil {
		t.Errorf("HelmServerSideApply = %v, want nil (helm-controller default)", *res.Release.HelmServerSideApply)
	}
	if len(res.Release.HealthCheckExprs) != 0 {
		t.Errorf("HealthCheckExprs = %v, want empty", res.Release.HealthCheckExprs)
	}
}
