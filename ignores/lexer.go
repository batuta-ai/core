package ignores

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The lexer never evaluates code. It splits JavaScript and TypeScript source
// into strings, templates, regex literals, identifiers, numbers and
// punctuation, which is enough to tell what a config names without running it.

type lexKind int

const (
	tokIdent lexKind = iota
	tokNumber
	tokString // text holds the decoded value; template pieces use it too
	tokRegex
	tokPunct
)

type lexToken struct {
	kind lexKind
	text string
}

type span struct{ start, end int }

type lexed struct {
	tokens   []lexToken
	comments []span
}

// regexKeywords are the words after which a "/" starts a regex.
var regexKeywords = map[string]bool{
	"return": true, "typeof": true, "case": true, "in": true, "of": true, "new": true,
	"delete": true, "void": true, "throw": true, "instanceof": true, "yield": true,
}

// regexPrefixes are the punctuation after which a "/" starts a regex.
// "${" is included because a substitution opens an expression.
var regexPrefixes = map[string]bool{
	"(": true, ",": true, "=": true, ":": true, "[": true, "!": true,
	"&": true, "|": true, "?": true, "{": true, ";": true, "${": true,
}

func isIdentStart(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r)
}

func isIdentPart(r rune) bool {
	return isIdentStart(r) || unicode.IsDigit(r)
}

func lex(src string) (lexed, error) {
	l := lexer{src: src}
	if err := l.run(); err != nil {
		return lexed{}, err
	}
	return lexed{tokens: l.tokens, comments: l.comments}, nil
}

type lexer struct {
	src      string
	pos      int
	tokens   []lexToken
	comments []span
	// braces holds one entry per open "{" or "${": true for a template
	// substitution, whose closing "}" resumes the template text.
	braces []bool
}

func (l *lexer) run() error {
	for l.pos < len(l.src) {
		r, size := utf8.DecodeRuneInString(l.src[l.pos:])
		switch {
		case unicode.IsSpace(r):
			l.pos += size
		case strings.HasPrefix(l.src[l.pos:], "//"):
			l.lineComment()
		case strings.HasPrefix(l.src[l.pos:], "/*"):
			if err := l.blockComment(); err != nil {
				return err
			}
		case r == '\'' || r == '"':
			if err := l.quoted(r); err != nil {
				return err
			}
		case r == '`':
			l.pos += size
			if err := l.templateText(); err != nil {
				return err
			}
		case r == '/' && l.regexAllowed():
			if err := l.regex(); err != nil {
				return err
			}
		case isIdentStart(r):
			l.ident()
		case r >= '0' && r <= '9':
			l.number()
		case r == '}' && len(l.braces) > 0 && l.braces[len(l.braces)-1]:
			l.braces = l.braces[:len(l.braces)-1]
			l.emit(tokPunct, "}")
			l.pos += size
			if err := l.templateText(); err != nil {
				return err
			}
		default:
			l.punct(size)
		}
	}
	if slices.Contains(l.braces, true) {
		return errors.New("unterminated template")
	}
	return nil
}

func (l *lexer) emit(kind lexKind, text string) {
	l.tokens = append(l.tokens, lexToken{kind: kind, text: text})
}

func (l *lexer) lineComment() {
	end := strings.IndexByte(l.src[l.pos:], '\n')
	if end < 0 {
		end = len(l.src) - l.pos
	}
	l.comments = append(l.comments, span{l.pos, l.pos + end})
	l.pos += end
}

func (l *lexer) blockComment() error {
	end := strings.Index(l.src[l.pos+2:], "*/")
	if end < 0 {
		return errors.New("unterminated block comment")
	}
	stop := l.pos + 2 + end + 2
	l.comments = append(l.comments, span{l.pos, stop})
	l.pos = stop
	return nil
}

func (l *lexer) quoted(quote rune) error {
	var b strings.Builder
	i := l.pos + 1
	for i < len(l.src) {
		r, size := utf8.DecodeRuneInString(l.src[i:])
		switch {
		case r == quote:
			l.emit(tokString, b.String())
			l.pos = i + size
			return nil
		case r == '\n':
			return errors.New("unterminated string")
		case r == '\\':
			i += size
			if i >= len(l.src) {
				return errors.New("unterminated string")
			}
			var text string
			text, size = unescape(l.src[i:])
			b.WriteString(text)
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return errors.New("unterminated string")
}

// templateText reads template text from l.pos up to the closing backtick or
// the next "${", emitting the text as a string lexToken.
func (l *lexer) templateText() error {
	var b strings.Builder
	i := l.pos
	for i < len(l.src) {
		r, size := utf8.DecodeRuneInString(l.src[i:])
		switch {
		case r == '`':
			l.emit(tokString, b.String())
			l.pos = i + size
			return nil
		case r == '$' && strings.HasPrefix(l.src[i:], "${"):
			l.emit(tokString, b.String())
			l.emit(tokPunct, "${")
			l.braces = append(l.braces, true)
			l.pos = i + 2
			return nil
		case r == '\\':
			i += size
			if i >= len(l.src) {
				return errors.New("unterminated template")
			}
			var text string
			text, size = unescape(l.src[i:])
			b.WriteString(text)
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return errors.New("unterminated template")
}

// unescape decodes the escape that starts right after a backslash and returns
// its text with the bytes it used. A malformed \x, \u or \u{} escape keeps the
// character after the backslash, as any unknown escape does.
func unescape(s string) (string, int) {
	r, size := utf8.DecodeRuneInString(s)
	switch r {
	case 'n':
		return "\n", size
	case 't':
		return "\t", size
	case 'r':
		return "\r", size
	case 'x':
		if code, ok := hexValue(s, 1, 2); ok {
			return string(rune(code)), 3
		}
	case 'u':
		if strings.HasPrefix(s, "u{") {
			if end := strings.IndexByte(s, '}'); end > 2 {
				if code, ok := hexValue(s, 2, end-2); ok && code <= unicode.MaxRune {
					return string(rune(code)), end + 1
				}
			}
		} else if code, ok := hexValue(s, 1, 4); ok {
			return string(rune(code)), 5
		}
	}
	return string(r), size
}

// hexValue parses the n hex digits of s that start at from.
func hexValue(s string, from, n int) (uint64, bool) {
	if from+n > len(s) {
		return 0, false
	}
	for _, c := range []byte(s[from : from+n]) {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return 0, false
		}
	}
	code, err := strconv.ParseUint(s[from:from+n], 16, 32)
	return code, err == nil
}

func (l *lexer) regexAllowed() bool {
	if len(l.tokens) == 0 {
		return true
	}
	prev := l.tokens[len(l.tokens)-1]
	switch prev.kind {
	case tokPunct:
		return regexPrefixes[prev.text]
	case tokIdent:
		if !regexKeywords[prev.text] {
			return false
		}
		// A keyword after "." is a property name: `a.return / 2`.
		if len(l.tokens) >= 2 {
			return !isPunct(l.tokens[len(l.tokens)-2], ".")
		}
		return true
	}
	return false
}

func (l *lexer) regex() error {
	inClass := false
	i := l.pos + 1
	for i < len(l.src) {
		r, size := utf8.DecodeRuneInString(l.src[i:])
		switch {
		case r == '\n':
			return errors.New("unterminated regex")
		case r == '\\':
			i += size
			if i >= len(l.src) || l.src[i] == '\n' {
				return errors.New("unterminated regex")
			}
			_, size = utf8.DecodeRuneInString(l.src[i:])
		case r == '[':
			inClass = true
		case r == ']':
			inClass = false
		case r == '/' && !inClass:
			i += size
			for i < len(l.src) {
				f, fsize := utf8.DecodeRuneInString(l.src[i:])
				if !isIdentPart(f) {
					break
				}
				i += fsize
			}
			l.emit(tokRegex, l.src[l.pos:i])
			l.pos = i
			return nil
		}
		i += size
	}
	return errors.New("unterminated regex")
}

func (l *lexer) ident() {
	i := l.pos
	for i < len(l.src) {
		r, size := utf8.DecodeRuneInString(l.src[i:])
		if !isIdentPart(r) {
			break
		}
		i += size
	}
	l.emit(tokIdent, l.src[l.pos:i])
	l.pos = i
}

func (l *lexer) number() {
	i := l.pos
	for i < len(l.src) {
		r, size := utf8.DecodeRuneInString(l.src[i:])
		if !isIdentPart(r) && r != '.' {
			break
		}
		i += size
	}
	l.emit(tokNumber, l.src[l.pos:i])
	l.pos = i
}

func (l *lexer) punct(size int) {
	if strings.HasPrefix(l.src[l.pos:], "...") {
		l.emit(tokPunct, "...")
		l.pos += 3
		return
	}
	switch l.src[l.pos] {
	case '{':
		l.braces = append(l.braces, false)
	case '}':
		if len(l.braces) > 0 {
			l.braces = l.braces[:len(l.braces)-1]
		}
	}
	l.emit(tokPunct, l.src[l.pos:l.pos+size])
	l.pos += size
}

// stripComments removes line and block comments and keeps everything else,
// including comment markers inside strings, templates and regex literals.
// A line comment leaves its newline; a block comment becomes one space.
func stripComments(src string) (string, error) {
	lx, err := lex(src)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	last := 0
	for _, c := range lx.comments {
		b.WriteString(src[last:c.start])
		if strings.HasPrefix(src[c.start:], "/*") {
			b.WriteByte(' ')
		}
		last = c.end
	}
	b.WriteString(src[last:])
	return b.String(), nil
}

// stringLiterals returns the decoded value of every string literal and
// template piece of a stripped source, in order.
func stringLiterals(src string) ([]string, error) {
	lx, err := lex(src)
	if err != nil {
		return nil, err
	}
	return literalsIn(lx.tokens), nil
}

func literalsIn(tokens []lexToken) []string {
	var out []string
	for _, t := range tokens {
		if t.kind == tokString {
			out = append(out, t.text)
		}
	}
	return out
}

func isOpen(t lexToken) bool {
	return t.kind == tokPunct && (t.text == "{" || t.text == "[" || t.text == "(" || t.text == "${")
}

func isClose(t lexToken) bool {
	return t.kind == tokPunct && (t.text == "}" || t.text == "]" || t.text == ")")
}

func isPunct(t lexToken, text string) bool {
	return t.kind == tokPunct && t.text == text
}

// keyAt reports the name of the object key at tokens[i], which is an
// identifier or string after "{" or "," and followed by a colon.
func keyAt(tokens []lexToken, i int) (string, bool) {
	t := tokens[i]
	if t.kind != tokIdent && t.kind != tokString {
		return "", false
	}
	if i+1 >= len(tokens) || !isPunct(tokens[i+1], ":") {
		return "", false
	}
	if i == 0 || !(isPunct(tokens[i-1], "{") || isPunct(tokens[i-1], ",")) {
		return "", false
	}
	return t.text, true
}

// valueEnd returns the index one past the value that starts at from: it stops
// at a "," outside brackets or at the bracket that closes the enclosing object.
func valueEnd(tokens []lexToken, from int) int {
	depth := 0
	for i := from; i < len(tokens); i++ {
		switch {
		case isOpen(tokens[i]):
			depth++
		case isClose(tokens[i]):
			if depth == 0 {
				return i
			}
			depth--
		case depth == 0 && isPunct(tokens[i], ","):
			return i
		}
	}
	return len(tokens)
}

// keyValue finds the first object key called name. When the value is a single
// string literal it returns the literal; a value that is an expression is
// reported as found without a literal.
func keyValue(src, name string) (value string, literal, found bool, err error) {
	lx, err := lex(src)
	if err != nil {
		return "", false, false, err
	}
	for i := range lx.tokens {
		if key, ok := keyAt(lx.tokens, i); !ok || key != name {
			continue
		}
		valueStart := i + 2
		end := valueEnd(lx.tokens, valueStart)
		if end-valueStart == 1 && lx.tokens[valueStart].kind == tokString {
			return lx.tokens[valueStart].text, true, true, nil
		}
		return "", false, true, nil
	}
	return "", false, false, nil
}

// keyOccurrence is one object key found by keyLiterals. Siblings lists the
// other keys of the same object; a spread element appears as "...".
type keyOccurrence struct {
	Literals []string
	Siblings []string
}

// keyLiterals returns one occurrence for every object key called name.
func keyLiterals(src, name string) ([]keyOccurrence, error) {
	lx, err := lex(src)
	if err != nil {
		return nil, err
	}
	var out []keyOccurrence
	for i := range lx.tokens {
		if key, ok := keyAt(lx.tokens, i); !ok || key != name {
			continue
		}
		valueStart := i + 2
		end := valueEnd(lx.tokens, valueStart)
		out = append(out, keyOccurrence{
			Literals: literalsIn(lx.tokens[valueStart:end]),
			Siblings: siblingKeys(lx.tokens, i),
		})
	}
	return out, nil
}

// siblingKeys lists the keys of the object around tokens[at], other than the
// key at that index. A shorthand property or a method counts as a key.
func siblingKeys(tokens []lexToken, at int) []string {
	start := -1
	depth := 0
	for i := at - 1; i >= 0; i-- {
		if isClose(tokens[i]) {
			depth++
		} else if isOpen(tokens[i]) {
			if depth == 0 {
				start = i
				break
			}
			depth--
		}
	}
	var out []string
	depth = 0
	for i := start + 1; i < len(tokens); i++ {
		t := tokens[i]
		if isClose(t) {
			if depth == 0 {
				break
			}
			depth--
			continue
		}
		if depth == 0 && isPunct(t, "[") && startsMember(tokens, i, start) {
			if end := closingBracket(tokens, i+1); end+1 < len(tokens) && isPunct(tokens[end], "]") &&
				(isPunct(tokens[end+1], ":") || isPunct(tokens[end+1], "(")) {
				out = append(out, "[computed]")
				i = end
				continue
			}
		}
		if isOpen(t) {
			depth++
			continue
		}
		if depth != 0 || i == at {
			continue
		}
		if isPunct(t, "...") {
			out = append(out, "...")
			continue
		}
		if (t.kind == tokIdent || t.kind == tokString) && startsMember(tokens, i, start) && endsKey(tokens, i) {
			out = append(out, t.text)
		}
	}
	return out
}

// closingBracket returns the index of the "]" that closes the "[" just before
// from, counting nested brackets and ignoring commas, or len(tokens).
func closingBracket(tokens []lexToken, from int) int {
	depth := 0
	for i := from; i < len(tokens); i++ {
		switch {
		case isOpen(tokens[i]):
			depth++
		case isClose(tokens[i]):
			if depth == 0 {
				return i
			}
			depth--
		}
	}
	return len(tokens)
}

func startsMember(tokens []lexToken, i, start int) bool {
	return i-1 == start || isPunct(tokens[i-1], ",")
}

// endsKey reports whether the member name at i is followed by ":", by "(" for
// a method, or by the end of the member for a shorthand.
func endsKey(tokens []lexToken, i int) bool {
	if i+1 >= len(tokens) {
		return true
	}
	next := tokens[i+1]
	return isPunct(next, ":") || isPunct(next, "(") || isPunct(next, ",") || isClose(next)
}

// callLiterals returns the string literals inside the arguments of every call
// to name, in order.
func callLiterals(src, name string) ([]string, error) {
	lx, err := lex(src)
	if err != nil {
		return nil, err
	}
	var out []string
	for i, t := range lx.tokens {
		if t.kind != tokIdent || t.text != name || i+1 >= len(lx.tokens) || !isPunct(lx.tokens[i+1], "(") {
			continue
		}
		if i > 0 && lx.tokens[i-1].kind == tokIdent && lx.tokens[i-1].text == "function" {
			continue
		}
		depth := 0
		for j := i + 1; j < len(lx.tokens); j++ {
			if isOpen(lx.tokens[j]) {
				depth++
			} else if isClose(lx.tokens[j]) {
				depth--
				if depth == 0 {
					if isMethodDefinition(lx.tokens, i, j) {
						break
					}
					out = append(out, literalsIn(lx.tokens[i+2:j])...)
					break
				}
			}
		}
	}
	return out, nil
}

var memberModifiers = map[string]bool{
	"async": true, "static": true, "public": true, "private": true,
	"protected": true, "readonly": true, "get": true, "set": true,
}

// isMethodDefinition reports whether the name at tokens[name] with its
// parameters closing at tokens[closeAt] defines a method rather than a call: a
// body "{" follows, or a ":" and a return type up to a "{" when the name starts
// a member.
func isMethodDefinition(tokens []lexToken, name, closeAt int) bool {
	if closeAt+1 >= len(tokens) {
		return false
	}
	if isPunct(tokens[closeAt+1], "{") {
		return true
	}
	if !isPunct(tokens[closeAt+1], ":") || !startsMemberName(tokens, name) {
		return false
	}
	depth := 0
	for i := closeAt + 2; i < len(tokens); i++ {
		t := tokens[i]
		switch {
		case depth == 0 && isPunct(t, "{"):
			return true
		case isOpen(t):
			depth++
		case isClose(t):
			if depth == 0 {
				return false
			}
			depth--
		case depth == 0 && (isPunct(t, ";") || isPunct(t, ",") || isPunct(t, "=")):
			return false
		}
	}
	return false
}

// startsMemberName reports whether the token before tokens[i] begins a class
// or object member: a separator or a member modifier.
func startsMemberName(tokens []lexToken, i int) bool {
	if i == 0 {
		return false
	}
	prev := tokens[i-1]
	if prev.kind == tokIdent {
		return memberModifiers[prev.text]
	}
	return isPunct(prev, "{") || isPunct(prev, "}") || isPunct(prev, ";") || isPunct(prev, ",")
}
