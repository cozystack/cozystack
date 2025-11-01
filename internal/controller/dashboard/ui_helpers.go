package dashboard

// ---------------- UI helpers (use float64 for numeric fields) ----------------

func contentCard(id string, style map[string]any, children []any) map[string]any {
	return contentCardWithTitle(id, "", style, children)
}

func contentCardWithTitle(id any, title string, style map[string]any, children []any) map[string]any {
	data := map[string]any{
		"id":    id,
		"style": style,
	}
	if title != "" {
		data["title"] = title
	}
	return map[string]any{
		"type":     "ContentCard",
		"data":     data,
		"children": children,
	}
}

func antdText(id string, strong bool, text string, style map[string]any) map[string]any {
	// Auto-generate ID if not provided
	if id == "" {
		id = generateTextID("auto", "antd")
	}

	data := map[string]any{
		"id":     id,
		"text":   text,
		"strong": strong,
	}
	if style != nil {
		data["style"] = style
	}
	return map[string]any{"type": "antdText", "data": data}
}

func parsedText(id, text string, style map[string]any) map[string]any {
	// Auto-generate ID if not provided
	if id == "" {
		id = generateTextID("auto", "parsed")
	}

	data := map[string]any{
		"id":   id,
		"text": text,
	}
	if style != nil {
		data["style"] = style
	}
	return map[string]any{"type": "parsedText", "data": data}
}

func parsedTextWithFormatter(id, text, formatter string) map[string]any {
	// Auto-generate ID if not provided
	if id == "" {
		id = generateTextID("auto", "formatted")
	}

	return map[string]any{
		"type": "parsedText",
		"data": map[string]any{
			"id":        id,
			"text":      text,
			"formatter": formatter,
		},
	}
}

func spacer(id string, space float64) map[string]any {
	// Auto-generate ID if not provided
	if id == "" {
		id = generateContainerID("auto", "spacer")
	}

	return map[string]any{
		"type": "Spacer",
		"data": map[string]any{
			"id":     id,
			"$space": space,
		},
	}
}

func antdFlex(id string, gap float64, children []any) map[string]any {
	// Auto-generate ID if not provided
	if id == "" {
		id = generateContainerID("auto", "flex")
	}

	return map[string]any{
		"type": "antdFlex",
		"data": map[string]any{
			"id":    id,
			"align": "center",
			"gap":   gap,
		},
		"children": children,
	}
}

func antdFlexVertical(id string, gap float64, children []any) map[string]any {
	// Auto-generate ID if not provided
	if id == "" {
		id = generateContainerID("auto", "flex-vertical")
	}

	return map[string]any{
		"type": "antdFlex",
		"data": map[string]any{
			"id":       id,
			"vertical": true,
			"gap":      gap,
		},
		"children": children,
	}
}

func antdRow(id string, gutter []any, children []any) map[string]any {
	// Auto-generate ID if not provided
	if id == "" {
		id = generateContainerID("auto", "row")
	}

	return map[string]any{
		"type": "antdRow",
		"data": map[string]any{
			"id":     id,
			"gutter": gutter,
		},
		"children": children,
	}
}

func antdCol(id string, span float64, children []any) map[string]any {
	return map[string]any{
		"type": "antdCol",
		"data": map[string]any{
			"id":   id,
			"span": span,
		},
		"children": children,
	}
}

func antdColWithStyle(id string, style map[string]any, children []any) map[string]any {
	return map[string]any{
		"type": "antdCol",
		"data": map[string]any{
			"id":    id,
			"style": style,
		},
		"children": children,
	}
}

func antdLink(id, text, href string) map[string]any {
	return map[string]any{
		"type": "antdLink",
		"data": map[string]any{
			"id":   id,
			"text": text,
			"href": href,
		},
	}
}

// ---------------- Badge helpers ----------------

// createBadge creates a badge element with the given text, color, and title
func createBadge(id, text, color, title string) map[string]any {
	return map[string]any{
		"type": "antdText",
		"data": map[string]any{
			"id":    id,
			"text":  text,
			"title": title,
			"style": map[string]any{
				"whiteSpace":      "nowrap",
				"backgroundColor": color,
				"fontWeight":      400,
				"lineHeight":      "24px",
				"minWidth":        24,
				"textAlign":       "center",
				"borderRadius":    "20px",
				"color":           "#fff",
				"display":         "inline-block",
				"fontFamily":      "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
				"fontSize":        "15px",
				"padding":         "0 9px",
			},
		},
	}
}

// createBadgeFromKind creates a badge using the existing badge generation functions
func createBadgeFromKind(id, kind, title string) map[string]any {
	return createUnifiedBadgeFromKind(id, kind)
}

// createHeaderBadge creates a badge specifically for headers with consistent styling
func createHeaderBadge(id, kind, plural string) map[string]any {
	return createUnifiedBadgeFromKind(id, kind)
}
