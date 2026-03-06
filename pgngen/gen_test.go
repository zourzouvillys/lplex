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
