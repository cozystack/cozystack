package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	workers "github.com/cozystack/cozystack/api/workers/v1alpha1"
	"github.com/cozystack/cozystack/internal/kubernetesworker/config"
	av1 "k8s.io/api/admission/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ca "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

type Mutator struct{ DefaultWorkerClass string }
type Validator struct{ Reader client.Reader }

func matches(req ca.Request) bool {
	return req.Resource.Group == "apps.cozystack.io" && req.Resource.Version == "v1alpha1" && req.Resource.Resource == "kubernetesnodeses" && (req.Operation == av1.Create || req.Operation == av1.Update)
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
		if name == "" {
			return ca.Errored(http.StatusInternalServerError, fmt.Errorf("platform default WorkerClass is empty"))
		}
	} else {
		name, err = config.ClassName(old)
		if err != nil {
			return ca.Errored(http.StatusBadRequest, err)
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
		return ca.Errored(http.StatusBadRequest, err)
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
	var spec *workers.WorkerClassSpec
	if class != nil {
		spec = &class.Spec
	}
	resolved, err := config.Resolve(spec, pool)
	if err != nil {
		return ca.Denied(fmt.Sprintf("WorkerClass %q: %v", name, err))
	}
	if req.Operation == av1.Update && oldName != name {
		previous, err := v.class(ctx, oldName)
		if err != nil {
			return ca.Errored(http.StatusInternalServerError, fmt.Errorf("reading previous WorkerClass %q: %w", oldName, err))
		}
		var previousSpec *workers.WorkerClassSpec
		if previous != nil {
			previousSpec = &previous.Spec
		}
		oldResolved, err := config.Resolve(previousSpec, old)
		if err != nil {
			return ca.Denied(fmt.Sprintf("previous WorkerClass %q: %v", oldName, err))
		}
		if oldResolved.Bootstrap != resolved.Bootstrap {
			return ca.Denied("workerClassName cannot switch a running pool's bootstrap family; create a new pool")
		}
		if oldName == "" && !config.Equivalent(oldResolved, resolved) {
			return ca.Denied(fmt.Sprintf("adopting WorkerClass %q changes legacy configuration; preserve the effective Talos settings", name))
		}
	}
	return ca.Allowed("")
}
