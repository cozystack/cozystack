package dashboard

import (
	"encoding/json"
	"testing"
)

func TestBuildMultilineStringSchema(t *testing.T) {
	// Test OpenAPI schema with various field types
	openAPISchema := `{
		"properties": {
			"spec": {
				"type": "object",
				"properties": {
					"simpleString": {
						"type": "string",
						"description": "A simple string field"
					},
					"stringWithEnum": {
						"type": "string",
						"enum": ["option1", "option2"],
						"description": "String with enum should be skipped"
					},
					"numberField": {
						"type": "number",
						"description": "Number field should be skipped"
					},
					"nestedObject": {
						"type": "object",
						"properties": {
							"nestedString": {
								"type": "string",
								"description": "Nested string should get multilineString"
							},
							"nestedStringWithEnum": {
								"type": "string",
								"enum": ["a", "b"],
								"description": "Nested string with enum should be skipped"
							}
						}
					},
					"arrayOfObjects": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"itemString": {
									"type": "string",
									"description": "String in array item"
								}
							}
						}
					}
				}
			}
		}
	}`

	schema, err := buildMultilineStringSchema(openAPISchema)
	if err != nil {
		t.Fatalf("buildMultilineStringSchema failed: %v", err)
	}

	// Marshal to JSON for easier inspection
	schemaJSON, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal schema: %v", err)
	}

	t.Logf("Generated schema:\n%s", schemaJSON)

	// Verify that simpleString has multilineString type
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema.properties is not a map")
	}

	// Check spec property exists
	spec, ok := props["spec"].(map[string]any)
	if !ok {
		t.Fatal("spec not found in properties")
	}

	specProps, ok := spec["properties"].(map[string]any)
	if !ok {
		t.Fatal("spec.properties is not a map")
	}

	// Check simpleString
	simpleString, ok := specProps["simpleString"].(map[string]any)
	if !ok {
		t.Fatal("simpleString not found in spec.properties")
	}
	if simpleString["type"] != "multilineString" {
		t.Errorf("simpleString should have type multilineString, got %v", simpleString["type"])
	}

	// Check stringWithEnum should not be present (or should not have multilineString)
	if stringWithEnum, ok := specProps["stringWithEnum"].(map[string]any); ok {
		if stringWithEnum["type"] == "multilineString" {
			t.Error("stringWithEnum should not have multilineString type")
		}
	}

	// Check numberField should not be present
	if numberField, ok := specProps["numberField"].(map[string]any); ok {
		if numberField["type"] != nil {
			t.Error("numberField should not have any type override")
		}
	}

	// Check nested object
	nestedObject, ok := specProps["nestedObject"].(map[string]any)
	if !ok {
		t.Fatal("nestedObject not found in spec.properties")
	}
	nestedProps, ok := nestedObject["properties"].(map[string]any)
	if !ok {
		t.Fatal("nestedObject.properties is not a map")
	}

	// Check nestedString
	nestedString, ok := nestedProps["nestedString"].(map[string]any)
	if !ok {
		t.Fatal("nestedString not found in nestedObject.properties")
	}
	if nestedString["type"] != "multilineString" {
		t.Errorf("nestedString should have type multilineString, got %v", nestedString["type"])
	}

	// Check array of objects
	arrayOfObjects, ok := specProps["arrayOfObjects"].(map[string]any)
	if !ok {
		t.Fatal("arrayOfObjects not found in spec.properties")
	}
	items, ok := arrayOfObjects["items"].(map[string]any)
	if !ok {
		t.Fatal("arrayOfObjects.items is not a map")
	}
	itemProps, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatal("arrayOfObjects.items.properties is not a map")
	}
	itemString, ok := itemProps["itemString"].(map[string]any)
	if !ok {
		t.Fatal("itemString not found in arrayOfObjects.items.properties")
	}
	if itemString["type"] != "multilineString" {
		t.Errorf("itemString should have type multilineString, got %v", itemString["type"])
	}
}

func TestBuildMultilineStringSchemaEmpty(t *testing.T) {
	schema, err := buildMultilineStringSchema("")
	if err != nil {
		t.Fatalf("buildMultilineStringSchema failed on empty string: %v", err)
	}
	if len(schema) != 0 {
		t.Errorf("Expected empty schema for empty input, got %v", schema)
	}
}

func TestBuildMultilineStringSchemaInvalidJSON(t *testing.T) {
	schema, err := buildMultilineStringSchema("{invalid json")
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
	if schema != nil {
		t.Errorf("Expected nil schema for invalid JSON, got %v", schema)
	}
}

func TestApplyListInputOverrides_VMInstance(t *testing.T) {
	openAPIProps := map[string]any{
		"instanceType": map[string]any{"type": "string", "default": "u1.medium"},
	}

	schema := map[string]any{}
	applyListInputOverrides(schema, "VMInstance", openAPIProps)

	specProps := schema["properties"].(map[string]any)["spec"].(map[string]any)["properties"].(map[string]any)
	instanceType, ok := specProps["instanceType"].(map[string]any)
	if !ok {
		t.Fatal("instanceType not found in schema.properties.spec.properties")
	}

	if instanceType["type"] != "listInput" {
		t.Errorf("expected type listInput, got %v", instanceType["type"])
	}

	if instanceType["default"] != "u1.medium" {
		t.Errorf("expected default u1.medium, got %v", instanceType["default"])
	}

	customProps, ok := instanceType["customProps"].(map[string]any)
	if !ok {
		t.Fatal("customProps not found")
	}

	expectedURI := "/api/clusters/{cluster}/k8s/apis/instancetype.kubevirt.io/v1beta1/virtualmachineclusterinstancetypes"
	if customProps["valueUri"] != expectedURI {
		t.Errorf("expected valueUri %s, got %v", expectedURI, customProps["valueUri"])
	}

	if customProps["allowEmpty"] != true {
		t.Errorf("expected allowEmpty true, got %v", customProps["allowEmpty"])
	}

	// Check disks[].name is a listInput
	disks, ok := specProps["disks"].(map[string]any)
	if !ok {
		t.Fatal("disks not found in schema.properties.spec.properties")
	}
	items, ok := disks["items"].(map[string]any)
	if !ok {
		t.Fatal("disks.items not found")
	}
	itemProps, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatal("disks.items.properties not found")
	}
	diskName, ok := itemProps["name"].(map[string]any)
	if !ok {
		t.Fatal("disks.items.properties.name not found")
	}
	if diskName["type"] != "listInput" {
		t.Errorf("expected disks name type listInput, got %v", diskName["type"])
	}
	diskCustomProps, ok := diskName["customProps"].(map[string]any)
	if !ok {
		t.Fatal("disks name customProps not found")
	}
	expectedDiskURI := "/api/clusters/{cluster}/k8s/apis/apps.cozystack.io/v1alpha1/namespaces/{namespace}/vmdisks"
	if diskCustomProps["valueUri"] != expectedDiskURI {
		t.Errorf("expected disks valueUri %s, got %v", expectedDiskURI, diskCustomProps["valueUri"])
	}

	// Check networks[].name is a listInput
	networks, ok := specProps["networks"].(map[string]any)
	if !ok {
		t.Fatal("networks not found in schema.properties.spec.properties")
	}
	netItems, ok := networks["items"].(map[string]any)
	if !ok {
		t.Fatal("networks.items not found")
	}
	netItemProps, ok := netItems["properties"].(map[string]any)
	if !ok {
		t.Fatal("networks.items.properties not found")
	}
	netName, ok := netItemProps["name"].(map[string]any)
	if !ok {
		t.Fatal("networks.items.properties.name not found")
	}
	if netName["type"] != "listInput" {
		t.Errorf("expected networks name type listInput, got %v", netName["type"])
	}
	netCustomProps, ok := netName["customProps"].(map[string]any)
	if !ok {
		t.Fatal("networks name customProps not found")
	}
	expectedNetURI := "/api/clusters/{cluster}/k8s/apis/k8s.cni.cncf.io/v1/namespaces/{namespace}/network-attachment-definitions"
	if netCustomProps["valueUri"] != expectedNetURI {
		t.Errorf("expected networks valueUri %s, got %v", expectedNetURI, netCustomProps["valueUri"])
	}
}

func TestHiddenDeprecatedFields_VMInstance(t *testing.T) {
	hidden := hiddenDeprecatedFields("VMInstance")
	if len(hidden) != 1 {
		t.Fatalf("expected 1 hidden path, got %d", len(hidden))
	}
	path, ok := hidden[0].([]any)
	if !ok {
		t.Fatal("hidden path is not []any")
	}
	if len(path) != 2 || path[0] != "spec" || path[1] != "subnets" {
		t.Errorf("expected [spec subnets], got %v", path)
	}
}

func TestHiddenDeprecatedFields_UnknownKind(t *testing.T) {
	hidden := hiddenDeprecatedFields("SomeOtherKind")
	if hidden != nil {
		t.Errorf("expected nil for unknown kind, got %v", hidden)
	}
}

func TestApplyListInputOverrides_StorageClassSimple(t *testing.T) {
	for _, kind := range []string{
		"ClickHouse", "Harbor", "HTTPCache", "Kubernetes", "MariaDB", "MongoDB",
		"NATS", "OpenBAO", "Postgres", "Qdrant", "RabbitMQ", "Redis", "VMDisk",
	} {
		t.Run(kind, func(t *testing.T) {
			schema := map[string]any{}
			applyListInputOverrides(schema, kind, map[string]any{})

			specProps := schema["properties"].(map[string]any)["spec"].(map[string]any)["properties"].(map[string]any)
			sc, ok := specProps["storageClass"].(map[string]any)
			if !ok {
				t.Fatalf("storageClass not found in spec.properties for kind %s", kind)
			}
			assertStorageClassListInput(t, sc)
		})
	}
}

func TestApplyListInputOverrides_StorageClassFoundationDB(t *testing.T) {
	schema := map[string]any{}
	applyListInputOverrides(schema, "FoundationDB", map[string]any{})

	storageProps := schema["properties"].(map[string]any)["spec"].(map[string]any)["properties"].(map[string]any)["storage"].(map[string]any)["properties"].(map[string]any)
	sc, ok := storageProps["storageClass"].(map[string]any)
	if !ok {
		t.Fatal("storageClass not found in spec.storage.properties")
	}
	assertStorageClassListInput(t, sc)
}

func TestApplyListInputOverrides_VMDisk_SourceFields(t *testing.T) {
	openAPISchema := `{
		"properties":{
			"optical":{"type":"boolean"},
			"source":{
				"type":"object",
				"properties":{
					"image":{
						"type":"object",
						"properties":{
							"name":{"type":"string"}
						}
					},
					"disk":{
						"type":"object",
						"properties":{
							"name":{"type":"string"}
						}
					}
				}
			},
			"storage":{"type":"string"},
			"storageClass":{"type":"string"}
		}
	}`

	schema, err := buildMultilineStringSchema(openAPISchema)
	if err != nil {
		t.Fatalf("buildMultilineStringSchema failed: %v", err)
	}

	applyListInputOverrides(schema, "VMDisk", map[string]any{})

	specProps := schema["properties"].(map[string]any)["spec"].(map[string]any)["properties"].(map[string]any)

	// Check storageClass
	sc, ok := specProps["storageClass"].(map[string]any)
	if !ok {
		t.Fatal("storageClass not found in spec.properties")
	}
	assertStorageClassListInput(t, sc)

	// Check source.image.name listInput
	// Structure: specProps["source"]["properties"]["image"]["properties"]["name"]
	sourceObj, ok := specProps["source"].(map[string]any)
	if !ok {
		t.Fatal("source not found in spec.properties")
	}
	sourceObjProps, ok := sourceObj["properties"].(map[string]any)
	if !ok {
		t.Fatal("source.properties not found")
	}
	imageObj, ok := sourceObjProps["image"].(map[string]any)
	if !ok {
		t.Fatal("image not found in source.properties")
	}
	imageObjProps, ok := imageObj["properties"].(map[string]any)
	if !ok {
		t.Fatal("image.properties not found")
	}
	imgName, ok := imageObjProps["name"].(map[string]any)
	if !ok {
		t.Fatal("name not found in image.properties")
	}
	if imgName["type"] != "listInput" {
		t.Errorf("expected type listInput, got %v", imgName["type"])
	}
	imgNameCustomProps, ok := imgName["customProps"].(map[string]any)
	if !ok {
		t.Fatal("name.customProps not found")
	}
	expectedImageURI := "/api/clusters/{cluster}/k8s/apis/cdi.kubevirt.io/v1beta1/namespaces/cozy-public/datavolumes"
	if imgNameCustomProps["valueUri"] != expectedImageURI {
		t.Errorf("expected valueUri %s, got %v", expectedImageURI, imgNameCustomProps["valueUri"])
	}

	// Check source.disk.name listInput
	diskObj, ok := sourceObjProps["disk"].(map[string]any)
	if !ok {
		t.Fatal("disk not found in source.properties")
	}
	diskObjProps, ok := diskObj["properties"].(map[string]any)
	if !ok {
		t.Fatal("disk.properties not found")
	}
	diskName, ok := diskObjProps["name"].(map[string]any)
	if !ok {
		t.Fatal("name not found in disk.properties")
	}
	if diskName["type"] != "listInput" {
		t.Errorf("expected type listInput, got %v", diskName["type"])
	}
	diskNameCustomProps, ok := diskName["customProps"].(map[string]any)
	if !ok {
		t.Fatal("disk name.customProps not found")
	}
	expectedDiskURI := "/api/clusters/{cluster}/k8s/apis/apps.cozystack.io/v1alpha1/namespaces/{namespace}/vmdisks"
	if diskNameCustomProps["valueUri"] != expectedDiskURI {
		t.Errorf("expected valueUri %s, got %v", expectedDiskURI, diskNameCustomProps["valueUri"])
	}
}

func TestApplyListInputOverrides_StorageClassKafka(t *testing.T) {
	schema := map[string]any{}
	applyListInputOverrides(schema, "Kafka", map[string]any{})

	specProps := schema["properties"].(map[string]any)["spec"].(map[string]any)["properties"].(map[string]any)

	kafkaSC, ok := specProps["kafka"].(map[string]any)["properties"].(map[string]any)["storageClass"].(map[string]any)
	if !ok {
		t.Fatal("storageClass not found in spec.kafka.properties")
	}
	assertStorageClassListInput(t, kafkaSC)

	zkSC, ok := specProps["zookeeper"].(map[string]any)["properties"].(map[string]any)["storageClass"].(map[string]any)
	if !ok {
		t.Fatal("storageClass not found in spec.zookeeper.properties")
	}
	assertStorageClassListInput(t, zkSC)
}

// assertStorageClassListInput verifies that a field is a correctly configured storageClass listInput.
func assertStorageClassListInput(t *testing.T, field map[string]any) {
	t.Helper()
	if field["type"] != "listInput" {
		t.Errorf("expected type listInput, got %v", field["type"])
	}
	customProps, ok := field["customProps"].(map[string]any)
	if !ok {
		t.Fatal("customProps not found")
	}
	expectedURI := "/api/clusters/{cluster}/k8s/apis/storage.k8s.io/v1/storageclasses"
	if customProps["valueUri"] != expectedURI {
		t.Errorf("expected valueUri %s, got %v", expectedURI, customProps["valueUri"])
	}
}

func TestApplyListInputOverrides_UnknownKind(t *testing.T) {
	schema := map[string]any{}
	applyListInputOverrides(schema, "SomeOtherKind", map[string]any{})

	if len(schema) != 0 {
		t.Errorf("expected empty schema for unknown kind, got %v", schema)
	}
}

func TestApplyListInputOverrides_NoDefault(t *testing.T) {
	openAPIProps := map[string]any{
		"instanceType": map[string]any{"type": "string"},
	}

	schema := map[string]any{}
	applyListInputOverrides(schema, "VMInstance", openAPIProps)

	specProps := schema["properties"].(map[string]any)["spec"].(map[string]any)["properties"].(map[string]any)
	instanceType := specProps["instanceType"].(map[string]any)

	if _, exists := instanceType["default"]; exists {
		t.Errorf("expected no default key, got %v", instanceType["default"])
	}
}

func TestApplyListInputOverrides_MergesWithExistingSchema(t *testing.T) {
	openAPIProps := map[string]any{
		"instanceType": map[string]any{"type": "string", "default": "u1.medium"},
	}

	// Simulate schema that already has spec.properties from buildMultilineStringSchema
	schema := map[string]any{
		"properties": map[string]any{
			"spec": map[string]any{
				"properties": map[string]any{
					"otherField": map[string]any{"type": "multilineString"},
				},
			},
		},
	}
	applyListInputOverrides(schema, "VMInstance", openAPIProps)

	specProps := schema["properties"].(map[string]any)["spec"].(map[string]any)["properties"].(map[string]any)

	// instanceType should be added
	if _, ok := specProps["instanceType"].(map[string]any); !ok {
		t.Fatal("instanceType not found after override")
	}

	// otherField should be preserved
	otherField, ok := specProps["otherField"].(map[string]any)
	if !ok {
		t.Fatal("otherField was lost after override")
	}
	if otherField["type"] != "multilineString" {
		t.Errorf("otherField type changed, got %v", otherField["type"])
	}
}

func TestParseOpenAPIProperties(t *testing.T) {
	t.Run("extracts properties", func(t *testing.T) {
		props := parseOpenAPIProperties(`{"type":"object","properties":{"instanceType":{"type":"string","default":"u1.medium"}}}`)
		field, _ := props["instanceType"].(map[string]any)
		if field["default"] != "u1.medium" {
			t.Errorf("expected default u1.medium, got %v", field["default"])
		}
	})

	t.Run("empty string", func(t *testing.T) {
		if props := parseOpenAPIProperties(""); props != nil {
			t.Errorf("expected nil, got %v", props)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		if props := parseOpenAPIProperties("{bad"); props != nil {
			t.Errorf("expected nil, got %v", props)
		}
	})

	t.Run("no properties key", func(t *testing.T) {
		if props := parseOpenAPIProperties(`{"type":"object"}`); props != nil {
			t.Errorf("expected nil, got %v", props)
		}
	})
}

func TestEnsureSchemaPath(t *testing.T) {
	t.Run("creates path from empty schema", func(t *testing.T) {
		schema := map[string]any{}
		props := ensureSchemaPath(schema, "spec")

		props["field"] = "value"

		// Verify structure: schema.properties.spec.properties.field
		got := schema["properties"].(map[string]any)["spec"].(map[string]any)["properties"].(map[string]any)["field"]
		if got != "value" {
			t.Errorf("expected value, got %v", got)
		}
	})

	t.Run("preserves existing nested properties", func(t *testing.T) {
		schema := map[string]any{
			"properties": map[string]any{
				"spec": map[string]any{
					"properties": map[string]any{
						"existing": "keep",
					},
				},
			},
		}
		props := ensureSchemaPath(schema, "spec")

		if props["existing"] != "keep" {
			t.Errorf("existing property lost, got %v", props["existing"])
		}
	})

	t.Run("multi-level path", func(t *testing.T) {
		schema := map[string]any{}
		props := ensureSchemaPath(schema, "spec", "nested")

		props["deep"] = true

		got := schema["properties"].(map[string]any)["spec"].(map[string]any)["properties"].(map[string]any)["nested"].(map[string]any)["properties"].(map[string]any)["deep"]
		if got != true {
			t.Errorf("expected true, got %v", got)
		}
	})
}

func TestEnsureArrayItemProps(t *testing.T) {
	t.Run("creates from empty parent", func(t *testing.T) {
		parent := map[string]any{}
		props := ensureArrayItemProps(parent, "disks")

		props["name"] = map[string]any{"type": "listInput"}

		got := parent["disks"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["name"].(map[string]any)["type"]
		if got != "listInput" {
			t.Errorf("expected listInput, got %v", got)
		}
	})

	t.Run("preserves existing item properties", func(t *testing.T) {
		parent := map[string]any{
			"disks": map[string]any{
				"items": map[string]any{
					"properties": map[string]any{
						"bus": map[string]any{"type": "string"},
					},
				},
			},
		}
		props := ensureArrayItemProps(parent, "disks")
		props["name"] = map[string]any{"type": "listInput"}

		if props["bus"].(map[string]any)["type"] != "string" {
			t.Error("existing bus property was lost")
		}
		if props["name"].(map[string]any)["type"] != "listInput" {
			t.Error("name property was not added")
		}
	})
}
