// Package pgngen provides a DSL parser and code generators for NMEA 2000 PGN definitions.
//
// The DSL format defines PGN packet structures with bit-level field layouts,
// scaling factors, units, and enumerations. Generators produce Go structs with
// decoders/encoders, Protocol Buffer definitions, and JSON Schema.
package pgngen

import "time"

// Schema is the top-level AST node containing all parsed definitions.
type Schema struct {
	Enums   []EnumDef
	Lookups []LookupDef
	Structs []StructDef
	PGNs    []PGNDef
}

// StructDef defines a named sub-structure used by repeated variable-length groups.
// Analogous to EnumDef but for composite types referenced by struct-typed fields.
type StructDef struct {
	Name   string     // snake_case (e.g. "satellite_in_view")
	Fields []FieldDef // field layout within each entry
	Line   int        // source line for error reporting
}

// HasVariableWidth returns true if any field in the struct is variable-width
// (TypeStringLAU), making entry size unknowable at compile time.
func (sd *StructDef) HasVariableWidth() bool {
	for _, f := range sd.Fields {
		if f.Type == TypeStringLAU {
			return true
		}
	}
	return false
}

// FixedEntryBytes returns the total bytes per entry for fixed-width structs.
// Only valid when HasVariableWidth() returns false.
func (sd *StructDef) FixedEntryBytes() int {
	total := 0
	for _, f := range sd.Fields {
		total += f.Bits
	}
	return total / 8
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
//
// Fields semantics:
//   - nil:   name-only PGN (we know it exists but not its field layout)
//   - []:    PGN with zero user-visible fields (only reserved/unknown bits)
//   - [...]: PGN with a full field definition
type PGNDef struct {
	PGN         uint32
	Description string
	FastPacket  bool
	Interval    time.Duration // 0 = unspecified
	OnDemand    bool
	Draft       bool // definition is incomplete or uncertain
	Fields      []FieldDef
	Line        int // source line for error reporting
}

// IsNameOnly returns true if the PGN has no field layout defined.
func (p *PGNDef) IsNameOnly() bool {
	return p.Fields == nil
}

// HasVariableWidth returns true if any field is variable-length (TypeStringLAU
// or TypeStruct with dynamic repeat), making the packet size unknowable at compile time.
func (p *PGNDef) HasVariableWidth() bool {
	for _, f := range p.Fields {
		if f.Type == TypeStringLAU || f.Type == TypeStruct {
			return true
		}
	}
	return false
}

// TotalBits returns the total number of bits across all fields.
// Repeated fields contribute Bits * RepeatCount.
// Variable-width fields (TypeStringLAU, TypeStruct) contribute 0 bits.
func (p *PGNDef) TotalBits() int {
	total := 0
	for _, f := range p.Fields {
		if f.Type == TypeStringLAU || f.Type == TypeStruct {
			continue
		}
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
	StructRef   string    // struct type name (non-empty when Type == TypeStruct)
	RepeatRef   string    // dynamic repeat count field name (e.g. "sats_in_view")
	Trim        string    // characters to right-trim from decoded string (e.g. "@ ")
	Tolerance   *float64  // change detection tolerance (nil = not set, 0 = any change significant)
	Signed      bool      // true for int types
	MatchValue  *int64    // when set, this field must equal this value (dispatch discriminator)
	RepeatCount int       // number of repetitions (0 = not repeated, must be >= 2 when set)
	GroupMode   string    // "" (array, default) or "map" (map[int]T)
	AliasPlural string    // override for auto-pluralized field name (from as= attribute)
	Line        int       // source line for error reporting
}

// IsReserved returns true if this is a padding/reserved field.
func (f *FieldDef) IsReserved() bool {
	return f.Type == TypeReserved
}

// IsUnknown returns true if this is an unknown data field.
func (f *FieldDef) IsUnknown() bool {
	return f.Type == TypeUnknown
}

// IsSkipped returns true if this field should be excluded from generated structs.
// Both reserved padding and unknown data fields are skipped.
func (f *FieldDef) IsSkipped() bool {
	return f.IsReserved() || f.IsUnknown()
}

// IsRepeated returns true if this field uses a static repeat= count.
func (f *FieldDef) IsRepeated() bool {
	return f.RepeatCount > 0
}

// IsDynamicRepeat returns true if this field uses repeat=<field_ref> (runtime count).
func (f *FieldDef) IsDynamicRepeat() bool {
	return f.RepeatRef != ""
}

// HasScaling returns true if the field has a scale or offset transformation.
func (f *FieldDef) HasScaling() bool {
	return f.Scale != 0 || f.Offset != 0
}

// FieldType classifies the base data type of a field.
type FieldType int

const (
	TypeUint      FieldType = iota // unsigned integer
	TypeInt                        // signed integer
	TypeFloat                      // IEEE 754 float
	TypeString                     // fixed-length ASCII string
	TypeEnum                       // lookup enum (references an EnumDef)
	TypeReserved                   // reserved/padding bits
	TypeUnknown                    // unknown data (observed but undocumented)
	TypeStringLAU                  // variable-length NMEA 2000 STRING_LAU
	TypeStruct                     // reference to a StructDef (generates a slice)
)

// Resolve computes BitStart offsets for all fields in all PGNs and validates
// enum references, struct references, and dispatch groups. Call after parsing,
// before code generation.
func (s *Schema) Resolve() error {
	enumSet := make(map[string]bool, len(s.Enums))
	for _, e := range s.Enums {
		enumSet[e.Name] = true
	}

	lookupSet := make(map[string]bool, len(s.Lookups))
	for _, l := range s.Lookups {
		lookupSet[l.Name] = true
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

	structSet := make(map[string]bool, len(s.Structs))
	for _, sd := range s.Structs {
		if structSet[sd.Name] {
			return &ResolveError{
				Line:    sd.Line,
				Message: "duplicate struct name: " + sd.Name,
			}
		}
		if enumSet[sd.Name] {
			return &ResolveError{
				Line:    sd.Line,
				Message: "struct name conflicts with enum: " + sd.Name,
			}
		}
		structSet[sd.Name] = true
	}

	// Compute BitStart within each StructDef's fields (for fixed-width structs).
	for si := range s.Structs {
		offset := 0
		for fi := range s.Structs[si].Fields {
			s.Structs[si].Fields[fi].BitStart = offset
			f := &s.Structs[si].Fields[fi]
			offset += f.Bits // TypeStringLAU has Bits=0, ok for variable-width
		}
	}

	for i := range s.PGNs {
		if s.PGNs[i].IsNameOnly() {
			continue
		}

		// Reclassify fields: if the parser treated an unknown type name as TypeEnum
		// but it's actually a struct, fix it.
		for j := range s.PGNs[i].Fields {
			f := &s.PGNs[i].Fields[j]
			if f.Type == TypeEnum && structSet[f.EnumRef] {
				f.Type = TypeStruct
				f.StructRef = f.EnumRef
				f.EnumRef = ""
			}
		}

		// Build a field name lookup for RepeatRef validation.
		fieldNames := make(map[string]FieldDef)
		offset := 0
		for j := range s.PGNs[i].Fields {
			s.PGNs[i].Fields[j].BitStart = offset
			f := &s.PGNs[i].Fields[j]

			switch {
			case f.Type == TypeStringLAU || f.Type == TypeStruct:
				// Variable-width: contributes 0 to static offset
			case f.IsRepeated():
				offset += f.Bits * f.RepeatCount
			default:
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
			if f.Type == TypeStruct && !structSet[f.StructRef] {
				return &ResolveError{
					Line:    f.Line,
					Message: "unknown struct type: " + f.StructRef,
				}
			}

			// Validate struct-typed fields must have RepeatRef.
			if f.Type == TypeStruct && f.RepeatRef == "" {
				return &ResolveError{
					Line:    f.Line,
					Message: "field " + f.Name + ": struct-typed fields require repeat=<field_name>",
				}
			}

			// Validate RepeatRef references an existing uint field.
			if f.RepeatRef != "" {
				ref, ok := fieldNames[f.RepeatRef]
				if !ok {
					return &ResolveError{
						Line:    f.Line,
						Message: "field " + f.Name + ": repeat=" + f.RepeatRef + " references unknown field",
					}
				}
				if ref.Type != TypeUint {
					return &ResolveError{
						Line:    f.Line,
						Message: "field " + f.Name + ": repeat=" + f.RepeatRef + " must reference a uint field",
					}
				}
			}

			// Validate byte alignment before first variable-width field.
			if (f.Type == TypeStringLAU || f.Type == TypeStruct) && offset%8 != 0 {
				return &ResolveError{
					Line:    f.Line,
					Message: "field " + f.Name + ": variable-width field must be byte-aligned (cumulative bits = " + itoa(int64(offset)) + ")",
				}
			}

			if !f.IsSkipped() && f.Name != "" {
				fieldNames[f.Name] = *f
			}
		}
	}

	// Validate dispatch groups: PGNs sharing the same number.
	// Name-only PGNs are excluded from dispatch groups.
	groups := make(map[uint32][]int) // PGN number -> indices into s.PGNs
	for i, p := range s.PGNs {
		if p.IsNameOnly() {
			continue
		}
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

	// Validate all variants agree on PGN-level metadata.
	first := &s.PGNs[indices[0]]
	for _, idx := range indices[1:] {
		p := &s.PGNs[idx]
		if p.FastPacket != first.FastPacket {
			return &ResolveError{
				Line:    p.Line,
				Message: "PGN " + pgnStr + ": conflicting fast_packet metadata across variants",
			}
		}
		if p.Interval != first.Interval {
			return &ResolveError{
				Line:    p.Line,
				Message: "PGN " + pgnStr + ": conflicting interval metadata across variants",
			}
		}
		if p.OnDemand != first.OnDemand {
			return &ResolveError{
				Line:    p.Line,
				Message: "PGN " + pgnStr + ": conflicting on_demand metadata across variants",
			}
		}
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
