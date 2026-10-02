package lineagecontrollerwebhook

import (
	"context"
	"fmt"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/shared/appdefowner"
)

// ApplicationDefinitionKindValidatorPath is where the kind-uniqueness check is
// served; the chart's ValidatingWebhookConfiguration points at it.
const ApplicationDefinitionKindValidatorPath = "/validate-applicationdefinition"

// ApplicationDefinitionKindValidator refuses an ApplicationDefinition that
// declares an application kind another definition already declares, so a
// second definition for a kind fails loudly when it is applied instead of
// sitting next to the first one. It is the feedback layer, not the guarantee:
// the webhook is registered with failurePolicy Ignore, so an outage of the
// webhook never blocks a platform install or upgrade, and appdefowner decides
// which definition manages a kind whatever reaches the cluster.
type ApplicationDefinitionKindValidator struct {
	reader  client.Reader
	decoder admission.Decoder
}

// NewApplicationDefinitionKindValidator builds the validator over reader,
// normally the manager's cached client.
func NewApplicationDefinitionKindValidator(reader client.Reader, scheme *runtime.Scheme) *ApplicationDefinitionKindValidator {
	return &ApplicationDefinitionKindValidator{reader: reader, decoder: admission.NewDecoder(scheme)}
}

// Handle implements admission.Handler.
func (v *ApplicationDefinitionKindValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	def := &cozyv1alpha1.ApplicationDefinition{}
	if err := v.decoder.DecodeRaw(req.Object, def); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	kind := def.Spec.Application.Kind
	if req.Operation == admissionv1.Update {
		old := &cozyv1alpha1.ApplicationDefinition{}
		if err := v.decoder.DecodeRaw(req.OldObject, old); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		// A definition that keeps its kind is not claiming anything new. This
		// also lets a duplicate that predates the check keep upgrading.
		if old.Spec.Application.Kind == kind {
			return admission.Allowed("")
		}
	}
	defs := &cozyv1alpha1.ApplicationDefinitionList{}
	if err := v.reader.List(ctx, defs); err != nil {
		// An error answer is a verdict that refuses the request, and
		// failurePolicy Ignore only covers a webhook that does not answer. The
		// check is feedback, not the guarantee, so let the request through and
		// say so.
		return admission.Allowed("").WithWarnings(fmt.Sprintf("application kind uniqueness was not checked: list ApplicationDefinitions: %v", err))
	}
	if kind == "" || !claimedByOther(defs.Items, def) {
		return admission.Allowed("")
	}
	return admission.Denied(fmt.Sprintf("application kind %s is already owned by ApplicationDefinition %s; delete that definition first to hand the kind over, or declare a different kind", kind, appdefowner.Owners(defs.Items)[kind]))
}

// claimedByOther reports whether a definition other than def declares def's
// kind. A definition from the same Helm release does not count: a release that
// renames its definition object creates the new one before it deletes the old
// one, and refusing that would fail the upgrade carrying the rename.
func claimedByOther(defs []cozyv1alpha1.ApplicationDefinition, def *cozyv1alpha1.ApplicationDefinition) bool {
	for i := range defs {
		other := &defs[i]
		if other.Name == def.Name || other.Spec.Application.Kind != def.Spec.Application.Kind {
			continue
		}
		if sameHelmRelease(other, def) {
			continue
		}
		return true
	}
	return false
}

func sameHelmRelease(a, b *cozyv1alpha1.ApplicationDefinition) bool {
	const name, namespace = "meta.helm.sh/release-name", "meta.helm.sh/release-namespace"
	an := a.GetAnnotations()[name]
	return an != "" && an == b.GetAnnotations()[name] &&
		a.GetAnnotations()[namespace] == b.GetAnnotations()[namespace]
}
