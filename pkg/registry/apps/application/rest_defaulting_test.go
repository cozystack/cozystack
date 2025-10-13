package application

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apischema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
)

func TestApplication(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Application defaulting Suite")
}

var _ = Describe("defaultLikeKubernetes", func() {
	var rootSchema *apischema.Structural

	BeforeEach(func() {
		rootSchema = buildTestSchema()
	})

	It("applies value-schema defaults to existing map key without merging parent object default", func() {
		spec := map[string]any{
			"nodeGroups": map[string]any{
				"md0": map[string]any{
					"minReplicas": 3,
				},
			},
		}

		err := defaultLikeKubernetes(&spec, rootSchema)
		Expect(err).NotTo(HaveOccurred())

		ng := spec["nodeGroups"].(map[string]any)["md0"].(map[string]any)

		Expect(ng).To(HaveKeyWithValue("minReplicas", BeNumerically("==", 3)))
		Expect(ng).To(HaveKeyWithValue("instanceType", "u1.medium"))
		Expect(ng["roles"]).To(ConsistOf("ingress-nginx"))

		Expect(ng).NotTo(HaveKey("ephemeralStorage"))
		Expect(ng).NotTo(HaveKey("maxReplicas"))
		Expect(ng).NotTo(HaveKey("resources"))
	})

	It("does not create new map keys from parent object default", func() {
		spec := map[string]any{
			"nodeGroups": map[string]any{},
		}

		err := defaultLikeKubernetes(&spec, rootSchema)
		Expect(err).NotTo(HaveOccurred())

		ng := spec["nodeGroups"].(map[string]any)
		Expect(ng).NotTo(HaveKey("md0"))
	})
})

func buildTestSchema() *apischema.Structural {
	instanceType := apischema.Structural{
		Generic: apischema.Generic{
			Type:    "string",
			Default: apischema.JSON{Object: "u1.medium"},
		},
	}
	roles := apischema.Structural{
		Generic: apischema.Generic{
			Type:    "array",
			Default: apischema.JSON{Object: []any{"ingress-nginx"}},
		},
		Items: &apischema.Structural{
			Generic: apischema.Generic{Type: "string"},
		},
	}
	minReplicas := apischema.Structural{
		Generic: apischema.Generic{Type: "integer"},
	}
	ephemeralStorage := apischema.Structural{
		Generic: apischema.Generic{Type: "string"},
	}
	maxReplicas := apischema.Structural{
		Generic: apischema.Generic{Type: "integer"},
	}
	resources := apischema.Structural{
		Generic:    apischema.Generic{Type: "object"},
		Properties: map[string]apischema.Structural{},
	}

	nodeGroupsValue := &apischema.Structural{
		Generic: apischema.Generic{Type: "object"},
		Properties: map[string]apischema.Structural{
			"instanceType":     instanceType,
			"roles":            roles,
			"minReplicas":      minReplicas,
			"ephemeralStorage": ephemeralStorage,
			"maxReplicas":      maxReplicas,
			"resources":        resources,
		},
	}

	nodeGroups := apischema.Structural{
		Generic: apischema.Generic{
			Type: "object",
			Default: apischema.JSON{Object: map[string]any{
				"md0": map[string]any{
					"ephemeralStorage": "20Gi",
					"maxReplicas":      10,
					"minReplicas":      0,
					"resources":        map[string]any{},
				},
			}},
		},
		AdditionalProperties: &apischema.StructuralOrBool{
			Structural: nodeGroupsValue,
		},
	}

	return &apischema.Structural{
		Generic: apischema.Generic{Type: "object"},
		Properties: map[string]apischema.Structural{
			"nodeGroups": nodeGroups,
		},
	}
}
