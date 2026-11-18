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

// ownerCacheTTL is how long an owner object is reused across admissions. The
// lineage labels depend only on owner kinds and names, but the scheduling class
// is read from the application's spec, so a change to it reaches new Pods up to
// this long after it is made.
const ownerCacheTTL = 60 * time.Second

// +kubebuilder:webhook:path=/mutate-lineage,mutating=true,failurePolicy=Fail,sideEffects=None,groups="",resources=pods,secrets,services,persistentvolumeclaims,verbs=create;update,versions=v1,name=mlineage.cozystack.io,admissionReviewVersions={v1}
type LineageControllerWebhook struct {
	client.Client
	Scheme     *runtime.Scheme
	decoder    admission.Decoder
	dynClient  dynamic.Interface
	mapper     meta.RESTMapper
	config     atomic.Value
	initOnce   sync.Once
	ownerCache *lineage.ObjectCache
}
