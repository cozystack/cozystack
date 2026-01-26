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
