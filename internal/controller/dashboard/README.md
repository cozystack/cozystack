# Dashboard Resource Integration Guide

This guide explains how to add a new Kubernetes resource to the Cozystack dashboard. The dashboard provides a unified interface for viewing and managing Kubernetes resources through custom table views, detail pages, and sidebar navigation.

## Overview

Adding a new resource to the dashboard requires three main components:

1. **CustomColumnsOverride**: Defines how the resource appears in list/table views
2. **Factory**: Defines the detail page layout for individual resource instances
3. **Sidebar Entry**: Adds navigation to the resource in the sidebar menu

## Prerequisites

- The resource must have a Kubernetes CustomResourceDefinition (CRD) or be a built-in Kubernetes resource
- Know the resource's API group, version, kind, and plural name
- Understand the resource's spec structure to display relevant fields

## Step-by-Step Guide

### Step 1: Add CustomColumnsOverride

The CustomColumnsOverride defines the columns shown in the resource list table and how clicking on a row navigates to the detail page.

**Location**: `internal/controller/dashboard/static_refactored.go` in `CreateAllCustomColumnsOverrides()`

**Example**:
```go
// Stock namespace backups cozystack io v1alpha1 plans
createCustomColumnsOverride("stock-namespace-/backups.cozystack.io/v1alpha1/plans", []any{
    createCustomColumnWithJsonPath("Name", ".metadata.name", "Plan", "", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/plan-details/{reqsJsonPath[0]['.metadata.name']['-']}"),
    createApplicationRefColumn("Application"),
    createTimestampColumn("Created", ".metadata.creationTimestamp"),
}),
```

**Key Components**:
- **ID Format**: `stock-namespace-/{group}/{version}/{plural}`
- **Name Column**: Use `createCustomColumnWithJsonPath()` with:
  - Badge value: Resource kind in PascalCase (e.g., "Plan", "Service")
  - Link href: `/openapi-ui/{2}/{namespace}/factory/{resource-details}/{name}`
- **Additional Columns**: Use helper functions like:
  - `createStringColumn()`: Simple string values
  - `createTimestampColumn()`: Timestamp fields
  - `createReadyColumn()`: Ready status from conditions
  - Custom helpers for complex fields

**Helper Functions Available**:
- `createCustomColumnWithJsonPath(name, jsonPath, badgeValue, badgeColor, linkHref)`: Column with badge and link
- `createStringColumn(name, jsonPath)`: Simple string column
- `createTimestampColumn(name, jsonPath)`: Timestamp with formatting
- `createReadyColumn()`: Ready status column
- `createBoolColumn(name, jsonPath)`: Boolean column
- `createArrayColumn(name, jsonPath)`: Array column

### Step 2: Add Factory (Detail Page)

The Factory defines the detail page layout when viewing an individual resource instance.

**Location**: `internal/controller/dashboard/static_refactored.go` in `CreateAllFactories()`

**Using Unified Factory Approach** (Recommended):
```go
// Resource details factory using unified approach
resourceConfig := UnifiedResourceConfig{
    Name:         "resource-details",  // Must match the href in CustomColumnsOverride
    ResourceType: "factory",
    Kind:         "ResourceKind",      // PascalCase
    Plural:       "resources",         // lowercase plural
    Title:        "resource",          // lowercase singular
}
resourceTabs := []any{
    map[string]any{
        "key":   "details",
        "label": "Details",
        "children": []any{
            contentCard("details-card", map[string]any{
                "marginBottom": "24px",
            }, []any{
                antdText("details-title", true, "Resource details", map[string]any{
                    "fontSize":     20,
                    "marginBottom": "12px",
                }),
                spacer("details-spacer", 16),
                antdRow("details-grid", []any{48, 12}, []any{
                    antdCol("col-left", 12, []any{
                        antdFlexVertical("col-left-stack", 24, []any{
                            // Metadata fields: Name, Namespace, Created, etc.
                        }),
                    }),
                    antdCol("col-right", 12, []any{
                        antdFlexVertical("col-right-stack", 24, []any{
                            // Spec fields
                        }),
                    }),
                }),
            }),
        },
    },
}
resourceSpec := createUnifiedFactory(resourceConfig, resourceTabs, []any{"/api/clusters/{2}/k8s/apis/{group}/{version}/namespaces/{3}/{plural}/{6}"})

// Add to return list
return []*dashboardv1alpha1.Factory{
    // ... other factories
    createFactory("resource-details", resourceSpec),
}
```

**Key Components**:
- **Factory Key**: Must match the href path segment (e.g., `plan-details` matches `/factory/plan-details/...`)
- **API Endpoint**: `/api/clusters/{2}/k8s/apis/{group}/{version}/namespaces/{3}/{plural}/{6}`
  - `{2}`: Cluster name
  - `{3}`: Namespace
  - `{6}`: Resource name
- **Sidebar Tags**: Automatically set to `["{lowercase-kind}-sidebar"]` by `createUnifiedFactory()`
- **Tabs**: Define the detail page content (Details, YAML, etc.)

**UI Helper Functions**:
- `contentCard(id, style, children)`: Container card
- `antdText(id, strong, text, style)`: Text element
- `antdFlexVertical(id, gap, children)`: Vertical flex container
- `antdRow(id, gutter, children)`: Row layout
- `antdCol(id, span, children)`: Column layout
- `parsedText(id, text, style)`: Text with JSON path parsing
- `parsedTextWithFormatter(id, text, formatter)`: Formatted text (e.g., timestamp)
- `spacer(id, space)`: Spacing element

**Displaying Fields**:
```go
antdFlexVertical("field-block", 4, []any{
    antdText("field-label", true, "Field Label", nil),
    parsedText("field-value", "{reqsJsonPath[0]['.spec.fieldName']['-']}", nil),
}),
```

### Step 3: Add Sidebar Entry

The sidebar entry adds navigation to the resource in the sidebar menu.

**Location**: `internal/controller/dashboard/sidebar.go` in `ensureSidebar()`

**3a. Add to keysAndTags** (around line 110-116):
```go
// Add sidebar for {group} {kind} resource
keysAndTags["{plural}"] = []any{"{lowercase-kind}-sidebar"}
```

**3b. Add Sidebar Section** (if creating a new section, around line 169):
```go
// Add hardcoded {SectionName} section
menuItems = append(menuItems, map[string]any{
    "key":   "{section-key}",
    "label": "{SectionName}",
    "children": []any{
        map[string]any{
            "key":   "{plural}",
            "label": "{ResourceLabel}",
            "link":  "/openapi-ui/{clusterName}/{namespace}/api-table/{group}/{version}/{plural}",
        },
    },
}),
```

**3c. Add Sidebar ID to targetIDs** (around line 220):
```go
"stock-project-factory-{lowercase-kind}-details",
```

**3d. Update Category Ordering** (if adding a new section):
- Update the comment around line 29 to include the new section
- Update `orderCategoryLabels()` function if needed
- Add skip condition in the category loop (around line 149)

**Important Notes**:
- The sidebar tag (`{lowercase-kind}-sidebar`) must match what the Factory uses
- The link format: `/openapi-ui/{clusterName}/{namespace}/api-table/{group}/{version}/{plural}`
- All sidebars share the same `keysAndTags` and `menuItems`, so changes affect all sidebar instances

### Step 4: Verify Integration

1. **Check Factory-Sidebar Connection**:
   - Factory uses `sidebarTags: ["{lowercase-kind}-sidebar"]`
   - Sidebar has `keysAndTags["{plural}"] = []any{"{lowercase-kind}-sidebar"}`
   - Sidebar ID `stock-project-factory-{lowercase-kind}-details` exists in `targetIDs`

2. **Check Navigation Flow**:
   - Sidebar link → List table (CustomColumnsOverride)
   - List table Name column → Detail page (Factory)
   - All paths use consistent naming

3. **Test**:
   - Verify the resource appears in the sidebar
   - Verify the list table displays correctly
   - Verify clicking a resource navigates to the detail page
   - Verify the detail page displays all relevant fields

## Common Patterns

### Displaying Object References

For fields that reference other resources (e.g., `applicationRef`, `storageRef`):

```go
// In CustomColumnsOverride
createApplicationRefColumn("Application"),  // Uses helper function

// In Factory details tab
parsedText("application-ref-value", 
    "{reqsJsonPath[0]['.spec.applicationRef.kind']['-']}.{reqsJsonPath[0]['.spec.applicationRef.apiGroup']['-']}/{reqsJsonPath[0]['.spec.applicationRef.name']['-']}", 
    nil),
```

### Displaying Timestamps

```go
antdFlexVertical("created-block", 4, []any{
    antdText("time-label", true, "Created", nil),
    antdFlex("time-block", 6, []any{
        map[string]any{
            "type": "antdText",
            "data": map[string]any{
                "id":   "time-icon",
                "text": "🌐",
            },
        },
        map[string]any{
            "type": "parsedText",
            "data": map[string]any{
                "formatter": "timestamp",
                "id":        "time-value",
                "text":      "{reqsJsonPath[0]['.metadata.creationTimestamp']['-']}",
            },
        },
    }),
}),
```

### Displaying Namespace with Link

```go
antdFlexVertical("meta-namespace-block", 8, []any{
    antdText("meta-namespace-label", true, "Namespace", nil),
    antdFlex("header-row", 6, []any{
        // Badge component
        map[string]any{
            "type": "antdText",
            "data": map[string]any{
                "id":    "header-badge",
                "text":  "NS",
                "title": "namespace",
                "style": map[string]any{
                    "backgroundColor": "#a25792ff",
                    // ... badge styles
                },
            },
        },
        // Link component
        map[string]any{
            "type": "antdLink",
            "data": map[string]any{
                "id":   "namespace-link",
                "text": "{reqsJsonPath[0]['.metadata.namespace']['-']}",
                "href": "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/marketplace",
            },
        },
    }),
}),
```

## File Reference

- **CustomColumnsOverride**: `internal/controller/dashboard/static_refactored.go` → `CreateAllCustomColumnsOverrides()`
- **Factory**: `internal/controller/dashboard/static_refactored.go` → `CreateAllFactories()`
- **Sidebar**: `internal/controller/dashboard/sidebar.go` → `ensureSidebar()`
- **Helper Functions**: `internal/controller/dashboard/static_helpers.go`
- **UI Helpers**: `internal/controller/dashboard/ui_helpers.go`
- **Unified Helpers**: `internal/controller/dashboard/unified_helpers.go`

## AI Agent Prompt Template

Use this template when asking an AI agent to add a new resource to the dashboard:

```
Please add support for the {ResourceKind} resource ({group}/{version}/{plural}) to the Cozystack dashboard.

Resource Details:
- API Group: {group}
- Version: {version}
- Kind: {ResourceKind}
- Plural: {plural}
- Namespaced: {true/false}

Requirements:
1. Add a CustomColumnsOverride in CreateAllCustomColumnsOverrides() with:
   - ID: stock-namespace-/{group}/{version}/{plural}
   - Name column with {ResourceKind} badge linking to /factory/{lowercase-kind}-details/{name}
   - Additional columns: {list relevant columns}
   
2. Add a Factory in CreateAllFactories() with:
   - Key: {lowercase-kind}-details
   - API endpoint: /api/clusters/{2}/k8s/apis/{group}/{version}/namespaces/{3}/{plural}/{6}
   - Details tab showing all spec fields:
     {list spec fields to display}
   - Use createUnifiedFactory() approach
   
3. Add sidebar entry in ensureSidebar():
   - Add keysAndTags entry: keysAndTags["{plural}"] = []any{"{lowercase-kind}-sidebar"}
   - Add sidebar section: {specify section name or "add to existing section"}
   - Add to targetIDs: "stock-project-factory-{lowercase-kind}-details"
   
4. Ensure Factory sidebarTags matches the keysAndTags entry

Please follow the existing patterns in the codebase, particularly the Plan resource implementation as a reference.
```

## Example: Plan Resource

The Plan resource (`backups.cozystack.io/v1alpha1/plans`) serves as a complete reference implementation:

- **CustomColumnsOverride**: [Diff](https://github.com/cozystack/cozystack/compare/1f0b5ff9ac0d9d8896af46f8a19501c8b728671d..88da2d1f642b6cf03873d368dfdc675de23f1513#diff-8309b1db3362715b3d94a8b0beae7e95d3ccaf248d4f8702aaa12fba398da895R374-R380) in `static_refactored.go`
- **Factory**: [Diff](https://github.com/cozystack/cozystack/compare/1f0b5ff9ac0d9d8896af46f8a19501c8b728671d..88da2d1f642b6cf03873d368dfdc675de23f1513#diff-8309b1db3362715b3d94a8b0beae7e95d3ccaf248d4f8702aaa12fba398da895R1443-R1558) in `static_refactored.go`
- **Sidebar**: [Diff](https://github.com/cozystack/cozystack/compare/1f0b5ff9ac0d9d8896af46f8a19501c8b728671d..88da2d1f642b6cf03873d368dfdc675de23f1513#diff-be79027f7179e457a8f10e225bb921a197ffa390eb8f916d8d21379fadd54a56) in `sidebar.go`
- **Helper Function**: `createApplicationRefColumn()` in `static_helpers.go` ([diff](https://github.com/cozystack/cozystack/compare/1f0b5ff9ac0d9d8896af46f8a19501c8b728671d..88da2d1f642b6cf03873d368dfdc675de23f1513#diff-f17bcccc089cac3a8e965b13b9ab26e678d45bfc9a58d842399f218703e06a08R1026-R1046))

Review [this implementation](https://github.com/cozystack/cozystack/compare/1f0b5ff9ac0d9d8896af46f8a19501c8b728671d..88da2d1f642b6cf03873d368dfdc675de23f1513) for a complete working example.

## Troubleshooting

### Resource doesn't appear in sidebar
- Check that `keysAndTags["{plural}"]` is set correctly
- Verify the sidebar section is added to `menuItems`
- Ensure the sidebar ID is in `targetIDs`

### Clicking resource doesn't navigate to detail page
- Verify the CustomColumnsOverride href matches the Factory key
- Check that the Factory key is exactly `{lowercase-kind}-details`
- Ensure the Factory is added to the return list in `CreateAllFactories()`

### Detail page shows wrong sidebar
- Verify Factory `sidebarTags` matches `keysAndTags["{plural}"]`
- Check that the sidebar ID `stock-project-factory-{lowercase-kind}-details` exists
- Ensure all sidebars are updated (they share the same `keysAndTags`)

### Fields not displaying correctly
- Verify JSON paths are correct (use `.spec.fieldName` format)
- Check that `reqsJsonPath[0]` index is used for single resource views
- Ensure field names match the actual resource spec structure

## Additional Resources

- Kubernetes API Conventions: https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md
- Dashboard API Types: `api/dashboard/v1alpha1/`
- Resource Types: `api/backups/v1alpha1/` (example)

