module cozystack.io/kubevirt-csi-driver

go 1.22.0

toolchain go1.22.5

require (
	github.com/container-storage-interface/spec v1.10.0
	google.golang.org/grpc v1.65.0
	gopkg.in/yaml.v2 v2.4.0
	k8s.io/api v0.31.4
	k8s.io/apimachinery v0.31.4
	k8s.io/client-go v12.0.0+incompatible
	k8s.io/klog/v2 v2.130.1
	k8s.io/mount-utils v0.33.1
	kubevirt.io/containerized-data-importer-api v1.59.0
	kubevirt.io/csi-driver v0.0.0-20250702202414-a8d6605bc999
)

require (
	github.com/coreos/go-systemd/v22 v22.5.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/emicklei/go-restful/v3 v3.11.0 // indirect
	github.com/fxamacker/cbor/v2 v2.7.0 // indirect
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-openapi/jsonpointer v0.19.6 // indirect
	github.com/go-openapi/jsonreference v0.20.2 // indirect
	github.com/go-openapi/swag v0.22.4 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/mock v1.6.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/gnostic-models v0.6.8 // indirect
	github.com/google/go-cmp v0.6.0 // indirect
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/imdario/mergo v0.3.15 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/kubernetes-csi/csi-lib-utils v0.18.1 // indirect
	github.com/kubernetes-csi/external-snapshotter/client/v6 v6.3.0 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/moby/sys/mountinfo v0.7.1 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/opencontainers/runc v1.1.13 // indirect
	github.com/opencontainers/runtime-spec v1.0.3-0.20220909204839-494a5a6aca78 // indirect
	github.com/openshift/api v0.0.0-20230503133300-8bbcb7ca7183 // indirect
	github.com/openshift/custom-resource-status v1.1.2 // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	golang.org/x/net v0.33.0 // indirect
	golang.org/x/oauth2 v0.21.0 // indirect
	golang.org/x/sys v0.28.0 // indirect
	golang.org/x/term v0.27.0 // indirect
	golang.org/x/text v0.21.0 // indirect
	golang.org/x/time v0.3.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240701130421-f6361c86f094 // indirect
	google.golang.org/protobuf v1.34.2 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	k8s.io/apiextensions-apiserver v0.26.4 // indirect
	k8s.io/kube-openapi v0.0.0-20240228011516-70dd3763d340 // indirect
	k8s.io/utils v0.0.0-20240711033017-18e509b52bc8 // indirect
	kubevirt.io/api v1.2.2 // indirect
	kubevirt.io/controller-lifecycle-operator-sdk/api v0.0.0-20220329064328-f3cc58c6ed90 // indirect
	sigs.k8s.io/json v0.0.0-20221116044647-bc3834ca7abd // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.4.1 // indirect
	sigs.k8s.io/yaml v1.4.0 // indirect
)

replace (
	k8s.io/api => k8s.io/api v0.31.4
	k8s.io/apiextensions-apiserver => k8s.io/apiextensions-apiserver v0.31.4
	k8s.io/apimachinery => k8s.io/apimachinery v0.31.4
	k8s.io/apiserver => k8s.io/apiserver v0.31.4
	k8s.io/cli-runtime => k8s.io/cli-runtime v0.31.4
	k8s.io/client-go => k8s.io/client-go v0.31.4
	k8s.io/cloud-provider => k8s.io/cloud-provider v0.31.4
	k8s.io/cluster-bootstrap => k8s.io/cluster-bootstrap v0.31.4
	k8s.io/code-generator => k8s.io/code-generator v0.31.4
	k8s.io/component-base => k8s.io/component-base v0.31.4
	k8s.io/component-helpers => k8s.io/component-helpers v0.31.4
	k8s.io/controller-manager => k8s.io/controller-manager v0.31.4
	k8s.io/cri-api => k8s.io/cri-api v0.31.4
	k8s.io/csi-translation-lib => k8s.io/csi-translation-lib v0.31.4
	k8s.io/kube-aggregator => k8s.io/kube-aggregator v0.31.4
	k8s.io/kube-controller-manager => k8s.io/kube-controller-manager v0.31.4
	k8s.io/kube-proxy => k8s.io/kube-proxy v0.31.4
	k8s.io/kube-scheduler => k8s.io/kube-scheduler v0.31.4
	k8s.io/kubectl => k8s.io/kubectl v0.31.4
	k8s.io/kubelet => k8s.io/kubelet v0.31.4
	k8s.io/legacy-cloud-providers => k8s.io/legacy-cloud-providers v0.31.4
	k8s.io/metrics => k8s.io/metrics v0.31.4
	k8s.io/mount-utils => k8s.io/mount-utils v0.31.4
	k8s.io/pod-security-admission => k8s.io/pod-security-admission v0.31.4
	k8s.io/sample-apiserver => k8s.io/sample-apiserver v0.31.4
	kubevirt.io/csi-driver => github.com/kubevirt/csi-driver v0.0.0-20250702202414-a8d6605bc999
)
