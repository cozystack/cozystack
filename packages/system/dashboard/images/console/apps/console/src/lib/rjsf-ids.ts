/**
 * RJSF's id configuration, shared rather than repeated: `SchemaForm` passes it
 * to the form, `AdditionalPropertiesField` continues it into the nested form it
 * mounts per map entry, and `focusFirstError` rebuilds an id from an error's
 * property path. If those three disagree, a blocked submit resolves no element
 * and scrolls nowhere.
 */
export const RJSF_ID_PREFIX = "root"
export const RJSF_ID_SEPARATOR = "_"
