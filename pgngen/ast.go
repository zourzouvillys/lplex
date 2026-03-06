// Package pgngen provides a DSL parser and code generators for NMEA 2000 PGN definitions.
//
// The DSL format defines PGN packet structures with bit-level field layouts,
// scaling factors, units, and enumerations. Generators produce Go structs with
// decoders/encoders, Protocol Buffer definitions, and JSON Schema.
package pgngen

// Schema is the top-level AST node containing all parsed definitions.
type Schema struct {
	Enums   []EnumDef
	Lookups []LookupDef
	PGNs    []PGNDef
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

// LookupDef defines a named lookup table mapping integer keys to human-readable names.
// Unlike enums, lookups don't change the field's Go type; the field stays its raw integer type
// and gets a Name() method for human-readable display.
type LookupDef struct {
	Name    string // e.g. "VictronRegister"
	KeyType string // Go type: "uint8", "uint16", "uint32", "uint64"
	Values  []LookupValue
	Line    int // source line for error reporting
}

// LookupValue is a single entry in a lookup table.
type LookupValue struct {
	Key  int64
	Name string
}

// PGNDef defines a single PGN packet layout.
type PGNDef struct {
	PGN         uint32
	Description string
	Fields      []FieldDef
	Line        int // source line for error reporting
}

// TotalBits returns the total number of bits across all fields.
// Repeated fields contribute Bits * RepeatCount.
func (p *PGNDef) TotalBits() int {
	total := 0
	for _, f := range p.Fields {
		if f.IsRepeated() {
			total += f.Bits * f.RepeatCount
		} else {
			total += f.Bits
		}
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
	Name        string    // empty for reserved fields ("_")
	Type        FieldType // base type
	Bits        int       // bit width
	BitStart    int       // computed bit offset from start of packet (set by Resolve)
	Scale       float64   // raw * scale = decoded (0 means no scaling)
	Offset      float64   // decoded = raw * scale + offset (0 means no offset)
	Unit        string    // human-readable unit (e.g. "deg", "m/s")
	Desc        string    // optional description
	EnumRef     string    // enum type name (non-empty when Type == TypeEnum)
	LookupRef   string    // lookup table name (non-empty when lookup= is set)
	Signed      bool      // true for int types
	MatchValue  *int64    // when set, this field must equal this value (dispatch discriminator)
	RepeatCount int       // number of repetitions (0 = not repeated, must be >= 2 when set)
	GroupMode   string    // "" (array, default) or "map" (map[int]T)
	AliasPlural string    // override for auto-pluralized field name (from as= attribute)
	Line        int       // source line for error reporting
}

// IsReserved returns true if this is a padding/reserved field.
func (f *FieldDef) IsReserved() bool {
	return f.Name == "" || f.Name == "_"
}

// IsRepeated returns true if this field uses the repeat= attribute.
func (f *FieldDef) IsRepeated() bool {
	return f.RepeatCount > 0
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

	lookupSet := make(map[string]bool, len(s.Lookups))
	for _, l := range s.Lookups {
		lookupSet[l.Name] = true
		// Validate no duplicate keys within a lookup.
		seen := make(map[int64]bool, len(l.Values))
		for _, v := range l.Values {
			if seen[v.Key] {
				return &ResolveError{
					Line:    l.Line,
					Message: "lookup " + l.Name + ": duplicate key " + itoa(v.Key),
				}
			}
			seen[v.Key] = true
		}
	}

	for i := range s.PGNs {
		offset := 0
		for j := range s.PGNs[i].Fields {
			s.PGNs[i].Fields[j].BitStart = offset
			f := &s.PGNs[i].Fields[j]
			if f.IsRepeated() {
				offset += f.Bits * f.RepeatCount
			} else {
				offset += f.Bits
			}
			if f.Type == TypeEnum && !enumSet[f.EnumRef] {
				return &ResolveError{
					Line:    f.Line,
					Message: "unknown enum type: " + f.EnumRef,
				}
			}
			if f.LookupRef != "" && !lookupSet[f.LookupRef] {
				return &ResolveError{
					Line:    f.Line,
					Message: "unknown lookup type: " + f.LookupRef,
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
	pgnStr := itoa(int64(pgn))
	if !found {
		return &ResolveError{
			Message: "PGN " + pgnStr + ": multiple definitions require at least one variant with a value= constraint",
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
					Message: "PGN " + pgnStr + ": multiple default variants (without value= constraints)",
				}
			}
			continue
		}
		if d.Name != discName || d.BitStart != discBitStart || d.Bits != discBits {
			return &ResolveError{
				Line: d.Line,
				Message: "PGN " + pgnStr + ": discriminator field mismatch: " +
					d.Name + " at bit " + itoa(int64(d.BitStart)) + ":" + itoa(int64(d.Bits)) +
					" vs " + discName + " at bit " + itoa(int64(discBitStart)) + ":" + itoa(int64(discBits)),
			}
		}
		if prev, dup := matchValues[*d.MatchValue]; dup {
			return &ResolveError{
				Line: d.Line,
				Message: "PGN " + pgnStr + ": duplicate match value " + itoa(*d.MatchValue) +
					" (also at line " + itoa(int64(s.PGNs[prev].Line)) + ")",
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
		return "line " + itoa(int64(e.Line)) + ": " + e.Message
	}
	return e.Message
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	buf := [20]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
