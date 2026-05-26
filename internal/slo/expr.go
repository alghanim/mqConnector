package slo

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// expr.go — tiny PromQL subset just rich enough to evaluate the rules
// shipped in deploy/prometheus/mqconnector-slos.yaml.
//
// Grammar (recursive-descent, left-associative for operators of equal
// precedence):
//
//   expr        ← orExpr
//   orExpr      ← andExpr ( ('or' | 'unless') andExpr )*
//   andExpr     ← cmpExpr ( 'and' cmpExpr )*
//   cmpExpr     ← addExpr ( ('==' | '!=' | '>=' | '<=' | '>' | '<') addExpr )?
//   addExpr     ← mulExpr ( ('+' | '-') mulExpr )*
//   mulExpr     ← unaryExpr ( ('*' | '/') unaryExpr )*
//   unaryExpr   ← '-'? primary
//   primary     ← number | funcCall | vectorSelector | '(' expr ')'
//   funcCall    ← IDENT '(' [ args ] ')' [ 'by' '(' labelList ')' ]
//   args        ← expr ( ',' expr )*
//   vectorSelector ← IDENT [ '{' labelMatchers '}' ] [ '[' DURATION ']' ]
//
// Functions implemented: rate, sum, histogram_quantile, clamp_min,
// clamp_max. Anything else returns an error from the evaluator and
// causes the rule to be skipped that tick.
//
// Value model:
//
//   - All numeric primaries evaluate to a *vector* (a slice of
//     labelSample). A bare number is a one-sample vector with no
//     labels.
//   - Arithmetic between two vectors matches label sets on identical
//     non-empty labels; the matching is a simple equality check (no
//     on()/ignoring() support). Arithmetic between a vector and a
//     scalar broadcasts the scalar across every sample.
//   - Comparisons filter the LHS vector to samples where the
//     comparison is true.
//   - Logical: AND keeps LHS samples whose label set matches a sample
//     in RHS; OR returns the union; UNLESS removes LHS samples that
//     match any RHS sample.
//
// The expression evaluator never returns NaN/Inf out the top — those
// are filtered to "no match" so an alert isn't tricked into firing on
// a divide-by-zero.

// labelSample is one element of a vector — a label set and a value.
type labelSample struct {
	labels map[string]string
	value  float64
}

// vector is the result of every evaluation. An alert FIRES if its
// vector is non-empty after the boolean comparison filtering.
type vector []labelSample

// ─── lexer ────────────────────────────────────────────────────────

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokNumber
	tokIdent
	tokString
	tokDuration
	tokLParen
	tokRParen
	tokLBrace
	tokRBrace
	tokLBracket
	tokRBracket
	tokComma
	tokPlus
	tokMinus
	tokStar
	tokSlash
	tokEq
	tokNeq
	tokGt
	tokGte
	tokLt
	tokLte
	tokAssign
)

type token struct {
	kind tokenKind
	text string
	pos  int
}

type lexer struct {
	src string
	pos int
}

func newLexer(s string) *lexer { return &lexer{src: s} }

func (l *lexer) tokenize() ([]token, error) {
	var out []token
	for {
		t, err := l.next()
		if err != nil {
			return nil, err
		}
		out = append(out, t)
		if t.kind == tokEOF {
			return out, nil
		}
	}
}

func (l *lexer) next() (token, error) {
	for l.pos < len(l.src) && unicode.IsSpace(rune(l.src[l.pos])) {
		l.pos++
	}
	if l.pos >= len(l.src) {
		return token{kind: tokEOF, pos: l.pos}, nil
	}
	start := l.pos
	c := l.src[l.pos]

	switch c {
	case '(':
		l.pos++
		return token{kind: tokLParen, text: "(", pos: start}, nil
	case ')':
		l.pos++
		return token{kind: tokRParen, text: ")", pos: start}, nil
	case '{':
		l.pos++
		return token{kind: tokLBrace, text: "{", pos: start}, nil
	case '}':
		l.pos++
		return token{kind: tokRBrace, text: "}", pos: start}, nil
	case '[':
		l.pos++
		return token{kind: tokLBracket, text: "[", pos: start}, nil
	case ']':
		l.pos++
		return token{kind: tokRBracket, text: "]", pos: start}, nil
	case ',':
		l.pos++
		return token{kind: tokComma, text: ",", pos: start}, nil
	case '+':
		l.pos++
		return token{kind: tokPlus, text: "+", pos: start}, nil
	case '-':
		l.pos++
		return token{kind: tokMinus, text: "-", pos: start}, nil
	case '*':
		l.pos++
		return token{kind: tokStar, text: "*", pos: start}, nil
	case '/':
		l.pos++
		return token{kind: tokSlash, text: "/", pos: start}, nil
	case '=':
		l.pos++
		if l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			return token{kind: tokEq, text: "==", pos: start}, nil
		}
		return token{kind: tokAssign, text: "=", pos: start}, nil
	case '!':
		l.pos++
		if l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			return token{kind: tokNeq, text: "!=", pos: start}, nil
		}
		return token{}, fmt.Errorf("expected '!=' at pos %d", start)
	case '>':
		l.pos++
		if l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			return token{kind: tokGte, text: ">=", pos: start}, nil
		}
		return token{kind: tokGt, text: ">", pos: start}, nil
	case '<':
		l.pos++
		if l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			return token{kind: tokLte, text: "<=", pos: start}, nil
		}
		return token{kind: tokLt, text: "<", pos: start}, nil
	case '"':
		return l.readString()
	}

	if isDigit(c) || (c == '.' && l.pos+1 < len(l.src) && isDigit(l.src[l.pos+1])) {
		return l.readNumberOrDuration()
	}
	if isIdentStart(c) {
		return l.readIdent()
	}
	return token{}, fmt.Errorf("unexpected character %q at pos %d", c, start)
}

func (l *lexer) readString() (token, error) {
	start := l.pos
	l.pos++
	var b strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == '\\' && l.pos+1 < len(l.src) {
			b.WriteByte(l.src[l.pos+1])
			l.pos += 2
			continue
		}
		if c == '"' {
			l.pos++
			return token{kind: tokString, text: b.String(), pos: start}, nil
		}
		b.WriteByte(c)
		l.pos++
	}
	return token{}, fmt.Errorf("unterminated string at pos %d", start)
}

func (l *lexer) readNumberOrDuration() (token, error) {
	start := l.pos
	hasDot := false
	hasE := false
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case isDigit(c):
			l.pos++
		case c == '.' && !hasDot && !hasE:
			hasDot = true
			l.pos++
		case (c == 'e' || c == 'E') && !hasE:
			hasE = true
			l.pos++
			if l.pos < len(l.src) && (l.src[l.pos] == '+' || l.src[l.pos] == '-') {
				l.pos++
			}
		default:
			goto done
		}
	}
done:
	if l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == 's' || c == 'm' || c == 'h' || c == 'd' || c == 'w' || c == 'y' {
			l.pos++
			if l.pos < len(l.src) && l.src[l.pos] == 's' && c == 'm' {
				l.pos++
			}
			return token{kind: tokDuration, text: l.src[start:l.pos], pos: start}, nil
		}
	}
	return token{kind: tokNumber, text: l.src[start:l.pos], pos: start}, nil
}

func (l *lexer) readIdent() (token, error) {
	start := l.pos
	for l.pos < len(l.src) && (isIdentPart(l.src[l.pos]) || l.src[l.pos] == ':') {
		l.pos++
	}
	return token{kind: tokIdent, text: l.src[start:l.pos], pos: start}, nil
}

func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isIdentStart(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' }
func isIdentPart(c byte) bool  { return isIdentStart(c) || isDigit(c) }

// ─── parser ───────────────────────────────────────────────────────

type astNode interface{ isNode() }

type numberNode struct{ value float64 }
type vectorSelectorNode struct {
	name     string
	matchers map[string]string
	duration string
}
type binaryNode struct {
	op       tokenKind
	opString string
	lhs, rhs astNode
}
type unaryMinusNode struct{ inner astNode }
type funcCallNode struct {
	name     string
	args     []astNode
	byLabels []string
}

func (numberNode) isNode()         {}
func (vectorSelectorNode) isNode() {}
func (binaryNode) isNode()         {}
func (unaryMinusNode) isNode()     {}
func (funcCallNode) isNode()       {}

type parser struct {
	toks []token
	pos  int
}

// parsePromQL parses src into an AST node. Exported as a helper for
// tests; production code goes via the cached node on each Rule.
func parsePromQL(src string) (astNode, error) {
	l := newLexer(src)
	toks, err := l.tokenize()
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	n, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tokEOF {
		return nil, fmt.Errorf("trailing input at pos %d: %q", p.peek().pos, p.peek().text)
	}
	return n, nil
}

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) eat() token  { t := p.toks[p.pos]; p.pos++; return t }
func (p *parser) accept(k tokenKind) bool {
	if p.peek().kind == k {
		p.eat()
		return true
	}
	return false
}
func (p *parser) expect(k tokenKind) (token, error) {
	t := p.peek()
	if t.kind != k {
		return token{}, fmt.Errorf("expected token kind %d, got %q at pos %d", k, t.text, t.pos)
	}
	p.eat()
	return t, nil
}

func (p *parser) acceptKeyword(kw string) bool {
	t := p.peek()
	if t.kind == tokIdent && strings.EqualFold(t.text, kw) {
		p.eat()
		return true
	}
	return false
}
func (p *parser) peekKeyword(kw string) bool {
	t := p.peek()
	return t.kind == tokIdent && strings.EqualFold(t.text, kw)
}

func (p *parser) parseExpr() (astNode, error) { return p.parseOr() }

func (p *parser) parseOr() (astNode, error) {
	lhs, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		switch {
		case p.acceptKeyword("or"):
			rhs, err := p.parseAnd()
			if err != nil {
				return nil, err
			}
			lhs = binaryNode{opString: "or", lhs: lhs, rhs: rhs}
		case p.acceptKeyword("unless"):
			rhs, err := p.parseAnd()
			if err != nil {
				return nil, err
			}
			lhs = binaryNode{opString: "unless", lhs: lhs, rhs: rhs}
		default:
			return lhs, nil
		}
	}
}

func (p *parser) parseAnd() (astNode, error) {
	lhs, err := p.parseCmp()
	if err != nil {
		return nil, err
	}
	for p.acceptKeyword("and") {
		rhs, err := p.parseCmp()
		if err != nil {
			return nil, err
		}
		lhs = binaryNode{opString: "and", lhs: lhs, rhs: rhs}
	}
	return lhs, nil
}

func (p *parser) parseCmp() (astNode, error) {
	lhs, err := p.parseAdd()
	if err != nil {
		return nil, err
	}
	switch p.peek().kind {
	case tokEq, tokNeq, tokGt, tokGte, tokLt, tokLte:
		op := p.eat()
		rhs, err := p.parseAdd()
		if err != nil {
			return nil, err
		}
		return binaryNode{op: op.kind, opString: op.text, lhs: lhs, rhs: rhs}, nil
	}
	return lhs, nil
}

func (p *parser) parseAdd() (astNode, error) {
	lhs, err := p.parseMul()
	if err != nil {
		return nil, err
	}
	for {
		switch p.peek().kind {
		case tokPlus, tokMinus:
			op := p.eat()
			rhs, err := p.parseMul()
			if err != nil {
				return nil, err
			}
			lhs = binaryNode{op: op.kind, opString: op.text, lhs: lhs, rhs: rhs}
		default:
			return lhs, nil
		}
	}
}

func (p *parser) parseMul() (astNode, error) {
	lhs, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		switch p.peek().kind {
		case tokStar, tokSlash:
			op := p.eat()
			rhs, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			lhs = binaryNode{op: op.kind, opString: op.text, lhs: lhs, rhs: rhs}
		default:
			return lhs, nil
		}
	}
}

func (p *parser) parseUnary() (astNode, error) {
	if p.accept(tokMinus) {
		inner, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		return unaryMinusNode{inner: inner}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (astNode, error) {
	t := p.peek()
	switch t.kind {
	case tokNumber:
		p.eat()
		v, err := strconv.ParseFloat(t.text, 64)
		if err != nil {
			return nil, fmt.Errorf("bad number %q at pos %d: %w", t.text, t.pos, err)
		}
		return numberNode{value: v}, nil
	case tokLParen:
		p.eat()
		inner, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tokRParen); err != nil {
			return nil, err
		}
		return inner, nil
	case tokIdent:
		name := t.text
		p.eat()
		// `sum by (labels) (expr)` form — by clause BEFORE args.
		if p.peekKeyword("by") {
			p.eat()
			labels, err := p.parseLabelList()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(tokLParen); err != nil {
				return nil, err
			}
			args, err := p.parseArgs()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(tokRParen); err != nil {
				return nil, err
			}
			return funcCallNode{name: name, args: args, byLabels: labels}, nil
		}
		if p.peek().kind == tokLParen {
			p.eat()
			args, err := p.parseArgs()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(tokRParen); err != nil {
				return nil, err
			}
			var byLabels []string
			if p.peekKeyword("by") {
				p.eat()
				byLabels, err = p.parseLabelList()
				if err != nil {
					return nil, err
				}
			}
			return funcCallNode{name: name, args: args, byLabels: byLabels}, nil
		}
		sel := vectorSelectorNode{name: name}
		if p.accept(tokLBrace) {
			m, err := p.parseLabelMatchers()
			if err != nil {
				return nil, err
			}
			sel.matchers = m
		}
		if p.accept(tokLBracket) {
			dt := p.eat()
			if dt.kind != tokDuration {
				return nil, fmt.Errorf("expected duration after '[' at pos %d", dt.pos)
			}
			sel.duration = dt.text
			if _, err := p.expect(tokRBracket); err != nil {
				return nil, err
			}
		}
		return sel, nil
	}
	return nil, fmt.Errorf("unexpected token %q at pos %d", t.text, t.pos)
}

func (p *parser) parseArgs() ([]astNode, error) {
	if p.peek().kind == tokRParen {
		return nil, nil
	}
	var out []astNode
	for {
		n, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		out = append(out, n)
		if !p.accept(tokComma) {
			return out, nil
		}
	}
}

func (p *parser) parseLabelList() ([]string, error) {
	if _, err := p.expect(tokLParen); err != nil {
		return nil, err
	}
	var out []string
	if p.peek().kind == tokRParen {
		p.eat()
		return out, nil
	}
	for {
		t := p.peek()
		if t.kind != tokIdent {
			return nil, fmt.Errorf("expected label name at pos %d", t.pos)
		}
		p.eat()
		out = append(out, t.text)
		if !p.accept(tokComma) {
			break
		}
	}
	if _, err := p.expect(tokRParen); err != nil {
		return nil, err
	}
	return out, nil
}

func (p *parser) parseLabelMatchers() (map[string]string, error) {
	m := map[string]string{}
	if p.peek().kind == tokRBrace {
		p.eat()
		return m, nil
	}
	for {
		name := p.peek()
		if name.kind != tokIdent {
			return nil, fmt.Errorf("expected label name at pos %d", name.pos)
		}
		p.eat()
		if !p.accept(tokAssign) {
			return nil, fmt.Errorf("only `=` matchers supported (at pos %d)", name.pos)
		}
		val := p.peek()
		if val.kind != tokString {
			return nil, fmt.Errorf("expected string value at pos %d", val.pos)
		}
		p.eat()
		m[name.text] = val.text
		if !p.accept(tokComma) {
			break
		}
	}
	if _, err := p.expect(tokRBrace); err != nil {
		return nil, err
	}
	return m, nil
}

// ─── evaluator ────────────────────────────────────────────────────

// nanos is a private alias used for clarity in historyAt signatures
// without leaking time.Duration import into the parser-only paths.
type nanos = int64

// evalContext carries everything an expression needs.
type evalContext struct {
	// current is the metric vector at "now". A label set
	// {"__name__":"…", …other labels} per row.
	current []labelSample
	// historyAt returns the cumulative counter value at the timestamp
	// older by `ago` for the same (name + labels) pair. Returns
	// (value, true) when a sample exists; (0, false) means rate
	// can't be computed.
	historyAt func(name string, labels map[string]string, ago nanos) (float64, bool)
	// recordings is keyed by recording-rule name; resolves
	// `mqconnector:availability:ratio5m` → its AST. Cycle-broken via
	// `expanding` set.
	recordings map[string]astNode
	expanding  map[string]bool
}

// evalNode dispatches by AST node type.
func evalNode(n astNode, ctx *evalContext) (vector, error) {
	switch nd := n.(type) {
	case numberNode:
		return vector{{labels: map[string]string{}, value: nd.value}}, nil
	case unaryMinusNode:
		inner, err := evalNode(nd.inner, ctx)
		if err != nil {
			return nil, err
		}
		out := make(vector, len(inner))
		for i, s := range inner {
			out[i] = labelSample{labels: s.labels, value: -s.value}
		}
		return out, nil
	case vectorSelectorNode:
		return evalSelector(nd, ctx)
	case binaryNode:
		return evalBinary(nd, ctx)
	case funcCallNode:
		return evalFunc(nd, ctx)
	default:
		return nil, fmt.Errorf("unsupported expression node %T", n)
	}
}

func evalSelector(sel vectorSelectorNode, ctx *evalContext) (vector, error) {
	// Recording rule reference?
	if def, ok := ctx.recordings[sel.name]; ok && !ctx.expanding[sel.name] {
		ctx.expanding[sel.name] = true
		defer delete(ctx.expanding, sel.name)
		v, err := evalNode(def, ctx)
		if err != nil {
			return nil, fmt.Errorf("recording-rule %q: %w", sel.name, err)
		}
		return filterByMatchers(v, sel.matchers), nil
	}
	out := make(vector, 0)
	for _, s := range ctx.current {
		if s.labels["__name__"] != sel.name {
			continue
		}
		if !matchesMatchers(s.labels, sel.matchers) {
			continue
		}
		out = append(out, labelSample{
			labels: copyLabelsWithout(s.labels, "__name__"),
			value:  s.value,
		})
	}
	return out, nil
}

func matchesMatchers(labels, matchers map[string]string) bool {
	for k, v := range matchers {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func filterByMatchers(v vector, matchers map[string]string) vector {
	if len(matchers) == 0 {
		return v
	}
	out := make(vector, 0, len(v))
	for _, s := range v {
		if matchesMatchers(s.labels, matchers) {
			out = append(out, s)
		}
	}
	return out
}

func copyLabelsWithout(in map[string]string, omit string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if k == omit {
			continue
		}
		out[k] = v
	}
	return out
}

func evalBinary(b binaryNode, ctx *evalContext) (vector, error) {
	switch strings.ToLower(b.opString) {
	case "and":
		l, err := evalNode(b.lhs, ctx)
		if err != nil {
			return nil, err
		}
		r, err := evalNode(b.rhs, ctx)
		if err != nil {
			return nil, err
		}
		return vectorAnd(l, r), nil
	case "or":
		l, err := evalNode(b.lhs, ctx)
		if err != nil {
			return nil, err
		}
		r, err := evalNode(b.rhs, ctx)
		if err != nil {
			return nil, err
		}
		return vectorOr(l, r), nil
	case "unless":
		l, err := evalNode(b.lhs, ctx)
		if err != nil {
			return nil, err
		}
		r, err := evalNode(b.rhs, ctx)
		if err != nil {
			return nil, err
		}
		return vectorUnless(l, r), nil
	}

	l, err := evalNode(b.lhs, ctx)
	if err != nil {
		return nil, err
	}
	r, err := evalNode(b.rhs, ctx)
	if err != nil {
		return nil, err
	}
	switch b.op {
	case tokEq, tokNeq, tokGt, tokGte, tokLt, tokLte:
		return vectorCompare(l, r, b.op), nil
	}
	return vectorArith(l, r, b.op)
}

func vectorAnd(l, r vector) vector {
	out := make(vector, 0)
	for _, ls := range l {
		for _, rs := range r {
			if labelsEqual(ls.labels, rs.labels) {
				out = append(out, ls)
				break
			}
		}
	}
	return out
}
func vectorOr(l, r vector) vector {
	out := append(vector(nil), l...)
	for _, rs := range r {
		dup := false
		for _, ls := range l {
			if labelsEqual(ls.labels, rs.labels) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, rs)
		}
	}
	return out
}
func vectorUnless(l, r vector) vector {
	out := make(vector, 0)
	for _, ls := range l {
		drop := false
		for _, rs := range r {
			if labelsEqual(ls.labels, rs.labels) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, ls)
		}
	}
	return out
}

func labelsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func vectorCompare(l, r vector, op tokenKind) vector {
	if len(r) == 1 && len(r[0].labels) == 0 {
		threshold := r[0].value
		out := make(vector, 0, len(l))
		for _, s := range l {
			if compareFloats(s.value, threshold, op) {
				out = append(out, s)
			}
		}
		return out
	}
	out := make(vector, 0)
	for _, ls := range l {
		for _, rs := range r {
			if labelsEqual(ls.labels, rs.labels) {
				if compareFloats(ls.value, rs.value, op) {
					out = append(out, ls)
				}
				break
			}
		}
	}
	return out
}

func compareFloats(a, b float64, op tokenKind) bool {
	switch op {
	case tokEq:
		return a == b
	case tokNeq:
		return a != b
	case tokGt:
		return a > b
	case tokGte:
		return a >= b
	case tokLt:
		return a < b
	case tokLte:
		return a <= b
	}
	return false
}

func vectorArith(l, r vector, op tokenKind) (vector, error) {
	if len(l) == 1 && len(l[0].labels) == 0 && len(r) == 1 && len(r[0].labels) == 0 {
		v, err := arith(l[0].value, r[0].value, op)
		if err != nil {
			return nil, err
		}
		return vector{{labels: map[string]string{}, value: v}}, nil
	}
	if len(r) == 1 && len(r[0].labels) == 0 {
		out := make(vector, 0, len(l))
		for _, s := range l {
			v, err := arith(s.value, r[0].value, op)
			if err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
				out = append(out, labelSample{labels: s.labels, value: v})
			}
		}
		return out, nil
	}
	if len(l) == 1 && len(l[0].labels) == 0 {
		out := make(vector, 0, len(r))
		for _, s := range r {
			v, err := arith(l[0].value, s.value, op)
			if err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
				out = append(out, labelSample{labels: s.labels, value: v})
			}
		}
		return out, nil
	}
	out := make(vector, 0)
	for _, ls := range l {
		for _, rs := range r {
			if !labelsEqual(ls.labels, rs.labels) {
				continue
			}
			v, err := arith(ls.value, rs.value, op)
			if err != nil {
				break
			}
			if math.IsNaN(v) || math.IsInf(v, 0) {
				break
			}
			out = append(out, labelSample{labels: ls.labels, value: v})
			break
		}
	}
	return out, nil
}

func arith(a, b float64, op tokenKind) (float64, error) {
	switch op {
	case tokPlus:
		return a + b, nil
	case tokMinus:
		return a - b, nil
	case tokStar:
		return a * b, nil
	case tokSlash:
		if b == 0 {
			return math.NaN(), nil
		}
		return a / b, nil
	}
	return 0, fmt.Errorf("unsupported arithmetic operator")
}

// ─── functions ────────────────────────────────────────────────────

func evalFunc(c funcCallNode, ctx *evalContext) (vector, error) {
	switch strings.ToLower(c.name) {
	case "rate":
		return evalRate(c, ctx)
	case "sum":
		return evalSum(c, ctx)
	case "clamp_min":
		return evalClamp(c, ctx, true)
	case "clamp_max":
		return evalClamp(c, ctx, false)
	case "histogram_quantile":
		return evalHistogramQuantile(c, ctx)
	case "avg", "min", "max", "count":
		return evalAggregator(c, ctx, c.name)
	}
	return nil, fmt.Errorf("unsupported function %q", c.name)
}

func evalRate(c funcCallNode, ctx *evalContext) (vector, error) {
	if len(c.args) != 1 {
		return nil, fmt.Errorf("rate(): want 1 arg, got %d", len(c.args))
	}
	sel, ok := c.args[0].(vectorSelectorNode)
	if !ok {
		return nil, fmt.Errorf("rate(): argument must be a vector selector with a [duration] window")
	}
	if sel.duration == "" {
		return nil, fmt.Errorf("rate(): missing [duration] window")
	}
	winSec, err := parseDurationSeconds(sel.duration)
	if err != nil {
		return nil, fmt.Errorf("rate(): bad window %q: %w", sel.duration, err)
	}
	cur, err := evalSelector(vectorSelectorNode{name: sel.name, matchers: sel.matchers}, ctx)
	if err != nil {
		return nil, err
	}
	out := make(vector, 0, len(cur))
	winNs := nanos(winSec * float64(1_000_000_000))
	for _, s := range cur {
		if ctx.historyAt == nil {
			out = append(out, labelSample{labels: s.labels, value: 0})
			continue
		}
		prev, ok := ctx.historyAt(sel.name, s.labels, winNs)
		if !ok {
			out = append(out, labelSample{labels: s.labels, value: 0})
			continue
		}
		delta := s.value - prev
		if delta < 0 {
			delta = 0
		}
		out = append(out, labelSample{labels: s.labels, value: delta / winSec})
	}
	return out, nil
}

func parseDurationSeconds(s string) (float64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	unit := s[len(s)-1]
	num := s[:len(s)-1]
	if len(s) >= 2 && s[len(s)-2:] == "ms" {
		unit = 'M'
		num = s[:len(s)-2]
	}
	n, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, err
	}
	switch unit {
	case 's':
		return n, nil
	case 'm':
		return n * 60, nil
	case 'h':
		return n * 3600, nil
	case 'd':
		return n * 86400, nil
	case 'w':
		return n * 7 * 86400, nil
	case 'y':
		return n * 365 * 86400, nil
	case 'M':
		return n / 1000, nil
	}
	return 0, fmt.Errorf("unknown duration unit %q", string(unit))
}

func evalSum(c funcCallNode, ctx *evalContext) (vector, error) {
	if len(c.args) != 1 {
		return nil, fmt.Errorf("sum(): want 1 arg, got %d", len(c.args))
	}
	in, err := evalNode(c.args[0], ctx)
	if err != nil {
		return nil, err
	}
	return aggregate(in, c.byLabels, "sum"), nil
}

func evalAggregator(c funcCallNode, ctx *evalContext, name string) (vector, error) {
	if len(c.args) != 1 {
		return nil, fmt.Errorf("%s(): want 1 arg, got %d", name, len(c.args))
	}
	in, err := evalNode(c.args[0], ctx)
	if err != nil {
		return nil, err
	}
	return aggregate(in, c.byLabels, strings.ToLower(name)), nil
}

func aggregate(in vector, byLabels []string, op string) vector {
	type bucket struct {
		labels map[string]string
		values []float64
	}
	groups := map[string]*bucket{}
	keyOrder := []string{}
	for _, s := range in {
		key, sub := groupKey(s.labels, byLabels)
		if _, ok := groups[key]; !ok {
			groups[key] = &bucket{labels: sub}
			keyOrder = append(keyOrder, key)
		}
		groups[key].values = append(groups[key].values, s.value)
	}
	out := make(vector, 0, len(groups))
	for _, k := range keyOrder {
		b := groups[k]
		var v float64
		switch op {
		case "sum":
			for _, x := range b.values {
				v += x
			}
		case "avg":
			if len(b.values) == 0 {
				continue
			}
			for _, x := range b.values {
				v += x
			}
			v /= float64(len(b.values))
		case "min":
			v = math.Inf(+1)
			for _, x := range b.values {
				if x < v {
					v = x
				}
			}
		case "max":
			v = math.Inf(-1)
			for _, x := range b.values {
				if x > v {
					v = x
				}
			}
		case "count":
			v = float64(len(b.values))
		}
		out = append(out, labelSample{labels: b.labels, value: v})
	}
	return out
}

func groupKey(labels map[string]string, by []string) (string, map[string]string) {
	if by == nil {
		return "", map[string]string{}
	}
	sub := make(map[string]string, len(by))
	keys := make([]string, 0, len(by))
	for _, k := range by {
		v := labels[k]
		sub[k] = v
		keys = append(keys, k+"="+v)
	}
	sort.Strings(keys)
	return strings.Join(keys, ","), sub
}

func evalClamp(c funcCallNode, ctx *evalContext, isMin bool) (vector, error) {
	if len(c.args) != 2 {
		return nil, fmt.Errorf("clamp_*(): want 2 args, got %d", len(c.args))
	}
	in, err := evalNode(c.args[0], ctx)
	if err != nil {
		return nil, err
	}
	bound, err := evalNode(c.args[1], ctx)
	if err != nil {
		return nil, err
	}
	if len(bound) != 1 || len(bound[0].labels) != 0 {
		return nil, fmt.Errorf("clamp_*(): second arg must be a scalar")
	}
	limit := bound[0].value
	out := make(vector, len(in))
	for i, s := range in {
		v := s.value
		if isMin {
			if v < limit {
				v = limit
			}
		} else {
			if v > limit {
				v = limit
			}
		}
		out[i] = labelSample{labels: s.labels, value: v}
	}
	return out, nil
}

// evalHistogramQuantile reads bucket samples by (le, …byLabels) and
// linearly interpolates the requested quantile.
func evalHistogramQuantile(c funcCallNode, ctx *evalContext) (vector, error) {
	if len(c.args) != 2 {
		return nil, fmt.Errorf("histogram_quantile(): want 2 args, got %d", len(c.args))
	}
	q, err := evalNode(c.args[0], ctx)
	if err != nil {
		return nil, err
	}
	if len(q) != 1 || len(q[0].labels) != 0 {
		return nil, fmt.Errorf("histogram_quantile(): first arg must be a scalar quantile")
	}
	quantile := q[0].value
	rhs, err := evalNode(c.args[1], ctx)
	if err != nil {
		return nil, err
	}
	type bucket struct {
		labels map[string]string
		points []histPoint
	}
	groups := map[string]*bucket{}
	order := []string{}
	for _, s := range rhs {
		leStr, ok := s.labels["le"]
		if !ok {
			continue
		}
		le, err := parseLEBound(leStr)
		if err != nil {
			continue
		}
		sub := make(map[string]string, len(s.labels)-1)
		for k, v := range s.labels {
			if k == "le" {
				continue
			}
			sub[k] = v
		}
		key := stableKey(sub)
		if _, ok := groups[key]; !ok {
			groups[key] = &bucket{labels: sub}
			order = append(order, key)
		}
		groups[key].points = append(groups[key].points, histPoint{le: le, count: s.value})
	}
	out := make(vector, 0, len(groups))
	for _, k := range order {
		b := groups[k]
		sort.Slice(b.points, func(i, j int) bool { return b.points[i].le < b.points[j].le })
		if len(b.points) == 0 {
			continue
		}
		total := b.points[len(b.points)-1].count
		if total <= 0 {
			out = append(out, labelSample{labels: b.labels, value: 0})
			continue
		}
		target := quantile * total
		var prevLE, prevCount float64
		var resolved bool
		for _, p := range b.points {
			if p.count >= target {
				bucketSize := p.le - prevLE
				bucketCount := p.count - prevCount
				if bucketCount <= 0 || math.IsInf(p.le, 1) {
					out = append(out, labelSample{labels: b.labels, value: prevLE})
				} else {
					frac := (target - prevCount) / bucketCount
					out = append(out, labelSample{labels: b.labels, value: prevLE + bucketSize*frac})
				}
				resolved = true
				break
			}
			prevLE = p.le
			prevCount = p.count
		}
		if !resolved {
			out = append(out, labelSample{labels: b.labels, value: prevLE})
		}
	}
	return out, nil
}

type histPoint struct {
	le    float64
	count float64
}

func parseLEBound(s string) (float64, error) {
	if s == "+Inf" {
		return math.Inf(+1), nil
	}
	return strconv.ParseFloat(s, 64)
}

func stableKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
		b.WriteByte(',')
	}
	return b.String()
}
