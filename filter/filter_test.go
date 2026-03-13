package filter

import (
	"testing"

	"github.com/sixfathoms/lplex/pgn"
)

// --- Lexer tests ---

func TestLex(t *testing.T) {
	tests := []struct {
		input  string
		tokens []tokenType
	}{
		{"pgn == 130310", []tokenType{tokIdent, tokEq, tokInt, tokEOF}},
		{"a < 1.5", []tokenType{tokIdent, tokLt, tokFloat, tokEOF}},
		{`name == "foo"`, []tokenType{tokIdent, tokEq, tokString, tokEOF}},
		{"a && b > 1", []tokenType{tokIdent, tokAnd, tokIdent, tokGt, tokInt, tokEOF}},
		{"a || b != 2", []tokenType{tokIdent, tokOr, tokIdent, tokNe, tokInt, tokEOF}},
		{"!a >= 3", []tokenType{tokNot, tokIdent, tokGe, tokInt, tokEOF}},
		{"(a <= 1)", []tokenType{tokLParen, tokIdent, tokLe, tokInt, tokRParen, tokEOF}},
		{"a.name == \"x\"", []tokenType{tokIdent, tokDot, tokIdent, tokEq, tokString, tokEOF}},
		{"a > -5", []tokenType{tokIdent, tokGt, tokInt, tokEOF}},
		{"x == 1 and y == 2 or z == 3", []tokenType{
			tokIdent, tokEq, tokInt,
			tokAnd,
			tokIdent, tokEq, tokInt,
			tokOr,
			tokIdent, tokEq, tokInt,
			tokEOF,
		}},
		{"not a == 1", []tokenType{tokNot, tokIdent, tokEq, tokInt, tokEOF}},
	}

	for _, tt := range tests {
		tokens, err := lex(tt.input)
		if err != nil {
			t.Errorf("lex(%q): %v", tt.input, err)
			continue
		}
		if len(tokens) != len(tt.tokens) {
			t.Errorf("lex(%q): got %d tokens, want %d", tt.input, len(tokens), len(tt.tokens))
			continue
		}
		for i, tok := range tokens {
			if tok.typ != tt.tokens[i] {
				t.Errorf("lex(%q)[%d]: got type %d, want %d (val=%q)", tt.input, i, tok.typ, tt.tokens[i], tok.val)
			}
		}
	}
}

func TestLexErrors(t *testing.T) {
	tests := []string{
		`"unterminated`,
		`'also unterminated`,
		"@invalid",
	}
	for _, input := range tests {
		_, err := lex(input)
		if err == nil {
			t.Errorf("lex(%q): expected error", input)
		}
	}
}

// --- Parser tests ---

func TestParseValid(t *testing.T) {
	tests := []struct {
		input    string
		nodeType string
	}{
		{"pgn == 130310", "comparison"},
		{"a > 1 && b < 2", "and"},
		{"a == 1 || b == 2", "or"},
		{"!a == 1", "not"},
		{"(a == 1)", "comparison"},
		{"a.name == \"foo\"", "comparison"},
		{"a == 1 && b == 2 || c == 3", "or"},  // || is lower precedence
		{"a == 1 || b == 2 && c == 3", "or"},  // && binds tighter
		{"not a == 1 and b == 2", "and"},       // not binds tightest
	}

	for _, tt := range tests {
		n, err := parse(tt.input)
		if err != nil {
			t.Errorf("parse(%q): %v", tt.input, err)
			continue
		}
		if n.nodeType() != tt.nodeType {
			t.Errorf("parse(%q): got nodeType %q, want %q", tt.input, n.nodeType(), tt.nodeType)
		}
	}
}

func TestParseErrors(t *testing.T) {
	tests := []string{
		"",             // empty expression
		"== 1",         // missing field
		"a ==",         // missing value
		"a + 1",        // invalid operator
		"(a == 1",      // unclosed paren
		"a == 1)",      // extra paren
		"a . == 1",     // dot with no sub-field (a number follows, not ident)
	}
	for _, input := range tests {
		_, err := parse(input)
		if err == nil {
			t.Errorf("parse(%q): expected error", input)
		}
	}
}

// --- Compile + Match end-to-end tests ---

func TestHeaderFields(t *testing.T) {
	tests := []struct {
		expr string
		ctx  EvalContext
		want bool
	}{
		{"pgn == 130310", EvalContext{PGN: 130310}, true},
		{"pgn == 130310", EvalContext{PGN: 130311}, false},
		{"pgn != 130310", EvalContext{PGN: 130311}, true},
		{"src == 42", EvalContext{Src: 42}, true},
		{"src == 42", EvalContext{Src: 43}, false},
		{"dst == 255", EvalContext{Dst: 255}, true},
		{"prio == 3", EvalContext{Prio: 3}, true},
		{"prio < 4", EvalContext{Prio: 3}, true},
		{"prio > 4", EvalContext{Prio: 3}, false},
		{"prio >= 3", EvalContext{Prio: 3}, true},
		{"prio <= 3", EvalContext{Prio: 3}, true},
	}

	for _, tt := range tests {
		f, err := Compile(tt.expr)
		if err != nil {
			t.Fatalf("Compile(%q): %v", tt.expr, err)
		}
		if got := f.Match(&tt.ctx); got != tt.want {
			t.Errorf("Match(%q, %+v) = %v, want %v", tt.expr, tt.ctx, got, tt.want)
		}
	}
}

func TestNeedsDecode(t *testing.T) {
	tests := []struct {
		expr string
		want bool
	}{
		{"pgn == 130310", false},
		{"src == 1 && dst == 255", false},
		{"water_temperature < 280", true},
		{"pgn == 130310 && water_temperature < 280", true},
		{"pgn == 130310 || src == 1", false},
		{"register.name == \"foo\"", true},
	}

	for _, tt := range tests {
		f, err := Compile(tt.expr)
		if err != nil {
			t.Fatalf("Compile(%q): %v", tt.expr, err)
		}
		if got := f.NeedsDecode(); got != tt.want {
			t.Errorf("NeedsDecode(%q) = %v, want %v", tt.expr, got, tt.want)
		}
	}
}

func TestDecodedFieldsEnvironmentalParams(t *testing.T) {
	decoded := pgn.EnvironmentalParametersOutside{
		Sid:                 1,
		WaterTemperature:    280.15, // ~7C
		OutsideTemperature:  293.15, // ~20C
		AtmosphericPressure: 101300,
	}

	tests := []struct {
		expr string
		want bool
	}{
		{"water_temperature < 290", true},
		{"water_temperature > 290", false},
		{"water_temperature == 280.15", true},
		{"water_temperature != 280.15", false},
		{"sid == 1", true},
		{"sid == 2", false},
		{"outside_temperature >= 293.15", true},
		{"atmospheric_pressure > 100000", true},
	}

	for _, tt := range tests {
		f, err := Compile(tt.expr)
		if err != nil {
			t.Fatalf("Compile(%q): %v", tt.expr, err)
		}
		ctx := &EvalContext{
			PGN:     130310,
			Decoded: decoded,
		}
		if got := f.Match(ctx); got != tt.want {
			t.Errorf("Match(%q) = %v, want %v", tt.expr, got, tt.want)
		}
	}
}

func TestDecodedFieldsWithPointer(t *testing.T) {
	decoded := &pgn.EnvironmentalParametersOutside{
		WaterTemperature: 280.15,
	}
	f, err := Compile("water_temperature < 290")
	if err != nil {
		t.Fatal(err)
	}
	ctx := &EvalContext{PGN: 130310, Decoded: decoded}
	if !f.Match(ctx) {
		t.Error("expected match with pointer to struct")
	}
}

func TestLookupFields(t *testing.T) {
	// VictronBatteryRegister with register=0xFFF -> "State of Charge"
	decoded := pgn.VictronBatteryRegister{
		ManufacturerCode: 358,
		IndustryCode:     4,
		Register:         0xFFF,
		Payload:          500,
	}

	tests := []struct {
		expr string
		want bool
	}{
		{"register == 4095", true},
		{"register != 4095", false},
		{`register.name == "State of Charge"`, true},
		{`register.name == "Something Else"`, false},
		{`register.name != "Something Else"`, true},
		{"payload == 500", true},
	}

	for _, tt := range tests {
		f, err := Compile(tt.expr)
		if err != nil {
			t.Fatalf("Compile(%q): %v", tt.expr, err)
		}
		ctx := &EvalContext{
			PGN:     61184,
			Decoded: decoded,
			Lookups: decoded.LookupFields(),
		}
		if got := f.Match(ctx); got != tt.want {
			t.Errorf("Match(%q) = %v, want %v", tt.expr, got, tt.want)
		}
	}
}

func TestNilDecoded(t *testing.T) {
	f, err := Compile("water_temperature < 280")
	if err != nil {
		t.Fatal(err)
	}
	ctx := &EvalContext{PGN: 130310, Decoded: nil}
	if f.Match(ctx) {
		t.Error("expected no match when Decoded is nil")
	}
}

func TestMissingField(t *testing.T) {
	decoded := pgn.EnvironmentalParametersOutside{WaterTemperature: 280}
	f, err := Compile("nonexistent_field == 1")
	if err != nil {
		t.Fatal(err)
	}
	ctx := &EvalContext{PGN: 130310, Decoded: decoded}
	if f.Match(ctx) {
		t.Error("expected no match for missing field")
	}
}

func TestMissingLookup(t *testing.T) {
	f, err := Compile(`nonexistent.name == "foo"`)
	if err != nil {
		t.Fatal(err)
	}
	// No lookups at all.
	ctx := &EvalContext{PGN: 130310, Lookups: nil}
	if f.Match(ctx) {
		t.Error("expected no match when Lookups is nil")
	}
	// Lookups present but key missing.
	ctx.Lookups = map[string]string{"other": "bar"}
	if f.Match(ctx) {
		t.Error("expected no match when lookup key missing")
	}
}

func TestBooleanLogic(t *testing.T) {
	tests := []struct {
		expr string
		ctx  EvalContext
		want bool
	}{
		{"pgn == 1 && src == 2", EvalContext{PGN: 1, Src: 2}, true},
		{"pgn == 1 && src == 3", EvalContext{PGN: 1, Src: 2}, false},
		{"pgn == 1 || src == 3", EvalContext{PGN: 1, Src: 2}, true},
		{"pgn == 2 || src == 3", EvalContext{PGN: 1, Src: 2}, false},
		{"!pgn == 2", EvalContext{PGN: 1}, true},
		{"!pgn == 1", EvalContext{PGN: 1}, false},
		{"!(pgn == 2 || src == 3)", EvalContext{PGN: 1, Src: 2}, true},
		{"!(pgn == 1 || src == 3)", EvalContext{PGN: 1, Src: 2}, false},
	}

	for _, tt := range tests {
		f, err := Compile(tt.expr)
		if err != nil {
			t.Fatalf("Compile(%q): %v", tt.expr, err)
		}
		if got := f.Match(&tt.ctx); got != tt.want {
			t.Errorf("Match(%q, %+v) = %v, want %v", tt.expr, tt.ctx, got, tt.want)
		}
	}
}

func TestPrecedence(t *testing.T) {
	// "a || b && c" should parse as "a || (b && c)" since && binds tighter.
	// So if a=false, b=true, c=false => false || (true && false) = false.
	f, err := Compile("pgn == 1 || src == 2 && dst == 3")
	if err != nil {
		t.Fatal(err)
	}

	// pgn=0,src=2,dst=3 => false || (true && true) = true
	ctx := &EvalContext{PGN: 0, Src: 2, Dst: 3}
	if !f.Match(ctx) {
		t.Error("expected true: false || (true && true)")
	}

	// pgn=0,src=2,dst=0 => false || (true && false) = false
	ctx = &EvalContext{PGN: 0, Src: 2, Dst: 0}
	if f.Match(ctx) {
		t.Error("expected false: false || (true && false)")
	}

	// pgn=1,src=0,dst=0 => true || (false && false) = true
	ctx = &EvalContext{PGN: 1, Src: 0, Dst: 0}
	if !f.Match(ctx) {
		t.Error("expected true: true || (false && false)")
	}
}

func TestCombinedHeaderAndDecoded(t *testing.T) {
	decoded := pgn.EnvironmentalParametersOutside{
		WaterTemperature: 280.15,
	}
	f, err := Compile("pgn == 130310 && water_temperature < 290")
	if err != nil {
		t.Fatal(err)
	}

	// Matching PGN and matching temp.
	ctx := &EvalContext{PGN: 130310, Decoded: decoded}
	if !f.Match(ctx) {
		t.Error("expected match")
	}

	// Wrong PGN, short-circuits.
	ctx = &EvalContext{PGN: 99999, Decoded: decoded}
	if f.Match(ctx) {
		t.Error("expected no match with wrong PGN")
	}

	// Right PGN but no decoded data.
	ctx = &EvalContext{PGN: 130310, Decoded: nil}
	if f.Match(ctx) {
		t.Error("expected no match with nil decoded")
	}
}

func TestStringFilter(t *testing.T) {
	f, err := Compile(`pgn == 130310 && water_temperature < 280`)
	if err != nil {
		t.Fatal(err)
	}
	if f.String() == "" {
		t.Error("String() should not be empty")
	}
}

func TestSingleQuoteStrings(t *testing.T) {
	f, err := Compile(`register.name == 'State of Charge'`)
	if err != nil {
		t.Fatal(err)
	}
	decoded := pgn.VictronBatteryRegister{Register: 0xFFF}
	ctx := &EvalContext{
		PGN:     61184,
		Decoded: decoded,
		Lookups: decoded.LookupFields(),
	}
	if !f.Match(ctx) {
		t.Error("expected match with single-quoted string")
	}
}

func TestHeaderFieldSubAccessor(t *testing.T) {
	tests := []struct {
		expr    string
		lookups map[string]string
		want    bool
	}{
		// src.manufacturer match.
		{`src.manufacturer == "Garmin"`, map[string]string{"src.manufacturer": "Garmin"}, true},
		{`src.manufacturer == "Garmin"`, map[string]string{"src.manufacturer": "Victron"}, false},
		{`src.manufacturer != "Garmin"`, map[string]string{"src.manufacturer": "Victron"}, true},

		// dst.manufacturer match.
		{`dst.manufacturer == "Lowrance"`, map[string]string{"dst.manufacturer": "Lowrance"}, true},
		{`dst.manufacturer == "Lowrance"`, map[string]string{"dst.manufacturer": "Garmin"}, false},

		// dst.model_id match.
		{`dst.model_id == "GPS 19x"`, map[string]string{"dst.model_id": "GPS 19x"}, true},
		{`dst.model_id == "GPS 19x"`, map[string]string{}, false},

		// Combined with header field.
		{`pgn == 59904 && dst.manufacturer == "Garmin"`, map[string]string{"dst.manufacturer": "Garmin"}, true},
		{`pgn == 59904 && dst.manufacturer == "Garmin"`, map[string]string{"dst.manufacturer": "Victron"}, false},

		// No lookups at all: sub-accessor should not match.
		{`src.manufacturer == "Garmin"`, nil, false},

		// Plain src/dst without sub-accessor still works as numeric.
		{`src == 42`, map[string]string{"src.manufacturer": "Garmin"}, true},
		{`dst == 10`, map[string]string{"dst.manufacturer": "Lowrance"}, true},
	}

	for _, tt := range tests {
		f, err := Compile(tt.expr)
		if err != nil {
			t.Fatalf("Compile(%q): %v", tt.expr, err)
		}
		ctx := &EvalContext{
			PGN:     59904,
			Src:     42,
			Dst:     10,
			Lookups: tt.lookups,
		}
		if got := f.Match(ctx); got != tt.want {
			t.Errorf("Match(%q, lookups=%v) = %v, want %v", tt.expr, tt.lookups, got, tt.want)
		}
	}
}

func TestNegativeNumber(t *testing.T) {
	f, err := Compile("sid > -1")
	if err != nil {
		t.Fatal(err)
	}
	decoded := pgn.EnvironmentalParametersOutside{Sid: 0}
	ctx := &EvalContext{PGN: 130310, Decoded: decoded}
	if !f.Match(ctx) {
		t.Error("expected match: 0 > -1")
	}
}
