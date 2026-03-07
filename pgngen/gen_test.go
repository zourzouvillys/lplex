package pgngen

import (
	"strings"
	"testing"
)

func TestGenerateGoCompiles(t *testing.T) {
	src := `
enum Ref {
  0 = "true"
  1 = "magnetic"
}

pgn 129025 "Position Rapid Update" {
  latitude   int32  :32  scale=1e-7  unit="deg"
  longitude  int32  :32  scale=1e-7  unit="deg"
}

pgn 127250 "Vessel Heading" {
  sid               uint8  :8
  heading           uint16 :16  scale=0.0001  unit="rad"
  deviation         int16  :16  scale=0.0001  unit="rad"
  variation         int16  :16  scale=0.0001  unit="rad"
  heading_reference Ref    :2
  _                        :6
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	code := GenerateGo(s, "pgn")

	// Check it contains expected elements
	if !strings.Contains(code, "type PositionRapidUpdate struct") {
		t.Error("missing PositionRapidUpdate struct")
	}
	if !strings.Contains(code, "func DecodePositionRapidUpdate") {
		t.Error("missing DecodePositionRapidUpdate function")
	}
	if !strings.Contains(code, "func (m *PositionRapidUpdate) Encode()") {
		t.Error("missing Encode method")
	}
	if !strings.Contains(code, "type Ref uint8") {
		t.Error("missing Ref enum type")
	}
	if !strings.Contains(code, "RefTrue Ref = 0") {
		t.Error("missing RefTrue constant")
	}
	if !strings.Contains(code, "var Registry = map[uint32]PGNInfo") {
		t.Error("missing Registry")
	}
	if !strings.Contains(code, "129025:") {
		t.Error("missing PGN 129025 in registry")
	}
}

func TestGenerateProto(t *testing.T) {
	src := `
enum WindReference {
  0 = "true_north"
  1 = "apparent"
}

pgn 130306 "Wind Data" {
  sid            uint8          :8
  wind_speed     uint16         :16  scale=0.01 unit="m/s"
  wind_reference WindReference  :3
  _                             :5
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	proto := GenerateProto(s, "pgn.v1")

	if !strings.Contains(proto, `syntax = "proto3"`) {
		t.Error("missing proto3 syntax")
	}
	if !strings.Contains(proto, "enum WindReference") {
		t.Error("missing WindReference enum")
	}
	if !strings.Contains(proto, "message WindData") {
		t.Error("missing WindData message")
	}
	if !strings.Contains(proto, "double wind_speed") {
		t.Error("wind_speed should be double (has scale)")
	}
	if !strings.Contains(proto, "WindReference wind_reference") {
		t.Error("missing wind_reference field")
	}
	if !strings.Contains(proto, "message DecodedPGN") {
		t.Error("missing DecodedPGN wrapper")
	}
}

func TestGenerateJSONSchema(t *testing.T) {
	src := `
pgn 129025 "Position Rapid Update" {
  latitude   int32  :32  scale=1e-7  unit="deg"
  longitude  int32  :32  scale=1e-7  unit="deg"
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	js := GenerateJSONSchema(s)

	if !strings.Contains(js, `"$schema"`) {
		t.Error("missing $schema")
	}
	if !strings.Contains(js, `"PositionRapidUpdate"`) {
		t.Error("missing PositionRapidUpdate definition")
	}
	if !strings.Contains(js, `"latitude"`) {
		t.Error("missing latitude property")
	}
	if !strings.Contains(js, `"number"`) {
		t.Error("latitude should be number type")
	}
}

func TestMinBufferBytesNonStandardWidth(t *testing.T) {
	// A byte-aligned 24-bit uint32 reads via Uint32 (4 bytes).
	// MinBytes (totalBits/8) would give 3, but the buffer must be 4.
	src := `
pgn 59904 "ISO Request" {
  requested_pgn  uint32  :24
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	got := minBufferBytes(s.PGNs[0])
	if got != 4 {
		t.Errorf("minBufferBytes = %d, want 4", got)
	}

	// Verify generated code uses the correct buffer size.
	code := GenerateGo(s, "pgn")
	if !strings.Contains(code, "if len(data) < 4") {
		t.Error("generated decode should pad to 4 bytes, not 3")
	}
}

func TestGenerateGoDispatch(t *testing.T) {
	src := `
pgn 61184 "Victron Battery Register" {
  manufacturer_code  uint16  :11  value=358
  _                          :2
  industry_code      uint8   :3
  register_id        uint16  :16
  payload            uint32  :32
}

pgn 61184 "Proprietary Single Frame" {
  manufacturer_code  uint16  :11
  _                          :2
  industry_code      uint8   :3
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	code := GenerateGo(s, "pgn")

	// Both variant structs should exist.
	if !strings.Contains(code, "type VictronBatteryRegister struct") {
		t.Error("missing VictronBatteryRegister struct")
	}
	if !strings.Contains(code, "type ProprietarySingleFrame struct") {
		t.Error("missing ProprietarySingleFrame struct")
	}

	// Both variant decoders should exist.
	if !strings.Contains(code, "func DecodeVictronBatteryRegister(data []byte)") {
		t.Error("missing DecodeVictronBatteryRegister")
	}
	if !strings.Contains(code, "func DecodeProprietarySingleFrame(data []byte)") {
		t.Error("missing DecodeProprietarySingleFrame")
	}

	// Dispatch function should exist.
	if !strings.Contains(code, "func Decode61184(data []byte) (any, error)") {
		t.Error("missing Decode61184 dispatch function")
	}

	// Dispatch should switch on manufacturer_code value.
	if !strings.Contains(code, "case 358:") {
		t.Error("missing case 358 in dispatch")
	}

	// Default branch should call the fallback variant.
	if !strings.Contains(code, "return DecodeProprietarySingleFrame(data)") {
		t.Error("default case should call DecodeProprietarySingleFrame")
	}

	// Registry should have a single entry for PGN 61184 using the dispatch function.
	if !strings.Contains(code, "61184: {PGN: 61184") {
		t.Error("missing PGN 61184 in registry")
	}
	if !strings.Contains(code, "Decode: Decode61184") {
		t.Error("registry should use Decode61184 dispatch function")
	}
	// Registry should use the default variant's description.
	if !strings.Contains(code, `Description: "Proprietary Single Frame"`) {
		t.Error("registry description should be from the default (fallback) variant")
	}

	// Constrained encode: Victron Encode should hardcode manufacturer_code=358.
	// The encode should use the literal value, not m.ManufacturerCode.
	encodeStart := strings.Index(code, "func (m *VictronBatteryRegister) Encode()")
	if encodeStart < 0 {
		t.Fatal("missing VictronBatteryRegister.Encode")
	}
	encodeEnd := strings.Index(code[encodeStart:], "\n}\n")
	encodeBody := code[encodeStart : encodeStart+encodeEnd]
	if !strings.Contains(encodeBody, "uint64(358)") {
		t.Error("VictronBatteryRegister.Encode should hardcode manufacturer_code=358")
	}
	if strings.Contains(encodeBody, "m.ManufacturerCode") {
		t.Error("VictronBatteryRegister.Encode should not read m.ManufacturerCode for constrained field")
	}
}

func TestGenerateGoDispatchNoDefault(t *testing.T) {
	src := `
pgn 61184 "Victron Register" {
  manufacturer_code  uint16  :11  value=358
  _                          :2
  industry_code      uint8   :3
  register_id        uint16  :16
}

pgn 61184 "Garmin Register" {
  manufacturer_code  uint16  :11  value=229
  _                          :2
  industry_code      uint8   :3
  data               uint32  :32
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	code := GenerateGo(s, "pgn")

	// Without a default variant, unknown discriminator values should return (nil, nil).
	if !strings.Contains(code, "return nil, nil") {
		t.Error("dispatch without default should return (nil, nil) for unknown discriminator")
	}

	// Should have both switch cases.
	if !strings.Contains(code, "case 358:") {
		t.Error("missing case 358")
	}
	if !strings.Contains(code, "case 229:") {
		t.Error("missing case 229")
	}
}

func TestGenerateGoDispatchSingleVariant(t *testing.T) {
	// A single PGN with value= should still get a dispatch function
	// that rejects non-matching discriminator values.
	src := `
pgn 61184 "Victron Battery Register" {
  manufacturer_code  uint16  :11  value=358
  _                          :2
  industry_code      uint8   :3
  register_id        uint16  :16
  payload            uint32  :32
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	code := GenerateGo(s, "pgn")

	// Should generate the variant struct and decoder.
	if !strings.Contains(code, "type VictronBatteryRegister struct") {
		t.Error("missing VictronBatteryRegister struct")
	}

	// Should generate a dispatch function despite being a single variant.
	if !strings.Contains(code, "func Decode61184(data []byte) (any, error)") {
		t.Error("missing Decode61184 dispatch function for single constrained variant")
	}

	// Unknown discriminator values should return (nil, nil).
	if !strings.Contains(code, "return nil, nil") {
		t.Error("single-variant dispatch should return (nil, nil) for unknown discriminator")
	}

	// Registry should use the dispatch function.
	if !strings.Contains(code, "Decode: Decode61184") {
		t.Error("registry should use Decode61184 dispatch function")
	}
}

func TestDispatchValidation(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		wantErr string
	}{
		{
			name: "conflicting discriminator position",
			src: `
pgn 61184 "Variant A" {
  manufacturer_code  uint16  :11  value=358
  _                          :5
}

pgn 61184 "Variant B" {
  _                          :3
  manufacturer_code  uint16  :11  value=229
  _                          :2
}
`,
			wantErr: "discriminator field mismatch",
		},
		{
			name: "duplicate match value",
			src: `
pgn 61184 "Variant A" {
  manufacturer_code  uint16  :11  value=358
  _                          :5
}

pgn 61184 "Variant B" {
  manufacturer_code  uint16  :11  value=358
  _                          :5
}
`,
			wantErr: "duplicate match value 358",
		},
		{
			name: "multiple defaults",
			src: `
pgn 61184 "Constrained" {
  manufacturer_code  uint16  :11  value=358
  _                          :5
}

pgn 61184 "Default A" {
  manufacturer_code  uint16  :11
  _                          :5
}

pgn 61184 "Default B" {
  manufacturer_code  uint16  :11
  _                          :5
}
`,
			wantErr: "multiple default variants",
		},
		{
			name: "no constrained variants",
			src: `
pgn 61184 "Only Default" {
  manufacturer_code  uint16  :11
  _                          :5
}

pgn 61184 "Also Default" {
  manufacturer_code  uint16  :11
  _                          :5
}
`,
			wantErr: "at least one variant with a value= constraint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Parse(tt.src)
			if err != nil {
				t.Fatal(err)
			}
			err = s.Resolve()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestParseValueAttribute(t *testing.T) {
	src := `
pgn 61184 "Test" {
  manufacturer_code  uint16  :11  value=358
  _                          :5
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	f := s.PGNs[0].Fields[0]
	if f.MatchValue == nil {
		t.Fatal("MatchValue should be set")
	}
	if *f.MatchValue != 358 {
		t.Errorf("MatchValue = %d, want 358", *f.MatchValue)
	}
}

func TestParseValueOnReservedField(t *testing.T) {
	src := `
pgn 61184 "Test" {
  _  :8  value=5
}
`
	_, err := Parse(src)
	if err == nil {
		t.Fatal("expected error for value= on reserved field")
	}
	if !strings.Contains(err.Error(), "cannot have value=") {
		t.Errorf("error = %q, want substring about value= restriction", err.Error())
	}
}

func TestParseLookup(t *testing.T) {
	src := `
lookup VictronRegister uint16 {
  0x0100 = "Product ID"
  0x0200 = "Device Mode"
  255    = "Decimal Key"
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Lookups) != 1 {
		t.Fatalf("got %d lookups, want 1", len(s.Lookups))
	}
	l := s.Lookups[0]
	if l.Name != "VictronRegister" {
		t.Errorf("name = %q, want VictronRegister", l.Name)
	}
	if l.KeyType != "uint16" {
		t.Errorf("key type = %q, want uint16", l.KeyType)
	}
	if len(l.Values) != 3 {
		t.Fatalf("got %d values, want 3", len(l.Values))
	}
	if l.Values[0].Key != 0x0100 {
		t.Errorf("first key = %d, want %d", l.Values[0].Key, 0x0100)
	}
	if l.Values[0].Name != "Product ID" {
		t.Errorf("first name = %q, want Product ID", l.Values[0].Name)
	}
	if l.Values[2].Key != 255 {
		t.Errorf("third key = %d, want 255", l.Values[2].Key)
	}
}

func TestParseLookupOnReservedField(t *testing.T) {
	src := `
lookup Foo uint8 {
  1 = "one"
}

pgn 99999 "Test" {
  _  :8  lookup=Foo
}
`
	_, err := Parse(src)
	if err == nil {
		t.Fatal("expected error for lookup= on reserved field")
	}
	if !strings.Contains(err.Error(), "cannot have lookup=") {
		t.Errorf("error = %q, want substring about lookup= restriction", err.Error())
	}
}

func TestParseLookupUnknownRef(t *testing.T) {
	src := `
pgn 99999 "Test" {
  register_id  uint16  :16  lookup=NonExistent
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Resolve()
	if err == nil {
		t.Fatal("expected error for unknown lookup reference")
	}
	if !strings.Contains(err.Error(), "unknown lookup type: NonExistent") {
		t.Errorf("error = %q, want substring about unknown lookup", err.Error())
	}
}

func TestParseLookupDuplicateKey(t *testing.T) {
	src := `
lookup Dup uint16 {
  0x01 = "First"
  0x01 = "Second"
}

pgn 99999 "Test" {
  x  uint16  :16  lookup=Dup
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Resolve()
	if err == nil {
		t.Fatal("expected error for duplicate lookup key")
	}
	if !strings.Contains(err.Error(), "duplicate key") {
		t.Errorf("error = %q, want substring about duplicate key", err.Error())
	}
}

func TestGenerateGoLookup(t *testing.T) {
	src := `
lookup StatusCode uint8 {
  0x01 = "Active"
  0x02 = "Standby"
}

pgn 99999 "Test Device" {
  status  uint8  :8  lookup=StatusCode
  value   uint16 :16
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	code := GenerateGo(s, "pgn")

	// Lookup map should exist.
	if !strings.Contains(code, "var statusCodeNames = map[uint8]string{") {
		t.Error("missing statusCodeNames map")
	}
	if !strings.Contains(code, `0x1: "Active"`) {
		t.Error("missing Active entry in lookup map")
	}
	if !strings.Contains(code, `0x2: "Standby"`) {
		t.Error("missing Standby entry in lookup map")
	}

	// Method should exist on the struct.
	if !strings.Contains(code, "func (m TestDevice) StatusName() string {") {
		t.Error("missing StatusName method")
	}
	if !strings.Contains(code, "return statusCodeNames[m.Status]") {
		t.Error("StatusName method should index into statusCodeNames")
	}

	// LookupFields aggregator should exist.
	if !strings.Contains(code, "func (m TestDevice) LookupFields() map[string]string {") {
		t.Error("missing LookupFields method")
	}
	if !strings.Contains(code, `"status": statusCodeNames[m.Status]`) {
		t.Error("LookupFields should map status to its lookup table")
	}
}

func TestGenerateGoLookupNoRef(t *testing.T) {
	src := `
lookup OrphanLookup uint16 {
  0x01 = "One"
}

pgn 99999 "Test Device" {
  value  uint16  :16
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	code := GenerateGo(s, "pgn")

	// Lookup map should still be generated even without a field reference.
	if !strings.Contains(code, "var orphanLookupNames = map[uint16]string{") {
		t.Error("missing orphanLookupNames map (unreferenced lookup should still be generated)")
	}

	// No method should be generated since no field references it.
	if strings.Contains(code, "Name() string") {
		t.Error("unexpected Name method for unreferenced lookup")
	}
	if strings.Contains(code, "LookupFields()") {
		t.Error("unexpected LookupFields method for struct with no lookup fields")
	}
}

func TestParseLookupInvalidKeyType(t *testing.T) {
	src := `
lookup Bad string {
  1 = "one"
}
`
	_, err := Parse(src)
	if err == nil {
		t.Fatal("expected error for invalid lookup key type")
	}
	if !strings.Contains(err.Error(), "invalid lookup key type") {
		t.Errorf("error = %q, want substring about invalid key type", err.Error())
	}
}

func TestPlural(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"indicator", "indicators"},
		{"status", "statuses"},
		{"switch", "switches"},
		{"bus", "buses"},
		{"box", "boxes"},
		{"buzz", "buzzes"},
		{"flash", "flashes"},
		{"category", "categories"},
		{"key", "keys"},       // vowel+y -> just +s
		{"day", "days"},       // vowel+y -> just +s
		{"entry", "entries"},  // consonant+y -> ies
		{"relay", "relays"},   // vowel+y -> just +s
		{"value", "values"},
	}
	for _, tt := range tests {
		if got := toPlural(tt.input); got != tt.want {
			t.Errorf("toPlural(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseRepeat(t *testing.T) {
	src := `
pgn 127501 "Binary Switch Bank Status" {
  instance    uint8   :8
  indicator   uint8   :2  repeat=28
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	f := s.PGNs[0].Fields[1]
	if f.RepeatCount != 28 {
		t.Errorf("RepeatCount = %d, want 28", f.RepeatCount)
	}
	if f.GroupMode != "" {
		t.Errorf("GroupMode = %q, want empty", f.GroupMode)
	}
	if f.AliasPlural != "" {
		t.Errorf("AliasPlural = %q, want empty", f.AliasPlural)
	}
}

func TestParseRepeatWithGroup(t *testing.T) {
	src := `
pgn 127501 "Test" {
  instance    uint8   :8
  indicator   uint8   :2  repeat=28  group="map"
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	f := s.PGNs[0].Fields[1]
	if f.RepeatCount != 28 {
		t.Errorf("RepeatCount = %d, want 28", f.RepeatCount)
	}
	if f.GroupMode != "map" {
		t.Errorf("GroupMode = %q, want map", f.GroupMode)
	}
}

func TestParseRepeatWithAs(t *testing.T) {
	src := `
pgn 127501 "Test" {
  instance    uint8   :8
  indicator   uint8   :2  repeat=28  as="switches"
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	f := s.PGNs[0].Fields[1]
	if f.AliasPlural != "switches" {
		t.Errorf("AliasPlural = %q, want switches", f.AliasPlural)
	}
}

func TestParseRepeatErrors(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		wantErr string
	}{
		{
			name: "repeat on reserved field",
			src: `
pgn 99999 "Test" {
  _  :2  repeat=4
}
`,
			wantErr: "cannot have repeat=",
		},
		{
			name: "repeat=1",
			src: `
pgn 99999 "Test" {
  x  uint8  :2  repeat=1
}
`,
			wantErr: "repeat= must be an integer >= 2",
		},
		{
			name: "repeat=0",
			src: `
pgn 99999 "Test" {
  x  uint8  :2  repeat=0
}
`,
			wantErr: "repeat= must be an integer >= 2",
		},
		{
			name: "repeat=foo",
			src: `
pgn 99999 "Test" {
  x  uint8  :2  repeat=foo
}
`,
			wantErr: "repeat= must be an integer >= 2",
		},
		{
			name: "group without repeat",
			src: `
pgn 99999 "Test" {
  x  uint8  :2  group="map"
}
`,
			wantErr: "group= requires repeat=",
		},
		{
			name: "as without repeat",
			src: `
pgn 99999 "Test" {
  x  uint8  :2  as="things"
}
`,
			wantErr: "as= requires repeat=",
		},
		{
			name: "invalid group value",
			src: `
pgn 99999 "Test" {
  x  uint8  :2  repeat=4  group="list"
}
`,
			wantErr: `group= must be "map"`,
		},
		{
			name: "repeat with value=",
			src: `
pgn 99999 "Test" {
  x  uint8  :2  repeat=4  value=1
}
`,
			wantErr: "repeat= cannot be combined with value=",
		},
		{
			name: "repeat with lookup=",
			src: `
lookup Foo uint8 {
  1 = "one"
}

pgn 99999 "Test" {
  x  uint8  :2  repeat=4  lookup=Foo
}
`,
			wantErr: "repeat= cannot be combined with lookup=",
		},
		{
			name: "repeat with enum type",
			src: `
enum Status {
  0 = "off"
  1 = "on"
}

pgn 99999 "Test" {
  x  Status  :2  repeat=4
}
`,
			wantErr: "repeat= cannot be used with enum types",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.src)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestRepeatBitOffsets(t *testing.T) {
	src := `
pgn 99999 "Test" {
  instance    uint8   :8
  indicator   uint8   :2  repeat=4
  trailer     uint8   :8
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	// indicator starts at bit 8
	if got := s.PGNs[0].Fields[1].BitStart; got != 8 {
		t.Errorf("indicator BitStart = %d, want 8", got)
	}
	// trailer should start at bit 8 + 4*2 = 16
	if got := s.PGNs[0].Fields[2].BitStart; got != 16 {
		t.Errorf("trailer BitStart = %d, want 16", got)
	}
	// TotalBits: 8 + 4*2 + 8 = 24
	if got := s.PGNs[0].TotalBits(); got != 24 {
		t.Errorf("TotalBits = %d, want 24", got)
	}
}

func TestGenerateGoRepeatArray(t *testing.T) {
	src := `
pgn 127501 "Binary Switch Bank Status" {
  instance    uint8   :8
  indicator   uint8   :2  repeat=4
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	code := GenerateGo(s, "pgn")

	// Struct should have a Uint8s field (not []uint8 which json base64-encodes).
	if !strings.Contains(code, "Indicators Uint8s") {
		t.Error("missing Indicators Uint8s field")
	}
	if strings.Contains(code, "Indicator1") {
		t.Error("should not have individual Indicator1 field")
	}

	// JSON tag should be pluralized.
	if !strings.Contains(code, `json:"indicators"`) {
		t.Error("missing indicators json tag")
	}

	// Decode should create a Uint8s literal.
	if !strings.Contains(code, "m.Indicators = Uint8s{") {
		t.Error("missing Uint8s literal in decode")
	}

	// Encode should have bounds-checked writes.
	if !strings.Contains(code, "if len(m.Indicators) > 0") {
		t.Error("missing bounds check for Indicators[0]")
	}
	if !strings.Contains(code, "if len(m.Indicators) > 3") {
		t.Error("missing bounds check for Indicators[3]")
	}
}

func TestGenerateGoRepeatMap(t *testing.T) {
	src := `
pgn 127501 "Test" {
  instance    uint8   :8
  indicator   uint8   :2  repeat=4  group="map"
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	code := GenerateGo(s, "pgn")

	// Struct should have a map field.
	if !strings.Contains(code, "Indicators map[int]uint8") {
		t.Error("missing Indicators map[int]uint8 field")
	}

	// Decode should create a map literal with 1-based keys.
	if !strings.Contains(code, "m.Indicators = map[int]uint8{") {
		t.Error("missing map literal in decode")
	}
	if !strings.Contains(code, "1:") {
		t.Error("missing key 1 in map literal")
	}
	if !strings.Contains(code, "4:") {
		t.Error("missing key 4 in map literal")
	}

	// Encode should use map lookups.
	if !strings.Contains(code, "if v, ok := m.Indicators[1]; ok") {
		t.Error("missing map lookup for key 1 in encode")
	}
}

func TestGenerateGoRepeatWithAs(t *testing.T) {
	src := `
pgn 127501 "Test" {
  instance    uint8   :8
  indicator   uint8   :2  repeat=4  as="switches"
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	code := GenerateGo(s, "pgn")

	if !strings.Contains(code, "Switches Uint8s") {
		t.Error("missing Switches Uint8s field (as= override)")
	}
	if !strings.Contains(code, `json:"switches"`) {
		t.Error("missing switches json tag (as= override)")
	}
}

func TestGenerateProtoRepeat(t *testing.T) {
	src := `
pgn 127501 "Binary Switch Bank Status" {
  instance    uint8   :8
  indicator   uint8   :2  repeat=4
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	proto := GenerateProto(s, "pgn.v1")

	if !strings.Contains(proto, "repeated uint32 indicators") {
		t.Error("missing repeated uint32 indicators in proto")
	}
}

func TestGenerateJSONSchemaRepeatArray(t *testing.T) {
	src := `
pgn 127501 "Binary Switch Bank Status" {
  instance    uint8   :8
  indicator   uint8   :2  repeat=4
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	js := GenerateJSONSchema(s)

	if !strings.Contains(js, `"indicators"`) {
		t.Error("missing indicators property")
	}
	if !strings.Contains(js, `"array"`) {
		t.Error("missing array type")
	}
	if !strings.Contains(js, `"minItems": 4`) {
		t.Error("missing minItems: 4")
	}
	if !strings.Contains(js, `"maxItems": 4`) {
		t.Error("missing maxItems: 4")
	}
}

func TestGenerateJSONSchemaRepeatMap(t *testing.T) {
	src := `
pgn 127501 "Test" {
  instance    uint8   :8
  indicator   uint8   :2  repeat=4  group="map"
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	js := GenerateJSONSchema(s)

	if !strings.Contains(js, `"indicators"`) {
		t.Error("missing indicators property")
	}
	if !strings.Contains(js, `"additionalProperties"`) {
		t.Error("missing additionalProperties for map mode")
	}
}

func TestGenerateGoMetadata(t *testing.T) {
	src := `
pgn 129029 "GNSS Position Data" fast_packet interval=1000ms {
  sid  uint8  :8
}

pgn 59904 "ISO Request" on_demand {
  requested_pgn  uint32  :24
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	code := GenerateGo(s, "pgn")

	// PGNInfo struct should have metadata fields.
	if !strings.Contains(code, "FastPacket  bool") {
		t.Error("missing FastPacket field in PGNInfo")
	}
	if !strings.Contains(code, "Interval    time.Duration") {
		t.Error("missing Interval field in PGNInfo")
	}
	if !strings.Contains(code, "OnDemand    bool") {
		t.Error("missing OnDemand field in PGNInfo")
	}

	// Registry entries should include metadata.
	if !strings.Contains(code, "FastPacket: true") {
		t.Error("missing FastPacket: true in registry entry for 129029")
	}
	if !strings.Contains(code, "Interval: 1000 * time.Millisecond") {
		t.Error("missing Interval in registry entry for 129029")
	}
	if !strings.Contains(code, "OnDemand: true") {
		t.Error("missing OnDemand: true in registry entry for 59904")
	}

	// time import should be present.
	if !strings.Contains(code, `"time"`) {
		t.Error("missing time import")
	}
}

func TestGenerateGoNameOnly(t *testing.T) {
	src := `
pgn 129038 "AIS Class A Position Report" fast_packet
pgn 129039 "AIS Class B Position Report" fast_packet

pgn 129025 "Position Rapid Update" {
  latitude   int32  :32  scale=1e-7  unit="deg"
  longitude  int32  :32  scale=1e-7  unit="deg"
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	code := GenerateGo(s, "pgn")

	// Name-only PGNs should NOT get structs or decode functions.
	if strings.Contains(code, "type AISClassAPositionReport struct") {
		t.Error("name-only PGN should not have a struct")
	}
	if strings.Contains(code, "func DecodeAISClassAPositionReport") {
		t.Error("name-only PGN should not have a decode function")
	}

	// But they SHOULD appear in the Registry with nil Decode.
	if !strings.Contains(code, `129038: {PGN: 129038, Description: "AIS Class A Position Report"`) {
		t.Error("name-only PGN 129038 missing from Registry")
	}
	if !strings.Contains(code, `129039: {PGN: 129039, Description: "AIS Class B Position Report"`) {
		t.Error("name-only PGN 129039 missing from Registry")
	}
	// Verify name-only entries have FastPacket set.
	if !strings.Contains(code, `129038: {PGN: 129038, Description: "AIS Class A Position Report", FastPacket: true,`) {
		t.Error("name-only PGN 129038 should have FastPacket: true")
	}

	// Field-defined PGN should still work normally.
	if !strings.Contains(code, "type PositionRapidUpdate struct") {
		t.Error("field-defined PGN should have a struct")
	}
	if !strings.Contains(code, "func DecodePositionRapidUpdate") {
		t.Error("field-defined PGN should have a decode function")
	}
}

func TestGenerateGoDraft(t *testing.T) {
	src := `
pgn 127493 "Transmission Parameters Dynamic" draft {
  engine_instance  uint8  :8
  gear             uint8  :2
  _                        :6
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	code := GenerateGo(s, "pgn")

	if !strings.Contains(code, "Draft       bool") {
		t.Error("missing Draft field in PGNInfo")
	}
	if !strings.Contains(code, "Draft: true") {
		t.Error("Draft: true missing from registry entry")
	}
}

func TestGenerateGoUnknownField(t *testing.T) {
	src := `
pgn 127493 "Test PGN" {
  engine_instance  uint8   :8
  ?                         :32
  known_field      uint16  :16
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	code := GenerateGo(s, "pgn")

	// Struct should have engine_instance and known_field but not the unknown.
	if !strings.Contains(code, "EngineInstance uint8") {
		t.Error("missing EngineInstance field")
	}
	if !strings.Contains(code, "KnownField uint16") {
		t.Error("missing KnownField field")
	}
	// The unknown field should be skipped from the struct entirely.
	// The struct should only have 2 fields.
	structStart := strings.Index(code, "type TestPGN struct")
	if structStart < 0 {
		t.Fatal("missing TestPGN struct")
	}
	structEnd := strings.Index(code[structStart:], "\n}\n")
	structBody := code[structStart : structStart+structEnd]
	if strings.Count(structBody, "\t") > 3 { // type line + 2 fields
		// Just verify the unknown bits don't generate a struct field named "?"
		if strings.Contains(structBody, "? ") {
			t.Error("unknown field should not appear in struct")
		}
	}
}

func TestGenerateGoNameOnlyNilDecode(t *testing.T) {
	// Verify that calling Registry[name-only-pgn].Decode would be nil.
	src := `pgn 59392 "ISO Ack"`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	code := GenerateGo(s, "pgn")

	// Registry entry should NOT have a Decode field (defaults to nil).
	entry := `59392: {PGN: 59392, Description: "ISO Ack", },`
	if !strings.Contains(code, entry) {
		// Find what it actually generated
		idx := strings.Index(code, "59392:")
		if idx < 0 {
			t.Fatal("PGN 59392 not in registry at all")
		}
		lineEnd := strings.Index(code[idx:], "\n")
		t.Errorf("registry line = %q, want %q", code[idx:idx+lineEnd], entry)
	}
}

func TestGenerateProtoNameOnly(t *testing.T) {
	src := `
pgn 129038 "AIS Class A Position Report" fast_packet

pgn 129025 "Position Rapid Update" {
  latitude   int32  :32  scale=1e-7  unit="deg"
  longitude  int32  :32  scale=1e-7  unit="deg"
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	proto := GenerateProto(s, "pgn.v1")

	// Name-only should not get a message.
	if strings.Contains(proto, "message AISClassAPositionReport") {
		t.Error("name-only PGN should not have a proto message")
	}
	// Field-defined should.
	if !strings.Contains(proto, "message PositionRapidUpdate") {
		t.Error("field-defined PGN should have a proto message")
	}
}

func TestGenerateJSONSchemaNameOnly(t *testing.T) {
	src := `
pgn 129038 "AIS Class A Position Report" fast_packet

pgn 129025 "Position Rapid Update" {
  latitude   int32  :32  scale=1e-7  unit="deg"
  longitude  int32  :32  scale=1e-7  unit="deg"
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	js := GenerateJSONSchema(s)

	// Name-only should not get a schema definition.
	if strings.Contains(js, "AISClassAPositionReport") {
		t.Error("name-only PGN should not have a JSON schema definition")
	}
	// Field-defined should.
	if !strings.Contains(js, "PositionRapidUpdate") {
		t.Error("field-defined PGN should have a JSON schema definition")
	}
}

func TestNaming(t *testing.T) {
	tests := []struct {
		input    string
		pascal   string
		snake    string
		screaming string
	}{
		{"wind_speed", "WindSpeed", "wind_speed", "WIND_SPEED"},
		{"Wind Data", "WindData", "wind_data", "WIND_DATA"},
		{"COG & SOG, Rapid Update", "COGSOGRapidUpdate", "cog_sog_rapid_update", "COG_SOG_RAPID_UPDATE"},
		{"Position Rapid Update", "PositionRapidUpdate", "position_rapid_update", "POSITION_RAPID_UPDATE"},
	}
	for _, tt := range tests {
		if got := toPascal(tt.input); got != tt.pascal {
			t.Errorf("toPascal(%q) = %q, want %q", tt.input, got, tt.pascal)
		}
		if got := toSnake(tt.input); got != tt.snake {
			t.Errorf("toSnake(%q) = %q, want %q", tt.input, got, tt.snake)
		}
		if got := toScreamingSnake(tt.input); got != tt.screaming {
			t.Errorf("toScreamingSnake(%q) = %q, want %q", tt.input, got, tt.screaming)
		}
	}
}
