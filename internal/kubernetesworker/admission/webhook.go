package admission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"

	workers "github.com/cozystack/cozystack/api/workers/v1alpha1"
	"github.com/cozystack/cozystack/internal/kubernetesworker/config"
	"github.com/cozystack/cozystack/internal/kubernetesworker/controller"
	av1 "k8s.io/api/admission/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	ca "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// Pool fields the bootstrap config and machine templates read directly. Their
// rendered content names those objects, so changing one in the adopting update
// would rename them in the same write; Resolve does not see them.
var templateInputs = []string{
	"diskSize", "gpus", "instanceType", "kubelet", "logSerialConsole", "osImage",
	"podCpuLimit", "podCpuRequest", "proxmox", "resources", "roles", "storageClass",
	"cluster", "network", "routeTable", "labels", "taints",
}

type Mutator struct{ DefaultWorkerClass string }
type Validator struct{ Reader client.Reader }

func matches(req ca.Request) bool {
	resource := config.PoolResource()
	return req.Resource.Group == resource.Group && req.Resource.Version == resource.Version && req.Resource.Resource == resource.Resource && (req.Operation == av1.Create || req.Operation == av1.Update)
}

func decode(raw []byte) (map[string]any, map[string]any, error) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, nil, err
	}
	if obj == nil {
		return nil, nil, fmt.Errorf("application object is missing")
	}
	spec := map[string]any{}
	if v, ok := obj["spec"]; ok && v != nil {
		var valid bool
		spec, valid = v.(map[string]any)
		if !valid {
			return nil, nil, fmt.Errorf("spec must be an object")
		}
	}
	obj["spec"] = spec
	return obj, spec, nil
}

func oldSpec(req ca.Request) (map[string]any, error) {
	if req.Operation != av1.Update {
		return nil, nil
	}
	_, old, err := decode(req.OldObject.Raw)
	return old, err
}

func (m *Mutator) Handle(_ context.Context, req ca.Request) ca.Response {
	if !matches(req) {
		return ca.Allowed("")
	}
	obj, pool, err := decode(req.Object.Raw)
	if err != nil {
		return ca.Errored(http.StatusBadRequest, err)
	}
	old, err := oldSpec(req)
	if err != nil {
		return ca.Errored(http.StatusBadRequest, err)
	}
	name, err := config.ClassName(pool)
	if err != nil {
		return ca.Errored(http.StatusBadRequest, err)
	}
	if name != "" {
		return ca.Allowed("")
	}
	if req.Operation == av1.Create {
		name = m.DefaultWorkerClass
	} else {
		name, err = config.ClassName(old)
		if err != nil {
			return ca.Errored(http.StatusBadRequest, fmt.Errorf("stored pool: %w", err))
		}
	}
	if name == "" {
		return ca.Allowed("")
	}
	pool[config.ClassField] = name
	patched, err := json.Marshal(obj)
	if err != nil {
		return ca.Errored(http.StatusInternalServerError, err)
	}
	return ca.PatchResponseFromRaw(req.Object.Raw, patched)
}

const switchesFamily = "workerClassName cannot switch a running pool's bootstrap family; create a new pool"

func (v *Validator) class(ctx context.Context, name string) (*workers.WorkerClass, error) {
	if name == "" {
		return nil, nil
	}
	if config.Builtin(name) {
		return &workers.WorkerClass{Spec: workers.WorkerClassSpec{Bootstrap: workers.Bootstrap(name)}}, nil
	}
	wc := &workers.WorkerClass{}
	if err := v.Reader.Get(ctx, types.NamespacedName{Name: name}, wc); err != nil {
		return nil, err
	}
	return wc, nil
}

func (v *Validator) Handle(ctx context.Context, req ca.Request) ca.Response {
	if !matches(req) {
		return ca.Allowed("")
	}
	_, pool, err := decode(req.Object.Raw)
	if err != nil {
		return ca.Errored(http.StatusBadRequest, err)
	}
	old, err := oldSpec(req)
	if err != nil {
		return ca.Errored(http.StatusBadRequest, err)
	}
	name, err := config.ClassName(pool)
	if err != nil {
		return ca.Errored(http.StatusBadRequest, err)
	}
	oldName, err := config.ClassName(old)
	if err != nil {
		return ca.Errored(http.StatusBadRequest, fmt.Errorf("stored pool: %w", err))
	}
	if name == "" && (req.Operation == av1.Create || oldName != "") {
		return ca.Denied("workerClassName must be assigned on CREATE and preserved on classified UPDATE")
	}
	class, err := v.class(ctx, name)
	if apierrors.IsNotFound(err) {
		return ca.Denied(fmt.Sprintf("WorkerClass %q does not exist", name))
	}
	if err != nil {
		return ca.Errored(http.StatusInternalServerError, fmt.Errorf("reading WorkerClass %q: %w", name, err))
	}
	if class != nil && !class.DeletionTimestamp.IsZero() && (req.Operation == av1.Create || name != oldName) {
		return ca.Denied(fmt.Sprintf("WorkerClass %q is deleting; choose another class", name))
	}
	// The controller adds the finalizer to a hoster class only after the class
	// exists; a pool admitted before that could outlive a class deleted in
	// between.
	if class != nil && !config.Builtin(name) && !controllerutil.ContainsFinalizer(class, controller.Finalizer) && (req.Operation == av1.Create || name != oldName) {
		return ca.Denied(fmt.Sprintf("WorkerClass %q is not yet protected against deletion; retry in a few seconds", name))
	}
	var spec *workers.WorkerClassSpec
	if class != nil {
		spec = &class.Spec
	}
	resolved, err := config.Resolve(spec, pool)
	var own config.PoolError
	if errors.As(err, &own) {
		return ca.Denied(fmt.Sprintf("%v; it overrides %s", err, subject(name)))
	}
	if err != nil {
		return ca.Denied(fmt.Sprintf("%s: %v", subject(name), err))
	}
	if req.Operation != av1.Update || oldName == name {
		return ca.Allowed("")
	}
	if oldName != "" {
		// Only the family matters when leaving a class: the pool's values were
		// admitted under it, and a class it no longer satisfies must not hold it.
		previous, err := v.class(ctx, oldName)
		if apierrors.IsNotFound(err) {
			return ca.Denied(fmt.Sprintf("previous WorkerClass %q no longer exists, so its bootstrap family cannot be checked; recreate it or create a new pool", oldName))
		}
		if err != nil {
			return ca.Errored(http.StatusInternalServerError, fmt.Errorf("reading previous WorkerClass %q: %w", oldName, err))
		}
		if previous.Spec.Bootstrap != resolved.Bootstrap {
			return ca.Denied(switchesFamily)
		}
		return ca.Allowed("")
	}
	legacy, err := config.Resolve(nil, old)
	if err != nil {
		return ca.Denied(fmt.Sprintf("%s: %v", subject(oldName), err))
	}
	if legacy.Bootstrap != resolved.Bootstrap {
		return ca.Denied(switchesFamily)
	}
	if !config.Equivalent(legacy, resolved) {
		return ca.Denied(fmt.Sprintf("adopting WorkerClass %q changes legacy configuration; preserve the effective Talos settings, or move the pool to the class's Talos settings without a class first", name))
	}
	for _, field := range templateInputs {
		if !reflect.DeepEqual(old[field], pool[field]) {
			return ca.Denied(fmt.Sprintf("adopting WorkerClass %q also changes %s; adopt the class in an update that changes nothing else", name, field))
		}
	}
	return ca.Allowed("")
}

func subject(class string) string {
	if class == "" {
		return "legacy pool"
	}
	return fmt.Sprintf("WorkerClass %q", class)
}
