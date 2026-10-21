package main

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// The kubemox objects the proxy reads and writes. Only the fields it needs
// are typed; the rest stays with kubemox.

var (
	vmGVR = schema.GroupVersionResource{
		Group: "proxmox.alperen.cloud", Version: "v1alpha1", Resource: "virtualmachines",
	}
	connectionGVR = schema.GroupVersionResource{
		Group: "proxmox.alperen.cloud", Version: "v1alpha1", Resource: "proxmoxconnections",
	}
)

// vmInfo is where a VirtualMachine runs, as kubemox last observed it.
type vmInfo struct {
	Namespace  string
	Name       string
	Connection string
	Node       string
	VMID       int64
	State      string
	PowerState string
}

func lookupVM(ctx context.Context, dyn dynamic.Interface, namespace, name string) (*vmInfo, error) {
	obj, err := dyn.Resource(vmGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	info := &vmInfo{Namespace: namespace, Name: name}
	info.Connection, _, _ = unstructured.NestedString(obj.Object, "spec", "connectionRef", "name")
	info.PowerState, _, _ = unstructured.NestedString(obj.Object, "spec", "powerState")
	info.Node, _, _ = unstructured.NestedString(obj.Object, "status", "status", "node")
	info.State, _, _ = unstructured.NestedString(obj.Object, "status", "status", "state")
	info.VMID, _, _ = unstructured.NestedInt64(obj.Object, "status", "status", "id")
	if info.Node == "" {
		info.Node, _, _ = unstructured.NestedString(obj.Object, "spec", "nodeName")
	}
	return info, nil
}

// placed reports whether kubemox has created the machine on the hypervisor.
func (v *vmInfo) placed() bool { return v.VMID > 0 && v.Node != "" }

// setPowerState asks kubemox for a power state: Running or Stopped.
func setPowerState(ctx context.Context, dyn dynamic.Interface, namespace, name, state string) error {
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"powerState": state}})
	if err != nil {
		return err
	}
	_, err = dyn.Resource(vmGVR).Namespace(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

// connectionSpec is the part of a ProxmoxConnection the proxy needs.
type connectionSpec struct {
	Endpoint           string
	TokenID            string
	InsecureSkipVerify bool
	SecretName         string
	SecretNamespace    string
	SecretKey          string
}

func lookupConnection(ctx context.Context, dyn dynamic.Interface, name string) (*connectionSpec, error) {
	obj, err := dyn.Resource(connectionGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	c := &connectionSpec{}
	c.Endpoint, _, _ = unstructured.NestedString(obj.Object, "spec", "endpoint")
	c.TokenID, _, _ = unstructured.NestedString(obj.Object, "spec", "tokenID")
	c.InsecureSkipVerify, _, _ = unstructured.NestedBool(obj.Object, "spec", "insecureSkipVerify")
	c.SecretName, _, _ = unstructured.NestedString(obj.Object, "spec", "secretFrom", "name")
	c.SecretNamespace, _, _ = unstructured.NestedString(obj.Object, "spec", "secretFrom", "namespace")
	c.SecretKey, _, _ = unstructured.NestedString(obj.Object, "spec", "secretFrom", "key")
	if c.SecretKey == "" {
		c.SecretKey = "token"
	}
	if c.Endpoint == "" || c.SecretName == "" || c.SecretNamespace == "" {
		return nil, fmt.Errorf("ProxmoxConnection %q names no endpoint or token secret", name)
	}
	return c, nil
}

func isNotFound(err error) bool { return apierrors.IsNotFound(err) }
