package dashboard

import (
	"testing"
)

func TestEventsTab_Structure(t *testing.T) {
	tab := eventsTab("PostgreSQL")

	if tab["key"] != "events" {
		t.Errorf("expected key=events, got %v", tab["key"])
	}
	if tab["label"] != "Events" {
		t.Errorf("expected label=Events, got %v", tab["label"])
	}

	children, ok := tab["children"].([]any)
	if !ok || len(children) != 1 {
		t.Fatal("expected exactly 1 child in events tab")
	}

	table, ok := children[0].(map[string]any)
	if !ok {
		t.Fatal("child is not a map")
	}
	if table["type"] != "EnrichedTable" {
		t.Errorf("expected type=EnrichedTable, got %v", table["type"])
	}

	data, ok := table["data"].(map[string]any)
	if !ok {
		t.Fatal("table data is not a map")
	}
	if data["id"] != "events-table" {
		t.Errorf("expected id=events-table, got %v", data["id"])
	}
	if data["fetchUrl"] != "/api/clusters/{2}/k8s/api/v1/namespaces/{3}/events" {
		t.Errorf("unexpected fetchUrl for non-Tenant: %v", data["fetchUrl"])
	}
	if data["customizationId"] != "factory-details-events" {
		t.Errorf("expected customizationId=factory-details-events, got %v", data["customizationId"])
	}

	pathToItems, ok := data["pathToItems"].([]any)
	if !ok || len(pathToItems) != 1 || pathToItems[0] != "items" {
		t.Errorf("expected pathToItems=[items], got %v", data["pathToItems"])
	}
}

func TestEventsTab_TenantUsesStatusNamespace(t *testing.T) {
	tab := eventsTab("Tenant")
	children := tab["children"].([]any)
	table := children[0].(map[string]any)
	data := table["data"].(map[string]any)

	expectedURL := "/api/clusters/{2}/k8s/api/v1/namespaces/{reqsJsonPath[0]['.status.namespace']}/events"
	if data["fetchUrl"] != expectedURL {
		t.Errorf("expected Tenant fetchUrl to use status.namespace, got %v", data["fetchUrl"])
	}
}
