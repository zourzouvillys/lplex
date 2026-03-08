package filter

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// AST node types for the display filter expression language.

// node is the interface all AST nodes implement.
type node interface {
	nodeType() string
}

// orNode represents "left || right".
type orNode struct {
	left, right node
}

func (orNode) nodeType() string { return "or" }

// andNode represents "left && right".
type andNode struct {
	left, right node
}

func (andNode) nodeType() string { return "and" }

// notNode represents "!expr".
type notNode struct {
	expr node
}

func (notNode) nodeType() string { return "not" }

// compNode represents "field op value".
type compNode struct {
	field fieldRef
	op    compOp
	value literal
}

func (compNode) nodeType() string { return "comparison" }

// fieldRef is a dotted field reference like "register" or "register.name".
type fieldRef struct {
	name    string // primary field name (JSON tag)
	subName string // sub-accessor (e.g. "name" for lookup resolution)
}

// compOp is a comparison operator.
type compOp int

const (
	opEq compOp = iota
	opNe
	opLt
	opGt
	opLe
	opGe
)

func (o compOp) String() string {
	switch o {
	case opEq:
		return "=="
	case opNe:
		return "!="
	case opLt:
		return "<"
	case opGt:
		return ">"
	case opLe:
		return "<="
	case opGe:
		return ">="
	default:
		return "?"
	}
}

// literal is a parsed value (int, float, or string).
type literal struct {
	intVal    int64
	floatVal  float64
	strVal    string
	isFloat   bool
	isString  bool
	isInt     bool
}

// Lexer

type tokenType int

const (
	tokEOF tokenType = iota
	tokIdent
	tokInt
	tokFloat
	tokString
	tokLParen
	tokRParen
	tokDot
	tokEq    // ==
	tokNe    // !=
	tokLt    // <
	tokGt    // >
	tokLe    // <=
	tokGe    // >=
	tokAnd   // && or "and"
	tokOr    // || or "or"
	tokNot   // ! or "not"
)

type token struct {
	typ tokenType
	val string
	pos int
}

type lexer struct {
	input  string
	pos    int
	tokens []token
}

func lex(input string) ([]token, error) {
	l := &lexer{input: input}
	if err := l.scan(); err != nil {
		return nil, err
	}
	return l.tokens, nil
}

func (l *lexer) scan() error {
	for l.pos < len(l.input) {
		l.skipWhitespace()
		if l.pos >= len(l.input) {
			break
		}

		start := l.pos
		ch := l.input[l.pos]

		switch {
		case ch == '(':
			l.emit(tokLParen, "(", start)
		case ch == ')':
			l.emit(tokRParen, ")", start)
		case ch == '.':
			l.emit(tokDot, ".", start)
		case ch == '!' && l.peek() == '=':
			l.pos++
			l.emit(tokNe, "!=", start)
		case ch == '!':
			l.emit(tokNot, "!", start)
		case ch == '=' && l.peek() == '=':
			l.pos++
			l.emit(tokEq, "==", start)
		case ch == '<' && l.peek() == '=':
			l.pos++
			l.emit(tokLe, "<=", start)
		case ch == '<':
			l.emit(tokLt, "<", start)
		case ch == '>' && l.peek() == '=':
			l.pos++
			l.emit(tokGe, ">=", start)
		case ch == '>':
			l.emit(tokGt, ">", start)
		case ch == '&' && l.peek() == '&':
			l.pos++
			l.emit(tokAnd, "&&", start)
		case ch == '|' && l.peek() == '|':
			l.pos++
			l.emit(tokOr, "||", start)
		case ch == '"' || ch == '\'':
			if err := l.scanString(ch); err != nil {
				return err
			}
		case ch == '-' || isDigit(ch):
			l.scanNumber()
		case isIdentStart(ch):
			l.scanIdent()
		default:
			return fmt.Errorf("unexpected character %q at position %d", ch, l.pos)
		}
	}
	l.tokens = append(l.tokens, token{typ: tokEOF, pos: l.pos})
	return nil
}

func (l *lexer) emit(typ tokenType, val string, start int) {
	l.tokens = append(l.tokens, token{typ: typ, val: val, pos: start})
	l.pos++
}

func (l *lexer) peek() byte {
	if l.pos+1 < len(l.input) {
		return l.input[l.pos+1]
	}
	return 0
}

func (l *lexer) skipWhitespace() {
	for l.pos < len(l.input) && unicode.IsSpace(rune(l.input[l.pos])) {
		l.pos++
	}
}

func (l *lexer) scanString(quote byte) error {
	start := l.pos
	l.pos++ // skip opening quote
	var sb strings.Builder
	for l.pos < len(l.input) {
		ch := l.input[l.pos]
		if ch == '\\' && l.pos+1 < len(l.input) {
			l.pos++
			sb.WriteByte(l.input[l.pos])
			l.pos++
			continue
		}
		if ch == quote {
			l.tokens = append(l.tokens, token{typ: tokString, val: sb.String(), pos: start})
			l.pos++
			return nil
		}
		sb.WriteByte(ch)
		l.pos++
	}
	return fmt.Errorf("unterminated string starting at position %d", start)
}

func (l *lexer) scanNumber() {
	start := l.pos
	if l.input[l.pos] == '-' {
		l.pos++
	}
	isFloat := false
	for l.pos < len(l.input) && (isDigit(l.input[l.pos]) || l.input[l.pos] == '.') {
		if l.input[l.pos] == '.' {
			isFloat = true
		}
		l.pos++
	}
	val := l.input[start:l.pos]
	if isFloat {
		l.tokens = append(l.tokens, token{typ: tokFloat, val: val, pos: start})
	} else {
		l.tokens = append(l.tokens, token{typ: tokInt, val: val, pos: start})
	}
}

func (l *lexer) scanIdent() {
	start := l.pos
	for l.pos < len(l.input) && isIdentPart(l.input[l.pos]) {
		l.pos++
	}
	val := l.input[start:l.pos]
	switch val {
	case "and":
		l.tokens = append(l.tokens, token{typ: tokAnd, val: val, pos: start})
	case "or":
		l.tokens = append(l.tokens, token{typ: tokOr, val: val, pos: start})
	case "not":
		l.tokens = append(l.tokens, token{typ: tokNot, val: val, pos: start})
	default:
		l.tokens = append(l.tokens, token{typ: tokIdent, val: val, pos: start})
	}
}

func isDigit(ch byte) bool     { return ch >= '0' && ch <= '9' }
func isIdentStart(ch byte) bool { return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_' }
func isIdentPart(ch byte) bool  { return isIdentStart(ch) || isDigit(ch) }

// Parser (recursive descent, LL(1))

type parser struct {
	tokens []token
	pos    int
}

func parse(input string) (node, error) {
	tokens, err := lex(input)
	if err != nil {
		return nil, err
	}
	p := &parser{tokens: tokens}
	n, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.current().typ != tokEOF {
		return nil, fmt.Errorf("unexpected token %q at position %d", p.current().val, p.current().pos)
	}
	return n, nil
}

func (p *parser) current() token {
	if p.pos < len(p.tokens) {
		return p.tokens[p.pos]
	}
	return token{typ: tokEOF, pos: -1}
}

func (p *parser) advance() token {
	t := p.current()
	if p.pos < len(p.tokens) {
		p.pos++
	}
	return t
}

func (p *parser) parseExpr() (node, error) {
	return p.parseOr()
}

func (p *parser) parseOr() (node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.current().typ == tokOr {
		p.advance()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = orNode{left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseAnd() (node, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for p.current().typ == tokAnd {
		p.advance()
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = andNode{left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseUnary() (node, error) {
	if p.current().typ == tokNot {
		p.advance()
		expr, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return notNode{expr: expr}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (node, error) {
	if p.current().typ == tokLParen {
		p.advance()
		expr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if p.current().typ != tokRParen {
			return nil, fmt.Errorf("expected ')' at position %d", p.current().pos)
		}
		p.advance()
		return expr, nil
	}
	return p.parseComparison()
}

func (p *parser) parseComparison() (node, error) {
	// field_ref
	if p.current().typ != tokIdent {
		return nil, fmt.Errorf("expected field name at position %d, got %q", p.current().pos, p.current().val)
	}
	field := fieldRef{name: p.advance().val}
	if p.current().typ == tokDot {
		p.advance()
		if p.current().typ != tokIdent {
			return nil, fmt.Errorf("expected sub-field name after '.' at position %d", p.current().pos)
		}
		field.subName = p.advance().val
	}

	// comp_op
	op, err := p.parseCompOp()
	if err != nil {
		return nil, err
	}

	// value
	val, err := p.parseValue()
	if err != nil {
		return nil, err
	}

	return compNode{field: field, op: op, value: val}, nil
}

func (p *parser) parseCompOp() (compOp, error) {
	switch p.current().typ {
	case tokEq:
		p.advance()
		return opEq, nil
	case tokNe:
		p.advance()
		return opNe, nil
	case tokLt:
		p.advance()
		return opLt, nil
	case tokGt:
		p.advance()
		return opGt, nil
	case tokLe:
		p.advance()
		return opLe, nil
	case tokGe:
		p.advance()
		return opGe, nil
	default:
		return 0, fmt.Errorf("expected comparison operator at position %d, got %q", p.current().pos, p.current().val)
	}
}

func (p *parser) parseValue() (literal, error) {
	switch p.current().typ {
	case tokInt:
		t := p.advance()
		v, err := strconv.ParseInt(t.val, 10, 64)
		if err != nil {
			return literal{}, fmt.Errorf("invalid integer %q: %w", t.val, err)
		}
		return literal{intVal: v, isInt: true}, nil
	case tokFloat:
		t := p.advance()
		v, err := strconv.ParseFloat(t.val, 64)
		if err != nil {
			return literal{}, fmt.Errorf("invalid float %q: %w", t.val, err)
		}
		return literal{floatVal: v, isFloat: true}, nil
	case tokString:
		t := p.advance()
		return literal{strVal: t.val, isString: true}, nil
	default:
		return literal{}, fmt.Errorf("expected value (integer, float, or string) at position %d, got %q", p.current().pos, p.current().val)
	}
}
