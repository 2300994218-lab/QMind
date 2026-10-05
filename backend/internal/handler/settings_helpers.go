package handler

import "strings"

// isMaskedSecret reports whether a secret came back from the settings API in
// its display-only form. A masked value must never replace the real value in
// storage when a user edits an unrelated setting.
func isMaskedSecret(value string) bool {
	return strings.Contains(strings.TrimSpace(value), "****")
}

// maskSecret keeps enough context for a user to recognise a configured key
// without exposing the key itself. It is deliberately idempotent so callers
// can safely pass an already masked value.
func maskSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || isMaskedSecret(value) {
		return value
	}
	runes := []rune(value)
	if len(runes) <= 8 {
		return "****"
	}
	return string(runes[:4]) + "****" + string(runes[len(runes)-4:])
}
