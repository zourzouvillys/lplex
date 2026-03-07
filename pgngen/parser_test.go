package pgngen

import (
	"strings"
	"testing"
	"time"
)

func TestParseBasic(t *testing.T) {
	src := `
enum WindReference {
  0 = "true_north"
  1 = "magnetic_north"
  2 = "apparent"
}

pgn 130306 "Wind Data" {
  sid              uint8          :8
  wind_speed       uint16         :16  scale=0.01   unit="m/s"
  wind_angle       uint16         :16  scale=0.0001 unit="rad"
  wind_reference   WindReference  :3
  _                               :5
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Enums) != 1 {
		t.Fatalf("expected 1 enum, got %d", len(s.Enums))
	}
	e := s.Enums[0]
	if e.Name != "WindReference" {
		t.Errorf("enum name = %q, want WindReference", e.Name)
	}
	if len(e.Values) != 3 {
		t.Fatalf("expected 3 enum values, got %d", len(e.Values))
	}
	if e.Values[2].Name != "apparent" {
		t.Errorf("enum value[2] = %q, want apparent", e.Values[2].Name)
	}

	if len(s.PGNs) != 1 {
		t.Fatalf("expected 1 PGN, got %d", len(s.PGNs))
	}
	p := s.PGNs[0]
	if p.PGN != 130306 {
		t.Errorf("PGN = %d, want 130306", p.PGN)
	}
	if p.Description != "Wind Data" {
		t.Errorf("description = %q, want Wind Data", p.Description)
	}
	if len(p.Fields) != 5 {
		t.Fatalf("expected 5 fields, got %d", len(p.Fields))
	}

	// Check wind_speed field
	ws := p.Fields[1]
	if ws.Name != "wind_speed" {
		t.Errorf("field[1].Name = %q, want wind_speed", ws.Name)
	}
	if ws.Bits != 16 {
		t.Errorf("wind_speed bits = %d, want 16", ws.Bits)
	}
	if ws.Scale != 0.01 {
		t.Errorf("wind_speed scale = %f, want 0.01", ws.Scale)
	}
	if ws.Unit != "m/s" {
		t.Errorf("wind_speed unit = %q, want m/s", ws.Unit)
	}

	// Check reserved field
	reserved := p.Fields[4]
	if !reserved.IsReserved() {
		t.Error("field[4] should be reserved")
	}
	if reserved.Bits != 5 {
		t.Errorf("reserved bits = %d, want 5", reserved.Bits)
	}
}

func TestParseResolve(t *testing.T) {
	src := `
enum Ref {
  0 = "a"
  1 = "b"
}

pgn 100 "Test" {
  x     uint8  :8
  y     uint16 :16
  z     Ref    :2
  _            :6
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}

	fields := s.PGNs[0].Fields
	if fields[0].BitStart != 0 {
		t.Errorf("x.BitStart = %d, want 0", fields[0].BitStart)
	}
	if fields[1].BitStart != 8 {
		t.Errorf("y.BitStart = %d, want 8", fields[1].BitStart)
	}
	if fields[2].BitStart != 24 {
		t.Errorf("z.BitStart = %d, want 24", fields[2].BitStart)
	}
	if fields[3].BitStart != 26 {
		t.Errorf("reserved.BitStart = %d, want 26", fields[3].BitStart)
	}
}

func TestParseResolveUnknownEnum(t *testing.T) {
	src := `
pgn 100 "Test" {
  x  UnknownEnum :8
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err == nil {
		t.Fatal("expected error for unknown enum")
	}
}

func TestParseTotalBits(t *testing.T) {
	src := `
pgn 129025 "Position" {
  latitude   int32  :32  scale=1e-7 unit="deg"
  longitude  int32  :32  scale=1e-7 unit="deg"
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	p := s.PGNs[0]
	if p.TotalBits() != 64 {
		t.Errorf("TotalBits = %d, want 64", p.TotalBits())
	}
	if p.MinBytes() != 8 {
		t.Errorf("MinBytes = %d, want 8", p.MinBytes())
	}
}

func TestParseStringField(t *testing.T) {
	src := `
pgn 126996 "Product Info" {
  code    uint16  :16
  model   string  :256
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	f := s.PGNs[0].Fields[1]
	if f.Type != TypeString {
		t.Errorf("type = %d, want TypeString", f.Type)
	}
	if f.Bits != 256 {
		t.Errorf("bits = %d, want 256", f.Bits)
	}
}

func TestParseStringFieldBadWidth(t *testing.T) {
	src := `
pgn 100 "Test" {
  name  string  :13
}
`
	_, err := Parse(src)
	if err == nil {
		t.Fatal("expected error for non-byte-aligned string width")
	}
}

func TestParseComments(t *testing.T) {
	src := `
# This is a comment
pgn 100 "Test" {
  x  uint8  :8  # inline comment
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.PGNs) != 1 {
		t.Fatalf("expected 1 PGN, got %d", len(s.PGNs))
	}
}

func TestParseMultipleFiles(t *testing.T) {
	sources := map[string]string{
		"a.pgn": `
enum Ref {
  0 = "x"
}
pgn 100 "A" {
  val Ref :4
  _       :4
}
`,
		"b.pgn": `
pgn 200 "B" {
  val uint8 :8
}
`,
	}
	s, err := ParseFiles(sources)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Enums) != 1 {
		t.Errorf("enums = %d, want 1", len(s.Enums))
	}
	if len(s.PGNs) != 2 {
		t.Errorf("pgns = %d, want 2", len(s.PGNs))
	}
}

func TestParsePGNFastPacket(t *testing.T) {
	src := `
pgn 129029 "GNSS Position Data" fast_packet {
  sid  uint8  :8
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	p := s.PGNs[0]
	if !p.FastPacket {
		t.Error("expected FastPacket to be true")
	}
	if p.Interval != 0 {
		t.Errorf("Interval = %v, want 0", p.Interval)
	}
	if p.OnDemand {
		t.Error("expected OnDemand to be false")
	}
}

func TestParsePGNInterval(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want time.Duration
	}{
		{"milliseconds", "pgn 100 \"Test\" interval=1000ms {\n  sid uint8 :8\n}", 1000 * time.Millisecond},
		{"seconds", "pgn 100 \"Test\" interval=2s {\n  sid uint8 :8\n}", 2 * time.Second},
		{"100ms", "pgn 100 \"Test\" interval=100ms {\n  sid uint8 :8\n}", 100 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Parse(tt.src)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := s.PGNs[0].Interval; got != tt.want {
				t.Errorf("Interval = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParsePGNOnDemand(t *testing.T) {
	src := `
pgn 59904 "ISO Request" on_demand {
  requested_pgn  uint32  :24
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	p := s.PGNs[0]
	if !p.OnDemand {
		t.Error("expected OnDemand to be true")
	}
	if p.FastPacket {
		t.Error("expected FastPacket to be false")
	}
}

func TestParsePGNAllMetadata(t *testing.T) {
	src := `
pgn 126996 "Product Information" fast_packet on_demand interval=5000ms {
  code  uint16  :16
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	p := s.PGNs[0]
	if !p.FastPacket {
		t.Error("expected FastPacket")
	}
	if !p.OnDemand {
		t.Error("expected OnDemand")
	}
	if p.Interval != 5*time.Second {
		t.Errorf("Interval = %v, want 5s", p.Interval)
	}
}

func TestParsePGNBadInterval(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		wantErr string
	}{
		{
			name:    "bad suffix",
			src:     "pgn 100 \"Test\" interval=100min {\n  x uint8 :8\n}",
			wantErr: "unsupported duration suffix",
		},
		{
			name:    "negative",
			src:     "pgn 100 \"Test\" interval=-5ms {\n  x uint8 :8\n}",
			wantErr: "duration must be positive",
		},
		{
			name:    "not a number",
			src:     "pgn 100 \"Test\" interval=abcms {\n  x uint8 :8\n}",
			wantErr: "invalid millisecond value",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.src)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestParsePGNUnknownAttribute(t *testing.T) {
	src := "pgn 100 \"Test\" bogus_flag {\n  x uint8 :8\n}"
	_, err := Parse(src)
	if err == nil {
		t.Fatal("expected error for unknown PGN attribute")
	}
	if !strings.Contains(err.Error(), "unknown PGN attribute") {
		t.Errorf("error = %q, want substring about unknown attribute", err.Error())
	}
}

func TestDispatchGroupConflictingMetadata(t *testing.T) {
	src := `
pgn 61184 "Variant A" fast_packet {
  manufacturer_code  uint16  :11  value=358
  _                          :5
}

pgn 61184 "Variant B" {
  manufacturer_code  uint16  :11  value=229
  _                          :5
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Resolve()
	if err == nil {
		t.Fatal("expected error for conflicting metadata")
	}
	if !strings.Contains(err.Error(), "conflicting fast_packet") {
		t.Errorf("error = %q, want substring about conflicting fast_packet", err.Error())
	}
}

func TestParseNameOnlyPGN(t *testing.T) {
	src := `pgn 129038 "AIS Class A Position Report" fast_packet`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.PGNs) != 1 {
		t.Fatalf("expected 1 PGN, got %d", len(s.PGNs))
	}
	p := s.PGNs[0]
	if p.PGN != 129038 {
		t.Errorf("PGN = %d, want 129038", p.PGN)
	}
	if p.Description != "AIS Class A Position Report" {
		t.Errorf("description = %q", p.Description)
	}
	if !p.FastPacket {
		t.Error("expected FastPacket")
	}
	if !p.IsNameOnly() {
		t.Error("expected name-only (Fields == nil)")
	}
	if p.Fields != nil {
		t.Errorf("Fields should be nil, got %v", p.Fields)
	}
}

func TestParseNameOnlyPGNWithDraft(t *testing.T) {
	src := `pgn 127493 "Transmission Parameters Dynamic" draft`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	p := s.PGNs[0]
	if !p.Draft {
		t.Error("expected Draft")
	}
	if !p.IsNameOnly() {
		t.Error("expected name-only")
	}
}

func TestParseDraftPGNWithFields(t *testing.T) {
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
	p := s.PGNs[0]
	if !p.Draft {
		t.Error("expected Draft")
	}
	if p.IsNameOnly() {
		t.Error("should not be name-only, has fields")
	}
	if len(p.Fields) != 3 {
		t.Errorf("expected 3 fields, got %d", len(p.Fields))
	}
}

func TestParseUnknownField(t *testing.T) {
	src := `
pgn 127493 "Test" {
  engine_instance  uint8   :8
  ?                         :32
  _                         :8
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	f := s.PGNs[0].Fields[1]
	if !f.IsUnknown() {
		t.Error("field should be unknown")
	}
	if !f.IsSkipped() {
		t.Error("unknown field should be skipped")
	}
	if f.IsReserved() {
		t.Error("unknown field should not be reserved")
	}
	if f.Bits != 32 {
		t.Errorf("bits = %d, want 32", f.Bits)
	}

	// reserved field
	r := s.PGNs[0].Fields[2]
	if !r.IsReserved() {
		t.Error("field should be reserved")
	}
	if !r.IsSkipped() {
		t.Error("reserved field should be skipped")
	}
	if r.IsUnknown() {
		t.Error("reserved field should not be unknown")
	}
}

func TestParseEmptyBraceBodyError(t *testing.T) {
	// Single-line empty body is rejected (parsed as bad attribute).
	src := `pgn 59392 "ISO Ack" { }`
	_, err := Parse(src)
	if err == nil {
		t.Fatal("expected error for empty PGN body")
	}
}

func TestParseEmptyBraceBodyMultilineError(t *testing.T) {
	src := "pgn 59392 \"ISO Ack\" {\n}"
	_, err := Parse(src)
	if err == nil {
		t.Fatal("expected error for empty PGN body")
	}
	if !strings.Contains(err.Error(), "empty PGN body") {
		t.Errorf("error = %q, want substring about empty body", err.Error())
	}
}

func TestParseUnknownFieldAttributeRestrictions(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		wantErr string
	}{
		{
			name:    "value on unknown field",
			src:     "pgn 99999 \"Test\" {\n  ?  :8  value=5\n}",
			wantErr: "reserved/unknown field cannot have value=",
		},
		{
			name:    "lookup on unknown field",
			src:     "lookup Foo uint8 {\n  1 = \"one\"\n}\npgn 99999 \"Test\" {\n  ?  :8  lookup=Foo\n}",
			wantErr: "reserved/unknown field cannot have lookup=",
		},
		{
			name:    "repeat on unknown field",
			src:     "pgn 99999 \"Test\" {\n  ?  :2  repeat=4\n}",
			wantErr: "reserved/unknown field cannot have repeat=",
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

func TestNameOnlyPGNResolve(t *testing.T) {
	src := `
pgn 129038 "AIS Class A Position Report" fast_packet
pgn 129039 "AIS Class B Position Report" fast_packet
pgn 129025 "Position Rapid Update" {
  latitude   int32  :32  scale=1e-7 unit="deg"
  longitude  int32  :32  scale=1e-7 unit="deg"
}
`
	s, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(); err != nil {
		t.Fatal(err)
	}
	// Should have 3 PGNs: 2 name-only + 1 with fields
	if len(s.PGNs) != 3 {
		t.Fatalf("expected 3 PGNs, got %d", len(s.PGNs))
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{`pgn 130306 "Wind Data" {`, []string{"pgn", "130306", `"Wind Data"`, "{"}},
		{`  speed  uint16  :16  scale=0.01`, []string{"speed", "uint16", ":16", "scale=0.01"}},
		{`  _  :5`, []string{"_", ":5"}},
		{``, nil},
		{`  # comment`, []string{"#", "comment"}}, // tokenize doesn't strip comments; parser does
	}
	for _, tt := range tests {
		got := tokenize(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("tokenize(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("tokenize(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}
