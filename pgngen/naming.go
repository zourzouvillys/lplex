package pgngen

import (
	"strings"
	"unicode"
)

// toPascal converts a string like "speed_water" or "Wind Data" to "SpeedWater" or "WindData".
func toPascal(s string) string {
	var b strings.Builder
	upper := true
	for _, r := range s {
		switch {
		case r == '_' || r == ' ' || r == '-' || r == '/' || r == ',' || r == '(' || r == ')' || r == '&':
			upper = true
		case upper:
			b.WriteRune(unicode.ToUpper(r))
			upper = false
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// toSnake converts "speed_water" to "speed_water" (already snake) and
// "Speed Water" to "speed_water".
func toSnake(s string) string {
	var b strings.Builder
	prevUpper := false
	for i, r := range s {
		switch {
		case r == ' ' || r == '-' || r == '/' || r == ',' || r == '(' || r == ')' || r == '&':
			if b.Len() > 0 {
				// Avoid double underscores
				str := b.String()
				if str[len(str)-1] != '_' {
					b.WriteByte('_')
				}
			}
		case unicode.IsUpper(r):
			if i > 0 && !prevUpper && b.Len() > 0 {
				str := b.String()
				if str[len(str)-1] != '_' {
					b.WriteByte('_')
				}
			}
			b.WriteRune(unicode.ToLower(r))
			prevUpper = true
		default:
			b.WriteRune(r)
			prevUpper = false
		}
	}
	// Trim trailing underscores
	return strings.TrimRight(b.String(), "_")
}

// toScreamingSnake converts "WindReference" or "true_north" to "WIND_REFERENCE" or "TRUE_NORTH".
func toScreamingSnake(s string) string {
	return strings.ToUpper(toSnake(s))
}
