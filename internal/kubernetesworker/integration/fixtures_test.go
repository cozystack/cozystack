package integration

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	ext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

const ns = "tenant-worker-integration"

func renderPackage(t *testing.T, root string, optedIn bool) []*unstructured.Unstructured {
	t.Helper()
	args := []string{"template", "worker-classes", filepath.Join(root, "packages/system/kubernetes-worker-classes"), "--namespace", ns}
	if optedIn {
		args = append(args, "--set-string", "valuesNamespaces[0]="+ns)
	}
	cmd := exec.Command("helm", args...)
	data, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var objects []*unstructured.Unstructured
	for {
		var obj unstructured.Unstructured
		err := decoder.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if obj.GetKind() != "" {
			objects = append(objects, &obj)
		}
	}
	return objects
}

func TestRenderedIntegrationGrantMatchesControllerIdentity(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for _, optedIn := range []bool{false, true} {
		t.Run(fmt.Sprintf("optedIn=%v", optedIn), func(t *testing.T) {
			objects := renderPackage(t, root, optedIn)
			var serviceAccount string
			roles := map[string]rbacv1.Role{}
			var bindings []rbacv1.RoleBinding
			for _, object := range objects {
				switch object.GetKind() {
				case "Deployment":
					serviceAccount, _, _ = unstructured.NestedString(object.Object, "spec", "template", "spec", "serviceAccountName")
				case "Role":
					var role rbacv1.Role
					if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &role); err != nil {
						t.Fatal(err)
					}
					roles[role.Name] = role
				case "RoleBinding":
					var binding rbacv1.RoleBinding
					if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &binding); err != nil {
						t.Fatal(err)
					}
					bindings = append(bindings, binding)
				}
			}
			if serviceAccount == "" {
				t.Fatal("rendered deployment has no ServiceAccount")
			}
			grants := 0
			for _, binding := range bindings {
				role, found := roles[binding.RoleRef.Name]
				if !found {
					continue
				}
				for _, rule := range role.Rules {
					if !reflect.DeepEqual(rule.Resources, []string{"secrets", "configmaps"}) {
						continue
					}
					grants++
					wantRules := []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets", "configmaps"}, Verbs: []string{"get"}}}
					wantSubjects := []rbacv1.Subject{{Kind: "ServiceAccount", Name: serviceAccount, Namespace: ns}}
					if !reflect.DeepEqual(role.Rules, wantRules) || !reflect.DeepEqual(binding.Subjects, wantSubjects) || role.Namespace != ns || binding.Namespace != ns || binding.RoleRef.Kind != "Role" || binding.RoleRef.APIGroup != rbacv1.GroupName {
						t.Fatalf("wrong namespace/controller grant: role=%#v binding=%#v", role, binding)
					}
				}
			}
			want := 0
			if optedIn {
				want = 1
			}
			if grants != want {
				t.Fatalf("Secret/ConfigMap grants=%d, want%d", grants, want)
			}
		})
	}
}

func integrationPoolChartRef() map[string]any {
	return map[string]any{"kind": "ExternalArtifact", "name": "cozystack-kubernetes-nodes-application-kubevirt-kubernetes-nodes", "namespace": "cozy-system"}
}

func integrationProvenanceCRDs(root string) ([]*ext.CustomResourceDefinition, error) {
	wanted := map[string]bool{"packages.cozystack.io": true, "packagesources.cozystack.io": true, "artifactgenerators.source.extensions.fluxcd.io": true, "externalartifacts.source.toolkit.fluxcd.io": true}
	var crds []*ext.CustomResourceDefinition
	for _, source := range []string{"internal/crdinstall/manifests/cozystack.io_packages.yaml", "internal/crdinstall/manifests/cozystack.io_packagesources.yaml", "internal/fluxinstall/manifests/fluxcd.yaml"} {
		f, err := os.Open(filepath.Join(root, source))
		if err != nil {
			return nil, err
		}
		decoder := yamlutil.NewYAMLOrJSONDecoder(f, 4096)
		for {
			var crd ext.CustomResourceDefinition
			err := decoder.Decode(&crd)
			if err == io.EOF {
				break
			}
			if err != nil {
				_ = f.Close()
				return nil, err
			}
			if crd.Kind == "CustomResourceDefinition" && wanted[crd.Name] {
				crds = append(crds, &crd)
				delete(wanted, crd.Name)
			}
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	}
	if len(wanted) != 0 {
		return nil, fmt.Errorf("missing provenance CRDs: %v", wanted)
	}
	return crds, nil
}

func TestPoolFixtureUsesGeneratedApplicationChart(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "packages/system/kubernetes-nodes-rd/cozyrds/kubernetes-nodes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var definition struct {
		Spec struct {
			Release struct {
				ChartRef map[string]any `json:"chartRef"`
			} `json:"release"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(raw, &definition); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(integrationPoolChartRef(), definition.Spec.Release.ChartRef) {
		t.Fatalf("fixture chartRef=%v, producer=%v", integrationPoolChartRef(), definition.Spec.Release.ChartRef)
	}
}

func TestProvenanceCRDSurfaceServesWorkerReaderKinds(t *testing.T) {
	crds, err := integrationProvenanceCRDs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"packages.cozystack.io": "v1alpha1", "packagesources.cozystack.io": "v1alpha1", "artifactgenerators.source.extensions.fluxcd.io": "v1beta1", "externalartifacts.source.toolkit.fluxcd.io": "v1"}
	for _, crd := range crds {
		version, ok := want[crd.Name]
		if !ok {
			t.Fatalf("unexpected CRD %s", crd.Name)
		}
		served := false
		for _, v := range crd.Spec.Versions {
			if v.Name == version && v.Served {
				served = true
			}
		}
		if !served {
			t.Fatalf("%s does not serve %s", crd.Name, version)
		}
		delete(want, crd.Name)
	}
	if len(want) > 0 {
		t.Fatalf("missing provenance CRDs: %v", want)
	}
}
