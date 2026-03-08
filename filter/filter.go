// Package filter provides a BPF-inspired expression language for filtering
// decoded NMEA 2000 frames by field values.
//
// Expressions support boolean logic (&&, ||, !), comparison operators
// (==, !=, <, >, <=, >=), and dotted field access for lookup sub-fields.
//
// Example expressions:
//
//	"pgn == 130310 && water_temperature < 280"
//	"register.name == \"State of Charge\""
//	"pgn == 61184 || pgn == 130310"
package filter

import (
	"reflect"
	"strconv"
	"sync"
)

// EvalContext provides the data a filter expression is evaluated against.
type EvalContext struct {
	PGN     uint32
	Src     uint8
	Dst     uint8
	Prio    uint8
	Decoded any               // decoded PGN struct, nil if unavailable
	Lookups map[string]string // from LookupFields(), nil if unavailable
}

// Filter is a compiled display filter expression.
type Filter struct {
	root        node
	needsDecode bool
}

// Compile parses and compiles a filter expression string.
// Returns an error if the expression is syntactically invalid.
func Compile(expr string) (*Filter, error) {
	root, err := parse(expr)
	if err != nil {
		return nil, err
	}
	return &Filter{
		root:        root,
		needsDecode: checkNeedsDecode(root),
	}, nil
}

// Match evaluates the filter against the given context.
func (f *Filter) Match(ctx *EvalContext) bool {
	return eval(f.root, ctx)
}

// NeedsDecode returns true if the expression references decoded fields
// (anything other than pgn, src, dst, prio).
func (f *Filter) NeedsDecode() bool {
	return f.needsDecode
}

// String returns a human-readable representation of the filter.
func (f *Filter) String() string {
	return formatNode(f.root)
}

func formatNode(n node) string {
	switch n := n.(type) {
	case orNode:
		return "(" + formatNode(n.left) + " || " + formatNode(n.right) + ")"
	case andNode:
		return "(" + formatNode(n.left) + " && " + formatNode(n.right) + ")"
	case notNode:
		return "!" + formatNode(n.expr)
	case compNode:
		field := n.field.name
		if n.field.subName != "" {
			field += "." + n.field.subName
		}
		return field + " " + n.op.String() + " " + formatLiteral(n.value)
	default:
		return "?"
	}
}

func formatLiteral(l literal) string {
	switch {
	case l.isString:
		return `"` + l.strVal + `"`
	case l.isFloat:
		return strconv.FormatFloat(l.floatVal, 'g', -1, 64)
	case l.isInt:
		return strconv.FormatInt(l.intVal, 10)
	default:
		return "?"
	}
}

// headerFields that don't require decode.
var headerFields = map[string]bool{
	"pgn":  true,
	"src":  true,
	"dst":  true,
	"prio": true,
}

func checkNeedsDecode(n node) bool {
	switch n := n.(type) {
	case orNode:
		return checkNeedsDecode(n.left) || checkNeedsDecode(n.right)
	case andNode:
		return checkNeedsDecode(n.left) || checkNeedsDecode(n.right)
	case notNode:
		return checkNeedsDecode(n.expr)
	case compNode:
		return !headerFields[n.field.name]
	default:
		return false
	}
}

// eval recursively evaluates a node against the context.
func eval(n node, ctx *EvalContext) bool {
	switch n := n.(type) {
	case orNode:
		return eval(n.left, ctx) || eval(n.right, ctx)
	case andNode:
		return eval(n.left, ctx) && eval(n.right, ctx)
	case notNode:
		return !eval(n.expr, ctx)
	case compNode:
		return evalComp(n, ctx)
	default:
		return false
	}
}

func evalComp(n compNode, ctx *EvalContext) bool {
	// Header field fast path.
	if headerFields[n.field.name] {
		return evalHeaderField(n, ctx)
	}

	// Lookup sub-accessor: field.name -> ctx.Lookups[field]
	if n.field.subName != "" {
		if ctx.Lookups == nil {
			return false
		}
		resolved, ok := ctx.Lookups[n.field.name]
		if !ok {
			return false
		}
		return compareString(resolved, n.op, n.value)
	}

	// Decoded struct field via reflection.
	if ctx.Decoded == nil {
		return false
	}
	return evalDecodedField(n, ctx.Decoded)
}

func evalHeaderField(n compNode, ctx *EvalContext) bool {
	var v uint32
	switch n.field.name {
	case "pgn":
		v = ctx.PGN
	case "src":
		v = uint32(ctx.Src)
	case "dst":
		v = uint32(ctx.Dst)
	case "prio":
		v = uint32(ctx.Prio)
	default:
		return false
	}
	return compareNumeric(float64(v), n.op, n.value)
}

// Field index cache: maps JSON tag names to struct field indices per type.
var fieldIndexCache sync.Map // map[reflect.Type]map[string]int

func getFieldIndex(t reflect.Type) map[string]int {
	if v, ok := fieldIndexCache.Load(t); ok {
		return v.(map[string]int)
	}
	m := buildFieldIndex(t)
	fieldIndexCache.Store(t, m)
	return m
}

// buildFieldIndex maps JSON tag names to struct field indices.
func buildFieldIndex(t reflect.Type) map[string]int {
	m := make(map[string]int, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Tag.Get("json")
		if name == "" || name == "-" {
			name = f.Name
		}
		// Strip options after comma (e.g. "field,omitempty").
		for j := range len(name) {
			if name[j] == ',' {
				name = name[:j]
				break
			}
		}
		m[name] = i
	}
	return m
}

func evalDecodedField(n compNode, decoded any) bool {
	v := reflect.ValueOf(decoded)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return false
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return false
	}

	idx := getFieldIndex(v.Type())
	fieldIdx, ok := idx[n.field.name]
	if !ok {
		return false
	}

	fv := v.Field(fieldIdx)
	return compareReflectValue(fv, n.op, n.value)
}

func compareReflectValue(fv reflect.Value, op compOp, lit literal) bool {
	// Try numeric comparison first.
	if f, ok := toFloat64(fv); ok {
		return compareNumeric(f, op, lit)
	}

	// String comparison.
	if fv.Kind() == reflect.String {
		return compareString(fv.String(), op, lit)
	}

	return false
}

// toFloat64 extracts a float64 from a reflect.Value for numeric comparison.
func toFloat64(v reflect.Value) (float64, bool) {
	switch v.Kind() {
	case reflect.Float32, reflect.Float64:
		return v.Float(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(v.Uint()), true
	default:
		return 0, false
	}
}

func compareNumeric(fieldVal float64, op compOp, lit literal) bool {
	var litVal float64
	switch {
	case lit.isInt:
		litVal = float64(lit.intVal)
	case lit.isFloat:
		litVal = lit.floatVal
	case lit.isString:
		return false
	}

	switch op {
	case opEq:
		return fieldVal == litVal
	case opNe:
		return fieldVal != litVal
	case opLt:
		return fieldVal < litVal
	case opGt:
		return fieldVal > litVal
	case opLe:
		return fieldVal <= litVal
	case opGe:
		return fieldVal >= litVal
	default:
		return false
	}
}

func compareString(fieldVal string, op compOp, lit literal) bool {
	var litVal string
	switch {
	case lit.isString:
		litVal = lit.strVal
	case lit.isInt:
		litVal = strconv.FormatInt(lit.intVal, 10)
	case lit.isFloat:
		litVal = strconv.FormatFloat(lit.floatVal, 'g', -1, 64)
	}

	switch op {
	case opEq:
		return fieldVal == litVal
	case opNe:
		return fieldVal != litVal
	case opLt:
		return fieldVal < litVal
	case opGt:
		return fieldVal > litVal
	case opLe:
		return fieldVal <= litVal
	case opGe:
		return fieldVal >= litVal
	default:
		return false
	}
}

