package lineagecontrollerwebhook

import (
	"sync"
	"sync/atomic"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:path=/mutate-lineage,mutating=true,failurePolicy=Fail,sideEffects=None,groups="",resources=pods,secrets,services,persistentvolumeclaims,verbs=create;update,versions=v1,name=mlineage.cozystack.io,admissionReviewVersions={v1}
type LineageControllerWebhook struct {
	client.Client
	Scheme    *runtime.Scheme
	decoder   admission.Decoder
	dynClient dynamic.Interface
	mapper    meta.RESTMapper
	config    atomic.Value
	initOnce  sync.Once
}
