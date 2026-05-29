package postgres

// pgJSONOrEmpty returns the input bytes as a string, or "{}" if empty.
// Used for nullable JSONB columns so the SQL DEFAULT '{}' is preserved
// when the caller hasn't populated the metadata field.
func pgJSONOrEmpty(b []byte) string {
	if len(b) == 0 {
		return "{}"
	}
	return string(b)
}
