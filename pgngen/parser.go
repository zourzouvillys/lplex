package pgngen

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Parse parses DSL source text into a Schema.
//
// DSL syntax:
//
//	# line comment
//
//	enum WindReference {
//	  0 = "true_north"
//	  1 = "magnetic_north"
//	  2 = "apparent"
//	}
//
//	pgn 130306 "Wind Data" {
//	  sid             uint8           :8
//	  wind_speed      uint16          :16  scale=0.01   unit="m/s"
//	  wind_angle      uint16          :16  scale=0.0001 unit="rad"
//	  wind_reference  WindReference   :3
//	  _                               :5
//	}
func Parse(src string) (*Schema, error) {
	p := &parser{lines: strings.Split(src, "\n")}
	return p.parse()
}

// ParseFile is a convenience wrapper that reads a file and parses it.
// The caller is responsible for reading the file contents.
func ParseFiles(sources map[string]string) (*Schema, error) {
	merged := &Schema{}
	for name, src := range sources {
		s, err := Parse(src)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		merged.Enums = append(merged.Enums, s.Enums...)
		merged.Lookups = append(merged.Lookups, s.Lookups...)
		merged.PGNs = append(merged.PGNs, s.PGNs...)
	}
	if err := merged.Resolve(); err != nil {
		return nil, err
	}
	return merged, nil
}

type parser struct {
	lines []string
	pos   int // current line index (0-based)
}

func (p *parser) parse() (*Schema, error) {
	s := &Schema{}
	for p.pos < len(p.lines) {
		line := p.stripComment(p.lines[p.pos])
		tokens := tokenize(line)
		if len(tokens) == 0 {
			p.pos++
			continue
		}
		switch tokens[0] {
		case "enum":
			e, err := p.parseEnum(tokens)
			if err != nil {
				return nil, err
			}
			s.Enums = append(s.Enums, e)
		case "lookup":
			l, err := p.parseLookup(tokens)
			if err != nil {
				return nil, err
			}
			s.Lookups = append(s.Lookups, l)
		case "pgn":
			d, err := p.parsePGN(tokens)
			if err != nil {
				return nil, err
			}
			s.PGNs = append(s.PGNs, d)
		default:
			return nil, p.errorf("unexpected keyword %q", tokens[0])
		}
	}
	return s, nil
}

func (p *parser) parseEnum(tokens []string) (EnumDef, error) {
	// enum <Name> {
	if len(tokens) < 3 || tokens[2] != "{" {
		return EnumDef{}, p.errorf("expected: enum <Name> {")
	}
	e := EnumDef{Name: tokens[1], Line: p.lineNum()}
	p.pos++
	for p.pos < len(p.lines) {
		line := p.stripComment(p.lines[p.pos])
		toks := tokenize(line)
		if len(toks) == 0 {
			p.pos++
			continue
		}
		if toks[0] == "}" {
			p.pos++
			return e, nil
		}
		// <value> = "<name>"
		if len(toks) < 3 || toks[1] != "=" {
			return EnumDef{}, p.errorf("expected: <value> = \"<name>\"")
		}
		val, err := strconv.Atoi(toks[0])
		if err != nil {
			return EnumDef{}, p.errorf("invalid enum value: %s", toks[0])
		}
		name := unquote(toks[2])
		e.Values = append(e.Values, EnumValue{Value: val, Name: name})
		p.pos++
	}
	return EnumDef{}, p.errorf("unterminated enum block")
}

var validLookupKeyTypes = map[string]bool{
	"uint8": true, "uint16": true, "uint32": true, "uint64": true,
}

func (p *parser) parseLookup(tokens []string) (LookupDef, error) {
	// lookup <Name> <type> {
	if len(tokens) < 4 || tokens[3] != "{" {
		return LookupDef{}, p.errorf("expected: lookup <Name> <type> {")
	}
	keyType := tokens[2]
	if !validLookupKeyTypes[keyType] {
		return LookupDef{}, p.errorf("invalid lookup key type %q (must be uint8, uint16, uint32, or uint64)", keyType)
	}
	l := LookupDef{Name: tokens[1], KeyType: keyType, Line: p.lineNum()}
	p.pos++
	for p.pos < len(p.lines) {
		line := p.stripComment(p.lines[p.pos])
		toks := tokenize(line)
		if len(toks) == 0 {
			p.pos++
			continue
		}
		if toks[0] == "}" {
			p.pos++
			return l, nil
		}
		// <key> = "<name>"
		if len(toks) < 3 || toks[1] != "=" {
			return LookupDef{}, p.errorf("expected: <key> = \"<name>\"")
		}
		key, err := strconv.ParseInt(toks[0], 0, 64)
		if err != nil {
			return LookupDef{}, p.errorf("invalid lookup key: %s", toks[0])
		}
		name := unquote(toks[2])
		l.Values = append(l.Values, LookupValue{Key: key, Name: name})
		p.pos++
	}
	return LookupDef{}, p.errorf("unterminated lookup block")
}

func (p *parser) parsePGN(tokens []string) (PGNDef, error) {
	// pgn <number> "<description>" {
	if len(tokens) < 4 || tokens[3] != "{" {
		return PGNDef{}, p.errorf("expected: pgn <number> \"<description>\" {")
	}
	pgn, err := strconv.ParseUint(tokens[1], 10, 32)
	if err != nil {
		return PGNDef{}, p.errorf("invalid PGN number: %s", tokens[1])
	}
	desc := unquote(tokens[2])
	d := PGNDef{PGN: uint32(pgn), Description: desc, Line: p.lineNum()}
	p.pos++
	for p.pos < len(p.lines) {
		line := p.stripComment(p.lines[p.pos])
		toks := tokenize(line)
		if len(toks) == 0 {
			p.pos++
			continue
		}
		if toks[0] == "}" {
			p.pos++
			return d, nil
		}
		f, err := p.parseField(toks)
		if err != nil {
			return PGNDef{}, err
		}
		d.Fields = append(d.Fields, f)
		p.pos++
	}
	return PGNDef{}, p.errorf("unterminated pgn block")
}

func (p *parser) parseField(tokens []string) (FieldDef, error) {
	// <name> <type> :<bits> [attrs...]
	// _ :<bits>
	if len(tokens) < 2 {
		return FieldDef{}, p.errorf("field needs at least name and bit width")
	}

	f := FieldDef{Line: p.lineNum()}
	idx := 0

	// Name
	f.Name = tokens[idx]
	idx++

	// Type (optional for "_" reserved fields)
	if f.Name == "_" {
		f.Type = TypeReserved
		// Next token should be :<bits>
	} else {
		if idx >= len(tokens) {
			return FieldDef{}, p.errorf("field %s: missing type", f.Name)
		}
		typStr := tokens[idx]
		idx++
		f.Type, f.Signed, f.EnumRef = classifyType(typStr)
	}

	// Bit width :<N>
	if idx >= len(tokens) {
		return FieldDef{}, p.errorf("field %s: missing bit width", f.Name)
	}
	bitsStr := tokens[idx]
	if !strings.HasPrefix(bitsStr, ":") {
		return FieldDef{}, p.errorf("field %s: expected :<bits>, got %q", f.Name, bitsStr)
	}
	bits, err := strconv.Atoi(bitsStr[1:])
	if err != nil || bits <= 0 {
		return FieldDef{}, p.errorf("field %s: invalid bit width %q", f.Name, bitsStr)
	}
	f.Bits = bits
	idx++

	// Attributes
	for idx < len(tokens) {
		attr := tokens[idx]
		idx++
		k, v, ok := splitAttr(attr)
		if !ok {
			return FieldDef{}, p.errorf("field %s: invalid attribute %q", f.Name, attr)
		}
		switch k {
		case "scale":
			f.Scale, err = strconv.ParseFloat(v, 64)
			if err != nil {
				return FieldDef{}, p.errorf("field %s: invalid scale %q", f.Name, v)
			}
		case "offset":
			f.Offset, err = strconv.ParseFloat(v, 64)
			if err != nil {
				return FieldDef{}, p.errorf("field %s: invalid offset %q", f.Name, v)
			}
		case "unit":
			f.Unit = unquote(v)
		case "desc":
			f.Desc = unquote(v)
		case "value":
			if f.IsReserved() {
				return FieldDef{}, p.errorf("reserved field cannot have value= attribute")
			}
			val, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return FieldDef{}, p.errorf("field %s: invalid value %q", f.Name, v)
			}
			f.MatchValue = &val
		case "lookup":
			if f.IsReserved() {
				return FieldDef{}, p.errorf("reserved field cannot have lookup= attribute")
			}
			f.LookupRef = unquote(v)
		default:
			return FieldDef{}, p.errorf("field %s: unknown attribute %q", f.Name, k)
		}
	}

	// Validate string field bit width
	if f.Type == TypeString && f.Bits%8 != 0 {
		return FieldDef{}, p.errorf("field %s: string bit width must be multiple of 8", f.Name)
	}

	return f, nil
}

// classifyType maps a type token to FieldType, signedness, and optional enum reference.
func classifyType(s string) (FieldType, bool, string) {
	switch s {
	case "uint8", "uint16", "uint32", "uint64":
		return TypeUint, false, ""
	case "int8", "int16", "int32", "int64":
		return TypeInt, true, ""
	case "float32", "float64":
		return TypeFloat, false, ""
	case "string":
		return TypeString, false, ""
	default:
		// Assume it's an enum reference
		return TypeEnum, false, s
	}
}

func (p *parser) stripComment(line string) string {
	// Handle # comments, but not inside quoted strings
	inQuote := false
	for i, ch := range line {
		if ch == '"' {
			inQuote = !inQuote
		}
		if ch == '#' && !inQuote {
			return line[:i]
		}
	}
	return line
}

func (p *parser) lineNum() int {
	return p.pos + 1
}

func (p *parser) errorf(format string, args ...any) error {
	return fmt.Errorf("line %d: %s", p.lineNum(), fmt.Sprintf(format, args...))
}

// tokenize splits a line into tokens, respecting quoted strings.
func tokenize(line string) []string {
	var tokens []string
	line = strings.TrimSpace(line)
	for len(line) > 0 {
		if line[0] == '"' {
			// Quoted string: find closing quote
			end := strings.IndexByte(line[1:], '"')
			if end < 0 {
				tokens = append(tokens, line)
				break
			}
			tokens = append(tokens, line[:end+2])
			line = strings.TrimSpace(line[end+2:])
		} else {
			// Regular token: find next whitespace
			end := strings.IndexFunc(line, unicode.IsSpace)
			if end < 0 {
				tokens = append(tokens, line)
				break
			}
			tokens = append(tokens, line[:end])
			line = strings.TrimSpace(line[end:])
		}
	}
	return tokens
}

// splitAttr splits "key=value" into key and value.
func splitAttr(s string) (string, string, bool) {
	i := strings.IndexByte(s, '=')
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// unquote removes surrounding double quotes from a string.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
