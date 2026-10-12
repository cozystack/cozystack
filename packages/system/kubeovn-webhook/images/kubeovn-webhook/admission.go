package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mattbaird/jsonpatch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	AnnotationRoutes       = "ovn.kubernetes.io/routes"
	AnnotationPortSecurity = "ovn.kubernetes.io/port_security"
)

func HandleMutatePods(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "could not read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var admissionReviewReq admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &admissionReviewReq); err != nil {
		http.Error(w, "could not unmarshal request", http.StatusBadRequest)
		return
	}

	admissionReviewResp := admissionv1.AdmissionReview{
		TypeMeta: admissionReviewReq.TypeMeta,
	}
	admissionResponse := &admissionv1.AdmissionResponse{
		UID: admissionReviewReq.Request.UID,
	}
	admissionReviewResp.Response = admissionResponse

	if admissionReviewReq.Request.Operation != admissionv1.Create ||
		admissionReviewReq.Request.Kind.Kind != "Pod" {
		admissionResponse.Allowed = true
		writeResponse(w, admissionReviewResp)
		return
	}

	raw := admissionReviewReq.Request.Object.Raw
	var pod corev1.Pod
	if err := json.Unmarshal(raw, &pod); err != nil {
		admissionResponse.Allowed = true
		writeResponse(w, admissionReviewResp)
		return
	}

	ns := admissionReviewReq.Request.Namespace
	nsAnnotations, err := getNamespaceAnnotations(ns)
	if err != nil {
		log.Printf("Failed to get namespace %q: %v", ns, err)
		admissionResponse.Allowed = true
		writeResponse(w, admissionReviewResp)
		return
	}

	mergedAnnotations := map[string]string{}
	if pod.Annotations != nil {
		maps.Copy(mergedAnnotations, pod.Annotations)
	}

	nsRoutes, nsHasRoutes := nsAnnotations[AnnotationRoutes]
	if nsHasRoutes {
		if _, alreadySet := mergedAnnotations[AnnotationRoutes]; !alreadySet {
			mergedAnnotations[AnnotationRoutes] = nsRoutes
		}
	} else if RoutesGlobal != "" {
		if _, alreadySet := mergedAnnotations[AnnotationRoutes]; !alreadySet {
			mergedAnnotations[AnnotationRoutes] = RoutesGlobal
		}
	}

	nsPortSec, nsHasPortSec := nsAnnotations[AnnotationPortSecurity]
	finalPortSec := ""
	if nsHasPortSec {
		finalPortSec = nsPortSec
	} else if PortSecurityGlobal {
		finalPortSec = "true"
	}

	if finalPortSec != "" {
		if _, alreadySet := mergedAnnotations[AnnotationPortSecurity]; !alreadySet {
			mergedAnnotations[AnnotationPortSecurity] = finalPortSec
		}
	}

	if len(mergedAnnotations) == len(pod.Annotations) {
		admissionResponse.Allowed = true
		writeResponse(w, admissionReviewResp)
		return
	}

	op := "replace"
	if pod.Annotations == nil {
		op = "add"
	}

	patches := []jsonpatch.JsonPatchOperation{{
		Operation: op,
		Path:      "/metadata/annotations",
		Value:     mergedAnnotations,
	}}

	patchBytes, err := json.Marshal(patches)
	if err != nil {
		log.Printf("Failed to marshal patch: %v", err)
		admissionResponse.Allowed = true
		writeResponse(w, admissionReviewResp)
		return
	}

	admissionResponse.Allowed = true
	admissionResponse.Patch = patchBytes
	pt := admissionv1.PatchTypeJSONPatch
	admissionResponse.PatchType = &pt

	writeResponse(w, admissionReviewResp)
}

func getNamespaceAnnotations(namespace string) (map[string]string, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	ns, err := clientset.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return ns.Annotations, nil
}

func writeResponse(w http.ResponseWriter, review admissionv1.AdmissionReview) {
	resp, err := json.Marshal(review)
	if err != nil {
		http.Error(w, fmt.Sprintf("could not marshal response: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resp)
}
