package dashboard

import (
	"crypto/sha1"
	"fmt"
	"strings"
)

// ---------------- Unified ID generation helpers ----------------

// generateID creates a unique ID based on the provided components
func generateID(components ...string) string {
	if len(components) == 0 {
		return ""
	}

	// Join components with hyphens and convert to lowercase
	id := strings.ToLower(strings.Join(components, "-"))

	// Remove any special characters that might cause issues
	id = strings.ReplaceAll(id, ".", "-")
	id = strings.ReplaceAll(id, "/", "-")
	id = strings.ReplaceAll(id, " ", "-")

	// Remove multiple consecutive hyphens
	for strings.Contains(id, "--") {
		id = strings.ReplaceAll(id, "--", "-")
	}

	// Remove leading/trailing hyphens
	id = strings.Trim(id, "-")

	return id
}

// generateSpecID creates a spec.id from metadata.name and other components
func generateSpecID(metadataName string, components ...string) string {
	allComponents := append([]string{metadataName}, components...)
	return generateID(allComponents...)
}

// generateMetadataName creates metadata.name from spec.id
func generateMetadataName(specID string) string {
	// Convert ID format to metadata.name format
	// Replace / with . for metadata.name
	name := strings.ReplaceAll(specID, "/", ".")

	// Clean up the name to be RFC 1123 compliant
	// Remove any leading/trailing dots and ensure it starts/ends with alphanumeric
	name = strings.Trim(name, ".")

	// Replace multiple consecutive dots with single dot
	for strings.Contains(name, "..") {
		name = strings.ReplaceAll(name, "..", ".")
	}

	// Replace any remaining problematic patterns
	// Handle cases like "stock-namespace-.v1" -> "stock-namespace-v1"
	name = strings.ReplaceAll(name, "-.", "-")
	name = strings.ReplaceAll(name, ".-", "-")

	// Ensure it starts with alphanumeric character
	if len(name) > 0 && !isAlphanumeric(name[0]) {
		name = "a" + name
	}

	// Ensure it ends with alphanumeric character
	if len(name) > 0 && !isAlphanumeric(name[len(name)-1]) {
		name = name + "a"
	}

	return name
}

// isAlphanumeric checks if a character is alphanumeric
func isAlphanumeric(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

// ---------------- Unified badge generation helpers ----------------

// BadgeConfig holds configuration for badge generation
type BadgeConfig struct {
	Text  string
	Color string
	Title string
	Size  BadgeSize
}

// BadgeSize represents the size of the badge
type BadgeSize int

const (
	BadgeSizeSmall BadgeSize = iota
	BadgeSizeMedium
	BadgeSizeLarge
)

// generateBadgeConfig creates a BadgeConfig from kind and optional custom values
func generateBadgeConfig(kind string, customText, customColor, customTitle string) BadgeConfig {
	config := BadgeConfig{
		Text:  initialsFromKind(kind),
		Color: hexColorForKind(kind),
		Title: strings.ToLower(kind),
		Size:  BadgeSizeMedium,
	}

	// Override with custom values if provided
	if customText != "" {
		config.Text = customText
	}
	if customColor != "" {
		config.Color = customColor
	}
	if customTitle != "" {
		config.Title = customTitle
	}

	return config
}

// createUnifiedBadge creates a badge using the unified BadgeConfig
func createUnifiedBadge(id string, config BadgeConfig) map[string]any {
	fontSize := "15px"
	if config.Size == BadgeSizeLarge {
		fontSize = "20px"
	} else if config.Size == BadgeSizeSmall {
		fontSize = "12px"
	}

	return map[string]any{
		"type": "antdText",
		"data": map[string]any{
			"id":    id,
			"text":  config.Text,
			"title": config.Title,
			"style": map[string]any{
				"backgroundColor": config.Color,
				"borderRadius":    "20px",
				"color":           "#fff",
				"display":         "inline-block",
				"fontFamily":      "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
				"fontSize":        fontSize,
				"fontWeight":      float64(400),
				"lineHeight":      "24px",
				"minWidth":        float64(24),
				"padding":         "0 9px",
				"textAlign":       "center",
				"whiteSpace":      "nowrap",
			},
		},
	}
}

// createUnifiedBadgeFromKind creates a badge from kind with automatic color generation
func createUnifiedBadgeFromKind(id, kind, title string, size BadgeSize) map[string]any {
	config := BadgeConfig{
		Text:  initialsFromKind(kind),
		Color: hexColorForKind(kind),
		Title: title,
		Size:  size,
	}
	return createUnifiedBadge(id, config)
}

// ---------------- Resource creation helpers with unified approach ----------------

// ResourceConfig holds configuration for resource creation
type ResourceConfig struct {
	SpecID       string
	MetadataName string
	Kind         string
	Title        string
	BadgeConfig  BadgeConfig
}

// createResourceConfig creates a ResourceConfig from components
func createResourceConfig(components []string, kind, title string) ResourceConfig {
	// Generate spec.id from components
	specID := generateID(components...)

	// Generate metadata.name from spec.id
	metadataName := generateMetadataName(specID)

	// Generate badge config
	badgeConfig := generateBadgeConfig(kind, "", "", title)

	return ResourceConfig{
		SpecID:       specID,
		MetadataName: metadataName,
		Kind:         kind,
		Title:        title,
		BadgeConfig:  badgeConfig,
	}
}

// ---------------- Enhanced color generation ----------------

// getColorForKind returns a color for a specific kind with improved distribution
func getColorForKind(kind string) string {
	// Use existing hexColorForKind function
	return hexColorForKind(kind)
}

// getColorForType returns a color for a specific type (like "namespace", "service", etc.)
func getColorForType(typeName string) string {
	// Map common types to specific colors for consistency
	colorMap := map[string]string{
		"namespace":       "#a25792ff",
		"service":         "#6ca100",
		"pod":             "#009596",
		"node":            "#8476d1",
		"secret":          "#c46100",
		"configmap":       "#b48c78ff",
		"ingress":         "#2e7dff",
		"workloadmonitor": "#c46100",
		"module":          "#8b5cf6",
	}

	if color, exists := colorMap[strings.ToLower(typeName)]; exists {
		return color
	}

	// Fall back to hash-based color generation
	return hexColorForKind(typeName)
}

// ---------------- Automatic ID generation for UI elements ----------------

// generateElementID creates an ID for UI elements based on context and type
func generateElementID(elementType, context string, components ...string) string {
	allComponents := append([]string{elementType, context}, components...)
	return generateID(allComponents...)
}

// generateBadgeID creates an ID for badge elements
func generateBadgeID(context string, kind string) string {
	return generateElementID("badge", context, kind)
}

// generateLinkID creates an ID for link elements
func generateLinkID(context string, linkType string) string {
	return generateElementID("link", context, linkType)
}

// generateTextID creates an ID for text elements
func generateTextID(context string, textType string) string {
	return generateElementID("text", context, textType)
}

// generateContainerID creates an ID for container elements
func generateContainerID(context string, containerType string) string {
	return generateElementID("container", context, containerType)
}

// generateTableID creates an ID for table elements
func generateTableID(context string, tableType string) string {
	return generateElementID("table", context, tableType)
}

// ---------------- Enhanced resource creation with automatic IDs ----------------

// createResourceWithAutoID creates a resource with automatically generated IDs
func createResourceWithAutoID(resourceType, name string, spec map[string]any) map[string]any {
	// Generate spec.id from name
	specID := generateSpecID(name)

	// Add the spec.id to the spec
	spec["id"] = specID

	return spec
}

// ---------------- Unified resource creation helpers ----------------

// UnifiedResourceConfig holds configuration for unified resource creation
type UnifiedResourceConfig struct {
	Name         string
	ResourceType string
	Kind         string
	Plural       string
	Title        string
	Color        string
	BadgeText    string
	Size         BadgeSize
}

// createUnifiedFactory creates a factory using unified approach
func createUnifiedFactory(config UnifiedResourceConfig, tabs []any, urlsToFetch []any) map[string]any {
	// Generate spec.id from name
	specID := generateSpecID(config.Name)

	// Create header with unified badge
	badgeConfig := BadgeConfig{
		Text:  config.BadgeText,
		Color: config.Color,
		Title: config.Title,
		Size:  config.Size,
	}
	if badgeConfig.Text == "" {
		badgeConfig.Text = initialsFromKind(config.Kind)
	}
	if badgeConfig.Color == "" {
		badgeConfig.Color = getColorForKind(config.Kind)
	}

	badge := createUnifiedBadge(generateBadgeID("header", config.Kind), badgeConfig)
	nameText := parsedText(generateTextID("header", "name"), "{reqsJsonPath[0]['.metadata.name']['-']}", map[string]any{
		"fontFamily": "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
		"fontSize":   float64(20),
		"lineHeight": "24px",
	})

	header := antdFlex(generateContainerID("header", "row"), float64(6), []any{
		badge,
		nameText,
	})

	// Add marginBottom style to header
	if headerData, ok := header["data"].(map[string]any); ok {
		if headerData["style"] == nil {
			headerData["style"] = map[string]any{}
		}
		if style, ok := headerData["style"].(map[string]any); ok {
			style["marginBottom"] = float64(24)
		}
	}

	return map[string]any{
		"key":                           config.Name,
		"id":                            specID,
		"sidebarTags":                   []any{fmt.Sprintf("%s-sidebar", strings.ToLower(config.Kind))},
		"withScrollableMainContentCard": true,
		"urlsToFetch":                   urlsToFetch,
		"data": []any{
			header,
			map[string]any{
				"type": "antdTabs",
				"data": map[string]any{
					"id":               generateContainerID("tabs", strings.ToLower(config.Kind)),
					"defaultActiveKey": "details",
					"items":            tabs,
				},
			},
		},
	}
}

// createUnifiedCustomColumn creates a custom column using unified approach
func createUnifiedCustomColumn(name, jsonPath, kind, title, href string) map[string]any {
	badgeConfig := generateBadgeConfig(kind, "", "", title)
	badge := createUnifiedBadge(generateBadgeID("column", kind), badgeConfig)

	linkID := generateLinkID("column", "name")
	if jsonPath == ".metadata.namespace" {
		linkID = generateLinkID("column", "namespace")
	}

	link := antdLink(linkID, "{reqsJsonPath[0]['"+jsonPath+"']['-']}", href)

	return map[string]any{
		"name":     name,
		"type":     "factory",
		"jsonPath": jsonPath,
		"customProps": map[string]any{
			"disableEventBubbling": true,
			"items": []any{
				map[string]any{
					"type": "antdFlex",
					"data": map[string]any{
						"id":    generateContainerID("column", "header"),
						"align": "center",
						"gap":   float64(6),
					},
					"children": []any{badge, link},
				},
			},
		},
	}
}

// ---------------- Utility functions ----------------

// hashString creates a short hash from a string for ID generation
func hashString(s string) string {
	hash := sha1.Sum([]byte(s))
	return fmt.Sprintf("%x", hash[:4])
}

// sanitizeForID removes characters that shouldn't be in IDs
func sanitizeForID(s string) string {
	// Replace problematic characters
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "_", "-")

	// Remove multiple consecutive hyphens
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}

	// Remove leading/trailing hyphens
	s = strings.Trim(s, "-")

	return strings.ToLower(s)
}
