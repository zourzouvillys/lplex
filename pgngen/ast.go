// Package pgngen provides a DSL parser and code generators for NMEA 2000 PGN definitions.
//
// The DSL format defines PGN packet structures with bit-level field layouts,
// scaling factors, units, and enumerations. Generators produce Go structs with
// decoders/encoders, Protocol Buffer definitions, and JSON Schema.
package pgngen

// Schema is the top-level AST node containing all parsed definitions.
type Schema struct {
	Enums []EnumDef
	PGNs  []PGNDef
}

// EnumDef defines a named enumeration used by lookup fields.
type EnumDef struct {
	Name   string
	Values []EnumValue
	Line   int // source line for error reporting
}

// EnumValue is a single value in an enumeration.
type EnumValue struct {
	Value int
	Name  string
}

// PGNDef defines a single PGN packet layout.
type PGNDef struct {
	PGN         uint32
	Description string
	Fields      []FieldDef
	Line        int // source line for error reporting
}

// TotalBits returns the total number of bits across all fields.
func (p *PGNDef) TotalBits() int {
	total := 0
	for _, f := range p.Fields {
		total += f.Bits
	}
	return total
}

// MinBytes returns the minimum number of bytes needed to decode this PGN.
func (p *PGNDef) MinBytes() int {
	bits := p.TotalBits()
	return (bits + 7) / 8
}

// FieldDef defines a single field within a PGN.
type FieldDef struct {
	Name       string    // empty for reserved fields ("_")
	Type       FieldType // base type
	Bits       int       // bit width
	BitStart   int       // computed bit offset from start of packet (set by Resolve)
	Scale      float64   // raw * scale = decoded (0 means no scaling)
	Offset     float64   // decoded = raw * scale + offset (0 means no offset)
	Unit       string    // human-readable unit (e.g. "deg", "m/s")
	Desc       string    // optional description
	EnumRef    string    // enum type name (non-empty when Type == TypeEnum)
	Signed     bool      // true for int types
	MatchValue *int64    // when set, this field must equal this value (dispatch discriminator)
	Line       int       // source line for error reporting
}

// IsReserved returns true if this is a padding/reserved field.
func (f *FieldDef) IsReserved() bool {
	return f.Name == "" || f.Name == "_"
}

// HasScaling returns true if the field has a scale or offset transformation.
func (f *FieldDef) HasScaling() bool {
	return f.Scale != 0 || f.Offset != 0
}

// FieldType classifies the base data type of a field.
type FieldType int

const (
	TypeUint     FieldType = iota // unsigned integer
	TypeInt                       // signed integer
	TypeFloat                     // IEEE 754 float
	TypeString                    // fixed-length ASCII string
	TypeEnum                      // lookup enum (references an EnumDef)
	TypeReserved                  // reserved/padding bits
)

// Resolve computes BitStart offsets for all fields in all PGNs and validates
// enum references and dispatch groups. Call after parsing, before code generation.
func (s *Schema) Resolve() error {
	enumSet := make(map[string]bool, len(s.Enums))
	for _, e := range s.Enums {
		enumSet[e.Name] = true
	}
	for i := range s.PGNs {
		offset := 0
		for j := range s.PGNs[i].Fields {
			s.PGNs[i].Fields[j].BitStart = offset
			offset += s.PGNs[i].Fields[j].Bits
			f := &s.PGNs[i].Fields[j]
			if f.Type == TypeEnum && !enumSet[f.EnumRef] {
				return &ResolveError{
					Line:    f.Line,
					Message: "unknown enum type: " + f.EnumRef,
				}
			}
		}
	}

	// Validate dispatch groups: PGNs sharing the same number.
	groups := make(map[uint32][]int) // PGN number -> indices into s.PGNs
	for i, p := range s.PGNs {
		groups[p.PGN] = append(groups[p.PGN], i)
	}
	for pgn, indices := range groups {
		if len(indices) < 2 {
			continue
		}
		if err := validateDispatchGroup(s, pgn, indices); err != nil {
			return err
		}
	}
	return nil
}

// discriminatorField returns the first field with a MatchValue constraint, or nil.
func discriminatorField(p *PGNDef) *FieldDef {
	for i := range p.Fields {
		if p.Fields[i].MatchValue != nil {
			return &p.Fields[i]
		}
	}
	return nil
}

// hasMatchValues returns true if any field in the PGN has a MatchValue constraint.
func hasMatchValues(p *PGNDef) bool {
	return discriminatorField(p) != nil
}

func validateDispatchGroup(s *Schema, pgn uint32, indices []int) error {
	// Find the discriminator from the first constrained variant.
	var discName string
	var discBitStart, discBits int
	found := false
	for _, idx := range indices {
		if d := discriminatorField(&s.PGNs[idx]); d != nil {
			discName = d.Name
			discBitStart = d.BitStart
			discBits = d.Bits
			found = true
			break
		}
	}
	if !found {
		return &ResolveError{
			Message: "PGN " + itoa(int(pgn)) + ": multiple definitions require at least one variant with a value= constraint",
		}
	}

	// Validate all constrained variants use the same discriminator field.
	defaultCount := 0
	matchValues := make(map[int64]int) // match value -> PGN index (for duplicate detection)
	for _, idx := range indices {
		p := &s.PGNs[idx]
		d := discriminatorField(p)
		if d == nil {
			defaultCount++
			if defaultCount > 1 {
				return &ResolveError{
					Line:    p.Line,
					Message: "PGN " + itoa(int(pgn)) + ": multiple default variants (without value= constraints)",
				}
			}
			continue
		}
		if d.Name != discName || d.BitStart != discBitStart || d.Bits != discBits {
			return &ResolveError{
				Line: d.Line,
				Message: "PGN " + itoa(int(pgn)) + ": discriminator field mismatch: " +
					d.Name + " at bit " + itoa(d.BitStart) + ":" + itoa(d.Bits) +
					" vs " + discName + " at bit " + itoa(discBitStart) + ":" + itoa(discBits),
			}
		}
		if prev, dup := matchValues[*d.MatchValue]; dup {
			return &ResolveError{
				Line: d.Line,
				Message: "PGN " + itoa(int(pgn)) + ": duplicate match value " + itoa(int(*d.MatchValue)) +
					" (also at line " + itoa(s.PGNs[prev].Line) + ")",
			}
		}
		matchValues[*d.MatchValue] = idx
	}

	return nil
}

// ResolveError reports a validation error during schema resolution.
type ResolveError struct {
	Line    int
	Message string
}

func (e *ResolveError) Error() string {
	if e.Line > 0 {
		return "line " + itoa(e.Line) + ": " + e.Message
	}
	return e.Message
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
