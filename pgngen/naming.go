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

// toLowerCamel converts "VictronRegister" or "wind_speed" to "victronRegister" or "windSpeed".
func toLowerCamel(s string) string {
	p := toPascal(s)
	if p == "" {
		return ""
	}
	r := []rune(p)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

// toScreamingSnake converts "WindReference" or "true_north" to "WIND_REFERENCE" or "TRUE_NORTH".
func toScreamingSnake(s string) string {
	return strings.ToUpper(toSnake(s))
}

// toPlural applies basic English pluralization rules to a snake_case name.
//
//   - ends in s, x, z, sh, ch -> append "es"  (status -> statuses)
//   - ends in consonant + y   -> replace y with "ies" (category -> categories)
//   - everything else         -> append "s"   (indicator -> indicators)
func toPlural(s string) string {
	if s == "" {
		return s
	}
	if strings.HasSuffix(s, "s") || strings.HasSuffix(s, "x") || strings.HasSuffix(s, "z") ||
		strings.HasSuffix(s, "sh") || strings.HasSuffix(s, "ch") {
		return s + "es"
	}
	if strings.HasSuffix(s, "y") && len(s) >= 2 {
		prev := s[len(s)-2]
		if !isVowel(prev) {
			return s[:len(s)-1] + "ies"
		}
	}
	return s + "s"
}

func isVowel(c byte) bool {
	switch c {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	}
	return false
}
