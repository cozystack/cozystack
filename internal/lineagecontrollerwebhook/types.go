package lineagecontrollerwebhook

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/cozystack/cozystack/pkg/lineage"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// ownerCacheTTL controls how long a successful or negative dynamic-client GET
// result is reused across admission requests. The labels we derive are based on
// stable identity (kind/name/owner refs), so even a generous TTL is safe; the
// dominant cost we are saving is the apiserver round-trip on every admission.
const ownerCacheTTL = 60 * time.Second

// +kubebuilder:webhook:path=/mutate-lineage,mutating=true,failurePolicy=Fail,sideEffects=None,groups="",resources=pods,secrets,services,persistentvolumeclaims,verbs=create;update,versions=v1,name=mlineage.cozystack.io,admissionReviewVersions={v1}
type LineageControllerWebhook struct {
	client.Client
	Scheme      *runtime.Scheme
	decoder     admission.Decoder
	dynClient   dynamic.Interface
	mapper      meta.RESTMapper
	config      atomic.Value
	initOnce    sync.Once
	ownerCache  *lineage.ObjectCache
}
