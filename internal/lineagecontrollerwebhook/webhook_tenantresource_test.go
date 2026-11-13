package lineagecontrollerwebhook

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// The tenantresource verdict is the definition's, but an object may narrow it
// on CREATE and never widen it: a rendered "false" survives a selector that
// matches the name, a rendered "true" does not survive one that does not. A
// re-admission UPDATE gets the definition's verdict whatever the label says.
func TestHandle_TenantResourceLabelNarrowsOnly(t *testing.T) {
	tenantResourcePath := "/metadata/labels/" + strings.ReplaceAll(corev1alpha1.TenantResourceLabelKey, "/", "~1")

	tests := []struct {
		name     string
		op       admissionv1.Operation
		secret   string
		rendered string
		// want is the value the label must hold once the patch is applied;
		// "" means the response must not touch the label at all.
		want string
	}{
		{
			name:     "a rendered false is kept where the definition selects the name",
			op:       admissionv1.Create,
			secret:   "harbor-demo-credentials",
			rendered: "false",
			want:     "",
		},
		{
			name:     "a rendered true is overwritten where the definition does not select the name",
			op:       admissionv1.Create,
			secret:   "harbor-demo-other",
			rendered: "true",
			want:     "false",
		},
		{
			name:     "no rendered label gets the definition's verdict",
			op:       admissionv1.Create,
			secret:   "harbor-demo-credentials",
			rendered: "",
			want:     "true",
		},
		{
			// The cacert controller's re-admission after a selector change:
			// managed-by-cozystack dropped, the old verdict still on the object.
			name:     "a re-admission grants where the definition now selects the name",
			op:       admissionv1.Update,
			secret:   "harbor-demo-credentials",
			rendered: "false",
			want:     "true",
		},
		{
			name:     "a re-admission revokes where the definition no longer selects the name",
			op:       admissionv1.Update,
			secret:   "harbor-demo-other",
			rendered: "true",
			want:     "false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newHandleTestWebhook(t)
			w.config.Store(&runtimeConfig{appCRDMap: map[appRef]*cozyv1alpha1.ApplicationDefinition{
				{"apps.cozystack.io", "Harbor"}: {Spec: cozyv1alpha1.ApplicationDefinitionSpec{
					Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: "Harbor"},
					Secrets: cozyv1alpha1.ApplicationDefinitionResources{
						Include: []*cozyv1alpha1.ApplicationDefinitionResourceSelector{
							{ResourceNames: []string{"{{ .kind }}-{{ .name }}-credentials"}},
						},
					},
				}},
			}})

			labels := map[string]string{"helm.toolkit.fluxcd.io/name": "harbor-demo"}
			if tt.rendered != "" {
				labels[corev1alpha1.TenantResourceLabelKey] = tt.rendered
			}
			obj := &unstructured.Unstructured{}
			obj.SetAPIVersion("v1")
			obj.SetKind("Secret")
			obj.SetNamespace("tenant-demo")
			obj.SetName(tt.secret)
			obj.SetLabels(labels)
			raw, err := json.Marshal(obj)
			if err != nil {
				t.Fatalf("marshal object: %v", err)
			}

			req := admissionv1.AdmissionRequest{
				Kind:      metav1.GroupVersionKind{Version: "v1", Kind: "Secret"},
				Namespace: "tenant-demo",
				Name:      tt.secret,
				Operation: tt.op,
				Object:    runtime.RawExtension{Raw: raw},
			}
			if tt.op == admissionv1.Update {
				old := obj.DeepCopy()
				oldLabels := old.GetLabels()
				oldLabels[ManagedObjectKey] = "true"
				old.SetLabels(oldLabels)
				oldRaw, err := json.Marshal(old)
				if err != nil {
					t.Fatalf("marshal old object: %v", err)
				}
				req.OldObject = runtime.RawExtension{Raw: oldRaw}
			}
			resp := w.Handle(context.Background(), admission.Request{AdmissionRequest: req})
			if !resp.Allowed {
				t.Fatalf("expected allowed response, got %+v", resp.Result)
			}

			var got string
			var touched bool
			for _, p := range resp.Patches {
				if p.Path != tenantResourcePath {
					continue
				}
				touched = true
				v, ok := p.Value.(string)
				if !ok {
					t.Fatalf("patch %s carries %T, want string", p.Path, p.Value)
				}
				got = v
			}
			switch {
			case tt.want == "" && touched:
				t.Errorf("expected the label untouched, got patch value %q", got)
			case tt.want != "" && !touched:
				t.Errorf("expected a patch setting the label to %q, got none", tt.want)
			case tt.want != "" && got != tt.want:
				t.Errorf("label patched to %q, want %q", got, tt.want)
			}
		})
	}
}
