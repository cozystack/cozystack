export function enrichSchemaWithEnums(
  schema: unknown,
  path: string[],
  enumMap: Record<string, string[]>
// eslint-disable-next-line @typescript-eslint/no-explicit-any
): any {
  if (!schema || typeof schema !== "object") return schema

  const currentPath = path.join(".")
  const result = { ...(schema as Record<string, unknown>) }

  if (enumMap[currentPath]) {
    result.enum = enumMap[currentPath]
  }

  if (result.properties && typeof result.properties === "object") {
    result.properties = Object.fromEntries(
      Object.entries(result.properties as Record<string, unknown>).map(([key, value]) => [
        key,
        enrichSchemaWithEnums(value, [...path, key], enumMap),
      ])
    )
  }

  return result
}
