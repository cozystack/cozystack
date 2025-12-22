/*
Copyright 2024 The Cozystack Authors.

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

package application

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fields "k8s.io/apimachinery/pkg/fields"
	labels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
	internalapiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"

	// Importing API errors package to construct appropriate error responses
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// Ensure REST implements necessary interfaces
var (
	_ rest.Getter          = &REST{}
	_ rest.Lister          = &REST{}
	_ rest.Updater         = &REST{}
	_ rest.Creater         = &REST{}
	_ rest.GracefulDeleter = &REST{}
	_ rest.Watcher         = &REST{}
	_ rest.Patcher         = &REST{}
)

// Define constants for label and annotation prefixes
const (
	LabelPrefix      = "apps.cozystack.io-"
	AnnotationPrefix = "apps.cozystack.io-"
)

// Application label keys - use constants from API package
const (
	ApplicationKindLabel  = appsv1alpha1.ApplicationKindLabel
	ApplicationGroupLabel = appsv1alpha1.ApplicationGroupLabel
	ApplicationNameLabel  = appsv1alpha1.ApplicationNameLabel
)

// Define the GroupVersionResource for HelmRelease
var helmReleaseGVR = schema.GroupVersionResource{
	Group:    "helm.toolkit.fluxcd.io",
	Version:  "v2",
	Resource: "helmreleases",
}

// REST implements the RESTStorage interface for Application resources
type REST struct {
	c             client.Client
	w             client.WithWatch
	gvr           schema.GroupVersionResource
	gvk           schema.GroupVersionKind
	kindName      string
	singularName  string
	releaseConfig config.ReleaseConfig
	specSchema    *structuralschema.Structural
}

// NewREST creates a new REST storage for Application with specific configuration
func NewREST(c client.Client, w client.WithWatch, config *config.Resource) *REST {
	var specSchema *structuralschema.Structural

	if raw := strings.TrimSpace(config.Application.OpenAPISchema); raw != "" {
		var v1js apiextv1.JSONSchemaProps
		if err := json.Unmarshal([]byte(raw), &v1js); err != nil {
			klog.Errorf("Failed to unmarshal v1 OpenAPI schema: %v", err)
		} else {
			scheme := runtime.NewScheme()
			_ = internalapiext.AddToScheme(scheme)
			_ = apiextv1.AddToScheme(scheme)

			var ijs internalapiext.JSONSchemaProps
			if err := scheme.Convert(&v1js, &ijs, nil); err != nil {
				klog.Errorf("Failed to convert v1->internal JSONSchemaProps: %v", err)
			} else if s, err := structuralschema.NewStructural(&ijs); err != nil {
				klog.Errorf("Failed to create structural schema: %v", err)
			} else {
				specSchema = s
			}
		}
	}

	return &REST{
		c: c,
		w: w,
		gvr: schema.GroupVersionResource{
			Group:    appsv1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: config.Application.Plural,
		},
		gvk: schema.GroupVersion{
			Group:   appsv1alpha1.GroupName,
			Version: "v1alpha1",
		}.WithKind(config.Application.Kind),
		kindName:      config.Application.Kind,
		singularName:  config.Application.Singular,
		releaseConfig: config.Release,
		specSchema:    specSchema,
	}
}

// NamespaceScoped indicates whether the resource is namespaced
func (r *REST) NamespaceScoped() bool {
	return true
}

// GetSingularName returns the singular name of the resource
func (r *REST) GetSingularName() string {
	return r.singularName
}

// Create handles the creation of a new Application by converting it to a HelmRelease
func (r *REST) Create(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
	// Assert the object is of type Application
	app, ok := obj.(*appsv1alpha1.Application)
	if !ok {
		return nil, fmt.Errorf("expected *appsv1alpha1.Application object, got %T", obj)
	}

	// Convert Application to HelmRelease
	helmRelease, err := r.ConvertApplicationToHelmRelease(app)
	if err != nil {
		klog.Errorf("Conversion error: %v", err)
		return nil, fmt.Errorf("conversion error: %v", err)
	}

	// Merge system labels (from config) directly
	helmRelease.Labels = mergeMaps(r.releaseConfig.Labels, helmRelease.Labels)
	// Merge user labels with prefix
	helmRelease.Labels = mergeMaps(helmRelease.Labels, addPrefixedMap(app.Labels, LabelPrefix))
	// Add application metadata labels
	if helmRelease.Labels == nil {
		helmRelease.Labels = make(map[string]string)
	}
	helmRelease.Labels[ApplicationKindLabel] = r.kindName
	helmRelease.Labels[ApplicationGroupLabel] = r.gvk.Group
	helmRelease.Labels[ApplicationNameLabel] = app.Name
	// Note: Annotations from config are not handled as r.releaseConfig.Annotations is undefined

	klog.V(6).Infof("Creating HelmRelease %s in namespace %s", helmRelease.Name, app.Namespace)

	// Create HelmRelease in Kubernetes
	err = r.c.Create(ctx, helmRelease, &client.CreateOptions{Raw: options})
	if err != nil {
		klog.Errorf("Failed to create HelmRelease %s: %v", helmRelease.Name, err)
		return nil, fmt.Errorf("failed to create HelmRelease: %v", err)
	}

	// Convert the created HelmRelease back to Application
	convertedApp, err := r.ConvertHelmReleaseToApplication(helmRelease)
	if err != nil {
		klog.Errorf("Conversion error from HelmRelease to Application for resource %s: %v", helmRelease.GetName(), err)
		return nil, fmt.Errorf("conversion error: %v", err)
	}

	klog.V(6).Infof("Successfully created and converted HelmRelease %s to Application", helmRelease.GetName())

	klog.V(6).Infof("Successfully retrieved and converted resource %s of type %s", convertedApp.GetName(), r.gvr.Resource)
	return &convertedApp, nil
}

// Get retrieves an Application by converting the corresponding HelmRelease
func (r *REST) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	namespace, err := r.getNamespace(ctx)
	if err != nil {
		klog.Errorf("Failed to get namespace: %v", err)
		return nil, err
	}

	klog.V(6).Infof("Attempting to retrieve resource %s of type %s in namespace %s", name, r.gvr.Resource, namespace)

	// Get the corresponding HelmRelease using the new prefix
	helmReleaseName := r.releaseConfig.Prefix + name
	helmRelease := &helmv2.HelmRelease{}
	err = r.c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: helmReleaseName}, helmRelease, &client.GetOptions{Raw: options})
	if err != nil {
		klog.Errorf("Error retrieving HelmRelease for resource %s: %v", name, err)

		// Check if the error is a NotFound error
		if apierrors.IsNotFound(err) {
			// Return a NotFound error for the Application resource instead of HelmRelease
			return nil, apierrors.NewNotFound(r.gvr.GroupResource(), name)
		}

		// For other errors, return them as-is
		return nil, err
	}

	// Check if HelmRelease has required labels
	if !r.hasRequiredApplicationLabels(helmRelease) {
		klog.Errorf("HelmRelease %s does not match the required application labels", helmReleaseName)
		// Return a NotFound error for the Application resource
		return nil, apierrors.NewNotFound(r.gvr.GroupResource(), name)
	}

	// Convert HelmRelease to Application
	convertedApp, err := r.ConvertHelmReleaseToApplication(helmRelease)
	if err != nil {
		klog.Errorf("Conversion error from HelmRelease to Application for resource %s: %v", name, err)
		return nil, fmt.Errorf("conversion error: %v", err)
	}

	klog.V(6).Infof("Successfully retrieved and converted resource %s of kind %s", name, r.gvr.Resource)
	return &convertedApp, nil
}

// List retrieves a list of Applications by converting HelmReleases
func (r *REST) List(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
	namespace, err := r.getNamespace(ctx)
	if err != nil {
		klog.Errorf("Failed to get namespace: %v", err)
		return nil, err
	}

	klog.V(6).Infof("Attempting to list HelmReleases in namespace %s with options: %v", namespace, options)

	// Get resource name from the request (if any)
	var resourceName string
	if requestInfo, ok := request.RequestInfoFrom(ctx); ok {
		resourceName = requestInfo.Name
	}

	// Initialize variables for selector mapping
	var helmFieldSelector string
	var helmLabelSelector string

	// Process field.selector
	if options.FieldSelector != nil {
		fs, err := fields.ParseSelector(options.FieldSelector.String())
		if err != nil {
			klog.Errorf("Invalid field selector: %v", err)
			return nil, fmt.Errorf("invalid field selector: %v", err)
		}
		// Check if selector is for metadata.name
		if name, exists := fs.RequiresExactMatch("metadata.name"); exists {
			// Convert Application name to HelmRelease name
			mappedName := r.releaseConfig.Prefix + name
			// Create new field.selector for HelmRelease
			helmFieldSelector = fields.OneTermEqualSelector("metadata.name", mappedName).String()
		} else {
			// If field.selector contains other fields, map them directly
			helmFieldSelector = fs.String()
		}
	}

	// Process label.selector
	// Always add application metadata label requirements
	appKindReq, err := labels.NewRequirement(ApplicationKindLabel, selection.Equals, []string{r.kindName})
	if err != nil {
		klog.Errorf("Error creating application kind label requirement: %v", err)
		return nil, fmt.Errorf("error creating application kind label requirement: %v", err)
	}
	appGroupReq, err := labels.NewRequirement(ApplicationGroupLabel, selection.Equals, []string{r.gvk.Group})
	if err != nil {
		klog.Errorf("Error creating application group label requirement: %v", err)
		return nil, fmt.Errorf("error creating application group label requirement: %v", err)
	}
	labelRequirements := []labels.Requirement{*appKindReq, *appGroupReq}

	if options.LabelSelector != nil {
		ls := options.LabelSelector.String()
		parsedLabels, err := labels.Parse(ls)
		if err != nil {
			klog.Errorf("Invalid label selector: %v", err)
			return nil, fmt.Errorf("invalid label selector: %v", err)
		}
		if !parsedLabels.Empty() {
			reqs, _ := parsedLabels.Requirements()
			var prefixedReqs []labels.Requirement
			for _, req := range reqs {
				// Add prefix to each label key
				prefixedReq, err := labels.NewRequirement(LabelPrefix+req.Key(), req.Operator(), req.Values().List())
				if err != nil {
					klog.Errorf("Error prefixing label key: %v", err)
					return nil, fmt.Errorf("error prefixing label key: %v", err)
				}
				prefixedReqs = append(prefixedReqs, *prefixedReq)
			}
			labelRequirements = append(labelRequirements, prefixedReqs...)
		}
	}
	helmLabelSelector = labels.NewSelector().Add(labelRequirements...).String()

	klog.V(6).Infof("Using label selector: %s for kind: %s, group: %s", helmLabelSelector, r.kindName, r.gvk.Group)

	// Set ListOptions for HelmRelease with selector mapping
	metaOptions := metav1.ListOptions{
		FieldSelector: helmFieldSelector,
		LabelSelector: helmLabelSelector,
	}

	// List HelmReleases with mapped selectors
	hrList := &helmv2.HelmReleaseList{}
	err = r.c.List(ctx, hrList, &client.ListOptions{
		Namespace: namespace,
		Raw:       &metaOptions,
	})
	if err != nil {
		klog.Errorf("Error listing HelmReleases: %v", err)
		return nil, err
	}

	klog.V(6).Infof("Found %d HelmReleases with label selector", len(hrList.Items))

	// Initialize Application items array
	items := make([]appsv1alpha1.Application, 0, len(hrList.Items))

	// Iterate over HelmReleases and convert to Applications
	// Note: All HelmReleases already match the required labels due to server-side label selector filtering
	for i := range hrList.Items {
		hr := &hrList.Items[i]

		app, err := r.ConvertHelmReleaseToApplication(hr)
		if err != nil {
			klog.Errorf("Error converting HelmRelease %s to Application: %v", hr.GetName(), err)
			continue
		}

		// If resourceName is set, check for match
		if resourceName != "" && app.Name != resourceName {
			continue
		}

		// Apply label.selector
		if options.LabelSelector != nil {
			sel, err := labels.Parse(options.LabelSelector.String())
			if err != nil {
				klog.Errorf("Invalid label selector: %v", err)
				continue
			}
			if !sel.Matches(labels.Set(app.Labels)) {
				continue
			}
		}

		// Apply field.selector by name and namespace (if specified)
		if options.FieldSelector != nil {
			fs, err := fields.ParseSelector(options.FieldSelector.String())
			if err != nil {
				klog.Errorf("Invalid field selector: %v", err)
				continue
			}
			fieldsSet := fields.Set{
				"metadata.name":      app.Name,
				"metadata.namespace": app.Namespace,
			}
			if !fs.Matches(fieldsSet) {
				continue
			}
		}

		items = append(items, app)
	}

	// Create ApplicationList with proper kind
	appList := r.NewList().(*appsv1alpha1.ApplicationList)
	appList.SetResourceVersion(hrList.GetResourceVersion())
	appList.Items = items

	klog.V(6).Infof("Successfully listed %d Application resources in namespace %s", len(items), namespace)
	return appList, nil
}

// Update updates an existing Application by converting it to a HelmRelease
func (r *REST) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
	// Retrieve the existing Application
	oldObj, err := r.Get(ctx, name, &metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			if !forceAllowCreate {
				return nil, false, err
			}
			// If not found and force allow create, create a new one
			obj, err := objInfo.UpdatedObject(ctx, nil)
			if err != nil {
				klog.Errorf("Failed to get updated object: %v", err)
				return nil, false, err
			}
			createdObj, err := r.Create(ctx, obj, createValidation, &metav1.CreateOptions{})
			if err != nil {
				klog.Errorf("Failed to create new Application: %v", err)
				return nil, false, err
			}
			return createdObj, true, nil
		}
		klog.Errorf("Failed to get existing Application %s: %v", name, err)
		return nil, false, err
	}

	// Update the Application object
	newObj, err := objInfo.UpdatedObject(ctx, oldObj)
	if err != nil {
		klog.Errorf("Failed to get updated object: %v", err)
		return nil, false, err
	}

	// Validate the update if a validation function is provided
	if updateValidation != nil {
		if err := updateValidation(ctx, newObj, oldObj); err != nil {
			klog.Errorf("Update validation failed for Application %s: %v", name, err)
			return nil, false, err
		}
	}

	// Assert the new object is of type Application
	app, ok := newObj.(*appsv1alpha1.Application)
	if !ok {
		errMsg := fmt.Sprintf("expected *appsv1alpha1.Application object, got %T", newObj)
		klog.Errorf(errMsg)
		return nil, false, fmt.Errorf(errMsg)
	}

	// Convert Application to HelmRelease
	helmRelease, err := r.ConvertApplicationToHelmRelease(app)
	if err != nil {
		klog.Errorf("Conversion error: %v", err)
		return nil, false, fmt.Errorf("conversion error: %v", err)
	}

	// Ensure ResourceVersion
	if helmRelease.ResourceVersion == "" {
		cur := &helmv2.HelmRelease{}
		err := r.c.Get(ctx, client.ObjectKey{Namespace: helmRelease.Namespace, Name: helmRelease.Name}, cur, &client.GetOptions{Raw: &metav1.GetOptions{}})
		if err != nil {
			return nil, false, fmt.Errorf("failed to fetch current HelmRelease: %w", err)
		}
		helmRelease.SetResourceVersion(cur.GetResourceVersion())
	}

	// Merge system labels (from config) directly
	helmRelease.Labels = mergeMaps(r.releaseConfig.Labels, helmRelease.Labels)
	// Merge user labels with prefix
	helmRelease.Labels = mergeMaps(helmRelease.Labels, addPrefixedMap(app.Labels, LabelPrefix))
	// Add application metadata labels
	if helmRelease.Labels == nil {
		helmRelease.Labels = make(map[string]string)
	}
	helmRelease.Labels[ApplicationKindLabel] = r.kindName
	helmRelease.Labels[ApplicationGroupLabel] = r.gvk.Group
	helmRelease.Labels[ApplicationNameLabel] = app.Name
	// Note: Annotations from config are not handled as r.releaseConfig.Annotations is undefined

	klog.V(6).Infof("Updating HelmRelease %s in namespace %s", helmRelease.Name, helmRelease.Namespace)

	// Update the HelmRelease in Kubernetes
	err = r.c.Update(ctx, helmRelease, &client.UpdateOptions{Raw: &metav1.UpdateOptions{}})
	if err != nil {
		klog.Errorf("Failed to update HelmRelease %s: %v", helmRelease.Name, err)
		return nil, false, fmt.Errorf("failed to update HelmRelease: %v", err)
	}

	// Convert the updated HelmRelease back to Application
	convertedApp, err := r.ConvertHelmReleaseToApplication(helmRelease)
	if err != nil {
		klog.Errorf("Conversion error from HelmRelease to Application for resource %s: %v", helmRelease.GetName(), err)
		return nil, false, fmt.Errorf("conversion error: %v", err)
	}

	klog.V(6).Infof("Successfully updated and converted HelmRelease %s to Application", helmRelease.GetName())

	klog.V(6).Infof("Returning updated Application object: %+v", convertedApp)

	return &convertedApp, false, nil
}

// Delete removes an Application by deleting the corresponding HelmRelease
func (r *REST) Delete(ctx context.Context, name string, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
	namespace, err := r.getNamespace(ctx)
	if err != nil {
		klog.Errorf("Failed to get namespace: %v", err)
		return nil, false, err
	}

	klog.V(6).Infof("Attempting to delete HelmRelease %s in namespace %s", name, namespace)

	// Construct HelmRelease name with the configured prefix
	helmReleaseName := r.releaseConfig.Prefix + name

	// Retrieve the HelmRelease before attempting to delete
	helmRelease := &helmv2.HelmRelease{}
	err = r.c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: helmReleaseName}, helmRelease, &client.GetOptions{Raw: &metav1.GetOptions{}})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// If HelmRelease does not exist, return NotFound error for Application
			klog.Errorf("HelmRelease %s not found in namespace %s", helmReleaseName, namespace)
			return nil, false, apierrors.NewNotFound(r.gvr.GroupResource(), name)
		}
		// For other errors, log and return
		klog.Errorf("Error retrieving HelmRelease %s: %v", helmReleaseName, err)
		return nil, false, err
	}

	// Validate that the HelmRelease has required labels
	if !r.hasRequiredApplicationLabelsWithName(helmRelease, name) {
		klog.Errorf("HelmRelease %s does not match the required application labels", helmReleaseName)
		// Return NotFound error for Application resource
		return nil, false, apierrors.NewNotFound(r.gvr.GroupResource(), name)
	}

	klog.V(6).Infof("Deleting HelmRelease %s in namespace %s", helmReleaseName, namespace)

	// Delete the HelmRelease corresponding to the Application
	err = r.c.Delete(ctx, helmRelease, &client.DeleteOptions{Raw: options})
	if err != nil {
		klog.Errorf("Failed to delete HelmRelease %s: %v", helmReleaseName, err)
		return nil, false, fmt.Errorf("failed to delete HelmRelease: %v", err)
	}

	klog.V(6).Infof("Successfully deleted HelmRelease %s", helmReleaseName)
	return nil, true, nil
}

// Watch sets up a watch on HelmReleases, filters them based on application labels, and converts events to Applications
func (r *REST) Watch(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
	namespace, err := r.getNamespace(ctx)
	if err != nil {
		klog.Errorf("Failed to get namespace: %v", err)
		return nil, err
	}

	klog.V(6).Infof("Setting up watch for HelmReleases in namespace %s with options: %v", namespace, options)

	// Get request information, including resource name if specified
	var resourceName string
	if requestInfo, ok := request.RequestInfoFrom(ctx); ok {
		resourceName = requestInfo.Name
	}

	// Initialize variables for selector mapping
	var helmFieldSelector string
	var helmLabelSelector string

	// Process field.selector
	if options.FieldSelector != nil {
		fs, err := fields.ParseSelector(options.FieldSelector.String())
		if err != nil {
			klog.Errorf("Invalid field selector: %v", err)
			return nil, fmt.Errorf("invalid field selector: %v", err)
		}

		// Check if selector is for metadata.name
		if name, exists := fs.RequiresExactMatch("metadata.name"); exists {
			// Convert Application name to HelmRelease name
			mappedName := r.releaseConfig.Prefix + name
			// Create new field.selector for HelmRelease
			helmFieldSelector = fields.OneTermEqualSelector("metadata.name", mappedName).String()
		} else {
			// If field.selector contains other fields, map them directly
			helmFieldSelector = fs.String()
		}
	}

	// Process label.selector
	// Always add application metadata label requirements
	appKindReq, err := labels.NewRequirement(ApplicationKindLabel, selection.Equals, []string{r.kindName})
	if err != nil {
		klog.Errorf("Error creating application kind label requirement: %v", err)
		return nil, fmt.Errorf("error creating application kind label requirement: %v", err)
	}
	appGroupReq, err := labels.NewRequirement(ApplicationGroupLabel, selection.Equals, []string{r.gvk.Group})
	if err != nil {
		klog.Errorf("Error creating application group label requirement: %v", err)
		return nil, fmt.Errorf("error creating application group label requirement: %v", err)
	}
	labelRequirements := []labels.Requirement{*appKindReq, *appGroupReq}

	if options.LabelSelector != nil {
		ls := options.LabelSelector.String()
		parsedLabels, err := labels.Parse(ls)
		if err != nil {
			klog.Errorf("Invalid label selector: %v", err)
			return nil, fmt.Errorf("invalid label selector: %v", err)
		}
		if !parsedLabels.Empty() {
			reqs, _ := parsedLabels.Requirements()
			var prefixedReqs []labels.Requirement
			for _, req := range reqs {
				// Add prefix to each label key
				prefixedReq, err := labels.NewRequirement(LabelPrefix+req.Key(), req.Operator(), req.Values().List())
				if err != nil {
					klog.Errorf("Error prefixing label key: %v", err)
					return nil, fmt.Errorf("error prefixing label key: %v", err)
				}
				prefixedReqs = append(prefixedReqs, *prefixedReq)
			}
			labelRequirements = append(labelRequirements, prefixedReqs...)
		}
	}
	helmLabelSelector = labels.NewSelector().Add(labelRequirements...).String()

	// Set ListOptions for HelmRelease with selector mapping
	metaOptions := metav1.ListOptions{
		Watch:           true,
		ResourceVersion: options.ResourceVersion,
		FieldSelector:   helmFieldSelector,
		LabelSelector:   helmLabelSelector,
	}

	// Start watch on HelmRelease with mapped selectors
	hrList := &helmv2.HelmReleaseList{}
	helmWatcher, err := r.w.Watch(ctx, hrList, &client.ListOptions{
		Namespace: namespace,
		Raw:       &metaOptions,
	})
	if err != nil {
		klog.Errorf("Error setting up watch for HelmReleases: %v", err)
		return nil, err
	}

	// Create a custom watcher to transform events
	customW := &customWatcher{
		resultChan: make(chan watch.Event),
		stopChan:   make(chan struct{}),
		underlying: helmWatcher,
	}

	go func() {
		defer close(customW.resultChan)
		defer customW.underlying.Stop()
		for {
			select {
			case event, ok := <-customW.underlying.ResultChan():
				if !ok {
					// The watcher has been closed, attempt to re-establish the watch
					klog.Warning("HelmRelease watcher closed, attempting to re-establish")
					// Implement retry logic or exit based on your requirements
					return
				}

				// Check if the object is a *v1.Status
				if status, ok := event.Object.(*metav1.Status); ok {
					klog.V(4).Infof("Received Status object in HelmRelease watch: %v", status.Message)
					continue // Skip processing this event
				}

				// Proceed with processing HelmRelease objects
				hr, ok := event.Object.(*helmv2.HelmRelease)
				if !ok {
					klog.V(4).Infof("Expected HelmRelease object, got %T", event.Object)
					continue
				}

				// Note: All HelmReleases already match the required labels due to server-side label selector filtering
				// Convert HelmRelease to Application
				app, err := r.ConvertHelmReleaseToApplication(hr)
				if err != nil {
					klog.Errorf("Error converting HelmRelease to Application: %v", err)
					continue
				}

				// Apply field.selector by name if specified
				if resourceName != "" && app.Name != resourceName {
					continue
				}

				// Apply label.selector
				if options.LabelSelector != nil {
					sel, err := labels.Parse(options.LabelSelector.String())
					if err != nil {
						klog.Errorf("Invalid label selector: %v", err)
						continue
					}
					if !sel.Matches(labels.Set(app.Labels)) {
						continue
					}
				}

				// Create watch event with Application object
				appEvent := watch.Event{
					Type:   event.Type,
					Object: &app,
				}

				// Send event to custom watcher
				select {
				case customW.resultChan <- appEvent:
				case <-customW.stopChan:
					return
				case <-ctx.Done():
					return
				}

			case <-customW.stopChan:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	klog.V(6).Infof("Custom watch established successfully")
	return customW, nil
}

// customWatcher wraps the original watcher and filters/converts events
type customWatcher struct {
	resultChan chan watch.Event
	stopChan   chan struct{}
	stopOnce   sync.Once
	underlying watch.Interface
}

// Stop terminates the watch
func (cw *customWatcher) Stop() {
	cw.stopOnce.Do(func() {
		close(cw.stopChan)
		if cw.underlying != nil {
			cw.underlying.Stop()
		}
	})
}

// ResultChan returns the event channel
func (cw *customWatcher) ResultChan() <-chan watch.Event {
	return cw.resultChan
}

// getNamespace extracts the namespace from the context
func (r *REST) getNamespace(ctx context.Context) (string, error) {
	namespace, ok := request.NamespaceFrom(ctx)
	if !ok {
		err := fmt.Errorf("namespace not found in context")
		klog.Errorf(err.Error())
		return "", err
	}
	return namespace, nil
}

// hasRequiredApplicationLabels checks if a HelmRelease has the required application labels
// matching the REST instance's kind and group
func (r *REST) hasRequiredApplicationLabels(hr *helmv2.HelmRelease) bool {
	if hr.Labels == nil {
		return false
	}
	return hr.Labels[ApplicationKindLabel] == r.kindName &&
		hr.Labels[ApplicationGroupLabel] == r.gvk.Group
}

// hasRequiredApplicationLabelsWithName checks if a HelmRelease has the required application labels
// matching the REST instance's kind, group, and the specified application name
func (r *REST) hasRequiredApplicationLabelsWithName(hr *helmv2.HelmRelease, appName string) bool {
	if hr.Labels == nil {
		return false
	}
	return hr.Labels[ApplicationKindLabel] == r.kindName &&
		hr.Labels[ApplicationGroupLabel] == r.gvk.Group &&
		hr.Labels[ApplicationNameLabel] == appName
}

// mergeMaps combines two maps of labels or annotations
func mergeMaps(a, b map[string]string) map[string]string {
	if a == nil && b == nil {
		return nil
	}
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	merged := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		merged[k] = v
	}
	for k, v := range b {
		merged[k] = v
	}
	return merged
}

// addPrefixedMap adds the predefined prefix to the keys of a map
func addPrefixedMap(original map[string]string, prefix string) map[string]string {
	if original == nil {
		return nil
	}
	processed := make(map[string]string, len(original))
	for k, v := range original {
		processed[prefix+k] = v
	}
	return processed
}

// filterPrefixedMap filters a map by the predefined prefix and removes the prefix from the keys
func filterPrefixedMap(original map[string]string, prefix string) map[string]string {
	if original == nil {
		return nil
	}
	processed := make(map[string]string)
	for k, v := range original {
		if strings.HasPrefix(k, prefix) {
			newKey := strings.TrimPrefix(k, prefix)
			processed[newKey] = v
		}
	}
	return processed
}

// ConvertHelmReleaseToApplication converts a HelmRelease to an Application
func (r *REST) ConvertHelmReleaseToApplication(hr *helmv2.HelmRelease) (appsv1alpha1.Application, error) {
	klog.V(6).Infof("Converting HelmRelease to Application for resource %s", hr.GetName())

	// Convert HelmRelease struct to Application struct
	app, err := r.convertHelmReleaseToApplication(hr)
	if err != nil {
		klog.Errorf("Error converting from HelmRelease to Application: %v", err)
		return appsv1alpha1.Application{}, err
	}

	if err := r.applySpecDefaults(&app); err != nil {
		return app, fmt.Errorf("defaulting error: %w", err)
	}

	klog.V(6).Infof("Successfully converted HelmRelease %s to Application", hr.GetName())
	return app, nil
}

// ConvertApplicationToHelmRelease converts an Application to a HelmRelease
func (r *REST) ConvertApplicationToHelmRelease(app *appsv1alpha1.Application) (*helmv2.HelmRelease, error) {
	return r.convertApplicationToHelmRelease(app)
}

// convertHelmReleaseToApplication implements the actual conversion logic
func (r *REST) convertHelmReleaseToApplication(hr *helmv2.HelmRelease) (appsv1alpha1.Application, error) {
	app := appsv1alpha1.Application{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps.cozystack.io/v1alpha1",
			Kind:       r.kindName,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:              strings.TrimPrefix(hr.Name, r.releaseConfig.Prefix),
			Namespace:         hr.Namespace,
			UID:               hr.GetUID(),
			ResourceVersion:   hr.GetResourceVersion(),
			CreationTimestamp: hr.CreationTimestamp,
			DeletionTimestamp: hr.DeletionTimestamp,
			Labels:            filterPrefixedMap(hr.Labels, LabelPrefix),
			Annotations:       filterPrefixedMap(hr.Annotations, AnnotationPrefix),
		},
		Spec: hr.Spec.Values,
		Status: appsv1alpha1.ApplicationStatus{
			Version: hr.Status.LastAttemptedRevision,
		},
	}

	var conditions []metav1.Condition
	for _, hrCondition := range hr.GetConditions() {
		if hrCondition.Type == "Ready" || hrCondition.Type == "Released" {
			conditions = append(conditions, metav1.Condition{
				LastTransitionTime: hrCondition.LastTransitionTime,
				Reason:             hrCondition.Reason,
				Message:            hrCondition.Message,
				Status:             hrCondition.Status,
				Type:               hrCondition.Type,
			})
		}
	}
	app.SetConditions(conditions)

	// Add namespace field for Tenant applications
	if r.kindName == "Tenant" {
		app.Status.Namespace = r.computeTenantNamespace(hr.Namespace, app.Name)
	}

	return app, nil
}

// convertApplicationToHelmRelease implements the actual conversion logic
func (r *REST) convertApplicationToHelmRelease(app *appsv1alpha1.Application) (*helmv2.HelmRelease, error) {
	helmRelease := &helmv2.HelmRelease{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "helm.toolkit.fluxcd.io/v2",
			Kind:       "HelmRelease",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            r.releaseConfig.Prefix + app.Name,
			Namespace:       app.Namespace,
			Labels:          addPrefixedMap(app.Labels, LabelPrefix),
			Annotations:     addPrefixedMap(app.Annotations, AnnotationPrefix),
			ResourceVersion: app.ObjectMeta.ResourceVersion,
			UID:             app.ObjectMeta.UID,
		},
		Spec: helmv2.HelmReleaseSpec{
			Chart: &helmv2.HelmChartTemplate{
				Spec: helmv2.HelmChartTemplateSpec{
					Chart:             r.releaseConfig.Chart.Name,
					Version:           ">= 0.0.0-0",
					ReconcileStrategy: "Revision",
					SourceRef: helmv2.CrossNamespaceObjectReference{
						Kind:      r.releaseConfig.Chart.SourceRef.Kind,
						Name:      r.releaseConfig.Chart.SourceRef.Name,
						Namespace: r.releaseConfig.Chart.SourceRef.Namespace,
					},
				},
			},
			Interval: metav1.Duration{Duration: 5 * time.Minute},
			Install: &helmv2.Install{
				Remediation: &helmv2.InstallRemediation{
					Retries: -1,
				},
			},
			Upgrade: &helmv2.Upgrade{
				Remediation: &helmv2.UpgradeRemediation{
					Retries: -1,
				},
			},
			Values: app.Spec,
		},
	}

	return helmRelease, nil
}

// ConvertToTable implements the TableConvertor interface for displaying resources in a table format
func (r *REST) ConvertToTable(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	klog.V(6).Infof("ConvertToTable: received object of type %T", object)

	var table metav1.Table

	switch obj := object.(type) {
	case *appsv1alpha1.ApplicationList:
		table = r.buildTableFromApplications(obj.Items)
		table.ListMeta.ResourceVersion = obj.ListMeta.ResourceVersion
	case *appsv1alpha1.Application:
		table = r.buildTableFromApplication(*obj)
		table.ListMeta.ResourceVersion = obj.GetResourceVersion()
	default:
		resource := schema.GroupResource{}
		if info, ok := request.RequestInfoFrom(ctx); ok {
			resource = schema.GroupResource{Group: info.APIGroup, Resource: info.Resource}
		}
		return nil, errNotAcceptable{
			resource: resource,
			message:  "object does not implement the Object interfaces",
		}
	}

	// Handle table options
	if opt, ok := tableOptions.(*metav1.TableOptions); ok && opt != nil && opt.NoHeaders {
		table.ColumnDefinitions = nil
	}

	table.TypeMeta = metav1.TypeMeta{
		APIVersion: "meta.k8s.io/v1",
		Kind:       "Table",
	}

	klog.V(6).Infof("ConvertToTable: returning table with %d rows", len(table.Rows))
	return &table, nil
}

// buildTableFromApplications constructs a table from a list of Applications
func (r *REST) buildTableFromApplications(apps []appsv1alpha1.Application) metav1.Table {
	table := metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "NAME", Type: "string", Description: "Name of the Application", Priority: 0},
			{Name: "READY", Type: "string", Description: "Ready status of the Application", Priority: 0},
			{Name: "AGE", Type: "string", Description: "Age of the Application", Priority: 0},
			{Name: "VERSION", Type: "string", Description: "Version of the Application", Priority: 0},
		},
		Rows: make([]metav1.TableRow, 0, len(apps)),
	}
	now := time.Now()

	for i := range apps {
		app := &apps[i]
		row := metav1.TableRow{
			Cells:  []interface{}{app.GetName(), getReadyStatus(app.Status.Conditions), computeAge(app.GetCreationTimestamp().Time, now), getVersion(app.Status.Version)},
			Object: runtime.RawExtension{Object: app},
		}
		table.Rows = append(table.Rows, row)
	}

	return table
}

// buildTableFromApplication constructs a table from a single Application
func (r *REST) buildTableFromApplication(app appsv1alpha1.Application) metav1.Table {
	table := metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "NAME", Type: "string", Description: "Name of the Application", Priority: 0},
			{Name: "READY", Type: "string", Description: "Ready status of the Application", Priority: 0},
			{Name: "AGE", Type: "string", Description: "Age of the Application", Priority: 0},
			{Name: "VERSION", Type: "string", Description: "Version of the Application", Priority: 0},
		},
		Rows: []metav1.TableRow{},
	}
	now := time.Now()

	a := app
	row := metav1.TableRow{
		Cells:  []interface{}{app.GetName(), getReadyStatus(app.Status.Conditions), computeAge(app.GetCreationTimestamp().Time, now), getVersion(app.Status.Version)},
		Object: runtime.RawExtension{Object: &a},
	}
	table.Rows = append(table.Rows, row)

	return table
}

// getVersion returns the application version or a placeholder if unknown
func getVersion(version string) string {
	if version == "" {
		return "<unknown>"
	}
	return version
}

// computeAge calculates the age of the object based on CreationTimestamp and current time
func computeAge(creationTime, currentTime time.Time) string {
	ageDuration := currentTime.Sub(creationTime)
	return duration.HumanDuration(ageDuration)
}

// getReadyStatus returns the ready status based on conditions
func getReadyStatus(conditions []metav1.Condition) string {
	for _, condition := range conditions {
		if condition.Type == "Ready" {
			switch condition.Status {
			case metav1.ConditionTrue:
				return "True"
			case metav1.ConditionFalse:
				return "False"
			default:
				return "Unknown"
			}
		}
	}
	return "Unknown"
}

// computeTenantNamespace computes the namespace for a Tenant application based on the specified logic
func (r *REST) computeTenantNamespace(currentNamespace, tenantName string) string {
	hrName := r.releaseConfig.Prefix + tenantName

	switch {
	case currentNamespace == "tenant-root" && hrName == "tenant-root":
		// 1) root tenant inside root namespace
		return "tenant-root"

	case currentNamespace == "tenant-root":
		// 2) any other tenant in root namespace
		return fmt.Sprintf("tenant-%s", tenantName)

	default:
		// 3) tenant in a dedicated namespace
		return fmt.Sprintf("%s-%s", currentNamespace, tenantName)
	}
}

// Destroy releases resources associated with REST
func (r *REST) Destroy() {
	// No additional actions needed to release resources.
}

// New creates a new instance of Application
func (r *REST) New() runtime.Object {
	obj := &appsv1alpha1.Application{}
	obj.TypeMeta = metav1.TypeMeta{
		APIVersion: r.gvk.GroupVersion().String(),
		Kind:       r.kindName,
	}
	return obj
}

// NewList returns an empty list of Application objects
func (r *REST) NewList() runtime.Object {
	obj := &appsv1alpha1.ApplicationList{}
	obj.TypeMeta = metav1.TypeMeta{
		APIVersion: r.gvk.GroupVersion().String(),
		Kind:       r.kindName + "List",
	}
	return obj
}

// Kind returns the resource kind used for API discovery
func (r *REST) Kind() string {
	return r.gvk.Kind
}

// GroupVersionKind returns the GroupVersionKind for REST
func (r *REST) GroupVersionKind(schema.GroupVersion) schema.GroupVersionKind {
	return r.gvk
}

// errNotAcceptable indicates that the resource does not support conversion to Table
type errNotAcceptable struct {
	resource schema.GroupResource
	message  string
}

func (e errNotAcceptable) Error() string {
	return e.message
}

func (e errNotAcceptable) Status() metav1.Status {
	return metav1.Status{
		Status:  metav1.StatusFailure,
		Code:    http.StatusNotAcceptable,
		Reason:  metav1.StatusReason("NotAcceptable"),
		Message: e.Error(),
	}
}
