package pgngen

import (
	"testing"
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
