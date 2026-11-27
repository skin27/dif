package dilsyntax

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// tokenKind is the kind of a token of DIL text.
type tokenKind int

const (
	tokEOF    tokenKind = iota
	tokWord             // a bare word: a name, a keyword, a URI or a number
	tokString           // a quoted ("..." or '...') or raw (`...`) string, decoded
	tokArrow            // ->
	tokLParen           // (
	tokRParen           // )
	tokLBrace           // {
	tokRBrace           // }
	tokLBrack           // [
	tokRBrack           // ]
	tokComma            // ,
	tokColon            // :
)

// Pos is a position in DIL text: line and column, both from 1. The column
// counts characters, not bytes.
type Pos struct{ Line, Col int }

type token struct {
	kind tokenKind
	text string // the word, or the decoded string
	pos  Pos
}

// describe names a token for an error message. It never shows the text of a
// word or a string: either can hold a credential.
func (t token) describe() string {
	switch t.kind {
	case tokEOF:
		return "end of input"
	case tokWord:
		return "a word"
	case tokString:
		return "a string"
	case tokArrow:
		return "'->'"
	}
	return "'" + string("????(){}[],:"[t.kind]) + "'"
}

// syntaxError is an error at a position of the text.
type syntaxError struct {
	pos Pos
	msg string
}

func (e *syntaxError) Error() string { return fmt.Sprintf("%d:%d: %s", e.pos.Line, e.pos.Col, e.msg) }

// isWordRune reports whether r can be part of a bare word. Words are names,
// keywords, step URIs (such as service:orders:PlaceOrder or
// https://host:9002/path) and numbers.
func isWordRune(r rune) bool {
	if r < utf8.RuneSelf {
		return 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || strings.ContainsRune("_-.:/@+*?=&%~!$", r)
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// lexer splits DIL text into tokens.
type lexer struct {
	src  string
	off  int
	line int
	col  int
}

func newLexer(src []byte) *lexer {
	s := strings.TrimPrefix(string(src), "\xef\xbb\xbf") // a byte order mark
	return &lexer{src: s, line: 1, col: 1}
}

func (l *lexer) peekRune() (rune, int) {
	if l.off >= len(l.src) {
		return -1, 0
	}
	return utf8.DecodeRuneInString(l.src[l.off:])
}

func (l *lexer) advance() rune {
	r, n := l.peekRune()
	l.off += n
	if r == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return r
}

func (l *lexer) errorf(pos Pos, format string, args ...any) error {
	return &syntaxError{pos, fmt.Sprintf(format, args...)}
}

// next returns the next token.
func (l *lexer) next() (token, error) {
	for {
		r, _ := l.peekRune()
		switch {
		case r == ' ' || r == '\t' || r == '\r' || r == '\n':
			l.advance()
			continue
		case r == '#':
			for r != -1 && r != '\n' {
				l.advance()
				r, _ = l.peekRune()
			}
			continue
		}
		break
	}
	pos := Pos{l.line, l.col}
	r, _ := l.peekRune()
	switch r {
	case -1:
		return token{kind: tokEOF, pos: pos}, nil
	case '(', ')', '{', '}', '[', ']', ',', ':':
		l.advance()
		kind := map[rune]tokenKind{'(': tokLParen, ')': tokRParen, '{': tokLBrace, '}': tokRBrace, '[': tokLBrack, ']': tokRBrack, ',': tokComma, ':': tokColon}[r]
		return token{kind: kind, pos: pos}, nil
	case '"', '\'':
		s, err := l.quoted(pos)
		return token{kind: tokString, text: s, pos: pos}, err
	case '`':
		s, err := l.raw(pos)
		return token{kind: tokString, text: s, pos: pos}, err
	}
	if strings.HasPrefix(l.src[l.off:], "->") {
		l.advance()
		l.advance()
		return token{kind: tokArrow, pos: pos}, nil
	}
	if !isWordRune(r) {
		if r > ' ' && r < utf8.RuneSelf {
			return token{}, l.errorf(pos, "unexpected character '%c'", r)
		}
		return token{}, l.errorf(pos, "unexpected character")
	}
	start := l.off
	for {
		r, _ := l.peekRune()
		if !isWordRune(r) || strings.HasPrefix(l.src[l.off:], "->") {
			break
		}
		l.advance()
	}
	word := l.src[start:l.off]
	// A word never ends with a colon: in "name: value" the colon separates.
	// (A word never starts with one either: a colon there is a token.)
	if trimmed := strings.TrimRight(word, ":"); trimmed != word {
		back := len(word) - len(trimmed)
		l.off -= back
		l.col -= back
		word = trimmed
	}
	return token{kind: tokWord, text: word, pos: pos}, nil
}

// quoted reads a string in double or single quotes. Escapes are those of
// JSON, plus \' . A string in quotes ends on its line.
func (l *lexer) quoted(pos Pos) (string, error) {
	quote := l.advance()
	var b strings.Builder
	for {
		r, _ := l.peekRune()
		switch r {
		case -1, '\n':
			return "", l.errorf(pos, "string not closed on its line (use `...` for text of several lines)")
		case quote:
			l.advance()
			return b.String(), nil
		case '\\':
			escPos := Pos{l.line, l.col}
			l.advance()
			e := l.advance()
			switch e {
			case '"', '\'', '\\', '/':
				b.WriteRune(e)
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'u':
				c, ok := l.hex4()
				if !ok {
					return "", l.errorf(escPos, "invalid \\u escape in string")
				}
				if 0xD800 <= c && c < 0xDC00 && strings.HasPrefix(l.src[l.off:], `\u`) {
					save, line, col := l.off, l.line, l.col
					l.advance()
					l.advance()
					if c2, ok := l.hex4(); ok && 0xDC00 <= c2 && c2 < 0xE000 {
						c = 0x10000 + (c-0xD800)<<10 + (c2 - 0xDC00)
					} else {
						l.off, l.line, l.col = save, line, col
					}
				}
				b.WriteRune(c)
			default:
				return "", l.errorf(escPos, "invalid escape in string")
			}
		default:
			l.advance()
			b.WriteRune(r)
		}
	}
}

func (l *lexer) hex4() (rune, bool) {
	var c rune
	for range 4 {
		r, _ := l.peekRune()
		var d rune
		switch {
		case '0' <= r && r <= '9':
			d = r - '0'
		case 'a' <= r && r <= 'f':
			d = r - 'a' + 10
		case 'A' <= r && r <= 'F':
			d = r - 'A' + 10
		default:
			return 0, false
		}
		l.advance()
		c = c<<4 | d
	}
	return c, true
}

// raw reads a string in backquotes. It may span lines, has no escapes and
// drops carriage returns, so a file with CRLF line ends means the same.
func (l *lexer) raw(pos Pos) (string, error) {
	l.advance()
	end := strings.IndexByte(l.src[l.off:], '`')
	if end < 0 {
		return "", l.errorf(pos, "raw string not closed")
	}
	s := l.src[l.off : l.off+end]
	for l.off < len(l.src) && l.src[l.off] != '`' {
		l.advance()
	}
	l.advance()
	return strings.ReplaceAll(s, "\r", ""), nil
}
