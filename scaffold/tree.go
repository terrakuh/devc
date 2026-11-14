package scaffold

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// A small JSONC tree that keeps comments, so the merged devcontainer.json still
// carries the explanations its blocks wrote. Comments are attached to the
// member they precede (or follow on the same line); comments at the very start
// of a document are dropped.

type kind int

const (
	scalar kind = iota
	object
	array
)

type node struct {
	kind    kind
	raw     string    // scalar: the JSON text as written
	members []*member // object fields or array items
	tail    []string  // comments after the last member
	from    string    // block that contributed the node, for conflict errors
}

type member struct {
	comments []string // comments above, verbatim
	key      string   // object fields only
	value    *node
	trailing string // comment on the same line, verbatim
}

type parser struct {
	src  string
	pos  int
	from string
}

func parseTree(src, from string) (*node, error) {
	p := &parser{src: src, from: from}
	if _, err := p.comments(); err != nil {
		return nil, err
	}
	n, err := p.value()
	if err != nil {
		return nil, err
	}
	if _, err := p.comments(); err != nil {
		return nil, err
	}
	if p.pos != len(p.src) {
		return nil, p.errorf("unexpected %q after the document", p.src[p.pos])
	}
	return n, nil
}

func (p *parser) errorf(format string, args ...any) error {
	line := 1 + strings.Count(p.src[:min(p.pos, len(p.src))], "\n")
	return fmt.Errorf("line %d: %s", line, fmt.Sprintf(format, args...))
}

// comments skips whitespace and returns the comments it passed.
func (p *parser) comments() ([]string, error) {
	var out []string
	for p.pos < len(p.src) {
		rest := p.src[p.pos:]
		switch {
		case strings.ContainsRune(" \t\r\n", rune(rest[0])):
			p.pos++
		case strings.HasPrefix(rest, "//"):
			out = append(out, p.lineComment())
		case strings.HasPrefix(rest, "/*"):
			end := strings.Index(rest[2:], "*/")
			if end < 0 {
				return nil, p.errorf("unterminated comment")
			}
			out = append(out, rest[:end+4])
			p.pos += end + 4
		default:
			return out, nil
		}
	}
	return out, nil
}

func (p *parser) lineComment() string {
	rest := p.src[p.pos:]
	end := strings.IndexByte(rest, '\n')
	if end < 0 {
		end = len(rest)
	}
	p.pos += end
	return strings.TrimRight(rest[:end], " \t\r")
}

// trailing consumes a // comment that follows on the same line.
func (p *parser) trailing() string {
	i := p.pos
	for i < len(p.src) && (p.src[i] == ' ' || p.src[i] == '\t') {
		i++
	}
	if !strings.HasPrefix(p.src[i:], "//") {
		return ""
	}
	p.pos = i
	return p.lineComment()
}

func (p *parser) value() (*node, error) {
	if p.pos >= len(p.src) {
		return nil, p.errorf("unexpected end of input")
	}
	switch p.src[p.pos] {
	case '{':
		return p.container(object, '}')
	case '[':
		return p.container(array, ']')
	case '"':
		raw, err := p.str()
		if err != nil {
			return nil, err
		}
		return &node{kind: scalar, raw: raw, from: p.from}, nil
	}
	start := p.pos
	for p.pos < len(p.src) && !strings.ContainsRune(" \t\r\n,]}/", rune(p.src[p.pos])) {
		p.pos++
	}
	raw := p.src[start:p.pos]
	if raw == "" || !json.Valid([]byte(raw)) {
		return nil, p.errorf("invalid value %q", raw)
	}
	return &node{kind: scalar, raw: raw, from: p.from}, nil
}

func (p *parser) str() (string, error) {
	start := p.pos
	for p.pos++; p.pos < len(p.src); p.pos++ {
		switch p.src[p.pos] {
		case '\\':
			p.pos++
		case '\n':
			return "", p.errorf("unterminated string")
		case '"':
			p.pos++
			raw := p.src[start:p.pos]
			if !json.Valid([]byte(raw)) {
				return "", p.errorf("invalid string %s", raw)
			}
			return raw, nil
		}
	}
	return "", p.errorf("unterminated string")
}

func (p *parser) container(k kind, closing byte) (*node, error) {
	n := &node{kind: k, from: p.from}
	p.pos++
	comma := false
	for {
		cs, err := p.comments()
		if err != nil {
			return nil, err
		}
		if p.pos >= len(p.src) {
			return nil, p.errorf("unexpected end of input")
		}
		if p.src[p.pos] == closing {
			p.pos++
			n.tail = cs
			return n, nil
		}
		if len(n.members) > 0 && !comma {
			return nil, p.errorf("expected ',' or '%c'", closing)
		}

		m := &member{comments: cs}
		if k == object {
			if p.src[p.pos] != '"' {
				return nil, p.errorf("expected a key")
			}
			raw, err := p.str()
			if err != nil {
				return nil, err
			}
			_ = json.Unmarshal([]byte(raw), &m.key)
			if _, err := p.comments(); err != nil {
				return nil, err
			}
			if p.pos >= len(p.src) || p.src[p.pos] != ':' {
				return nil, p.errorf("expected ':' after %s", raw)
			}
			p.pos++
			if _, err := p.comments(); err != nil {
				return nil, err
			}
		}
		if m.value, err = p.value(); err != nil {
			return nil, err
		}
		m.trailing = p.trailing()
		n.members = append(n.members, m)

		// A comma may follow, on the same line or after a line break.
		i := p.pos
		for i < len(p.src) && strings.ContainsRune(" \t\r\n", rune(p.src[i])) {
			i++
		}
		comma = i < len(p.src) && p.src[i] == ','
		if comma {
			p.pos = i + 1
			if t := p.trailing(); t != "" {
				m.trailing = t
			}
		}
	}
}

// merge folds src into dst: objects merge key by key, arrays gain the items
// they do not have yet, and scalars must agree.
func merge(dst, src *node, path string) error {
	if dst.kind != src.kind {
		return fmt.Errorf("%s: %s and %s give it different types", describe(path), dst.from, src.from)
	}
	switch dst.kind {
	case scalar:
		if canonical(dst) != canonical(src) {
			return fmt.Errorf("%s: %s sets %s but %s sets %s", describe(path), dst.from, dst.raw, src.from, src.raw)
		}
	case object:
		for _, sm := range src.members {
			dm := dst.field(sm.key)
			if dm == nil {
				dst.members = append(dst.members, sm)
				continue
			}
			// Both blocks fill this key, so a comment above it would describe
			// only part of what follows: move each down to its block's first entry.
			dm.pushDown()
			sm.pushDown()
			dm.adopt(sm)
			sub := sm.key
			if path != "" {
				sub = path + "." + sm.key
			}
			if err := merge(dm.value, sm.value, sub); err != nil {
				return err
			}
		}
	case array:
		for _, sm := range src.members {
			if dm := dst.item(canonical(sm.value)); dm != nil {
				dm.adopt(sm)
			} else {
				dst.members = append(dst.members, sm)
			}
		}
	}
	dst.tail = appendComments(dst.tail, src.tail)
	return nil
}

func describe(path string) string {
	if path == "" {
		return "devcontainer.json"
	}
	return fmt.Sprintf("devcontainer.json %q", path)
}

func (n *node) field(key string) *member {
	for _, m := range n.members {
		if m.key == key {
			return m
		}
	}
	return nil
}

func (n *node) item(canon string) *member {
	for _, m := range n.members {
		if canonical(m.value) == canon {
			return m
		}
	}
	return nil
}

// pushDown moves the comments above a non-empty object or array onto its first
// member.
func (m *member) pushDown() {
	if len(m.comments) == 0 || m.value.kind == scalar || len(m.value.members) == 0 {
		return
	}
	first := m.value.members[0]
	first.comments = append(m.comments, first.comments...)
	m.comments = nil
}

// adopt takes over the comments of a member that merged into m.
func (m *member) adopt(o *member) {
	m.comments = appendComments(m.comments, o.comments)
	if m.trailing == "" {
		m.trailing = o.trailing
	}
}

// appendComments appends add unless dst already holds it, so two blocks that
// set the same value with the same explanation print it once.
func appendComments(dst, add []string) []string {
	if len(add) == 0 || strings.Contains(strings.Join(dst, "\n"), strings.Join(add, "\n")) {
		return dst
	}
	return append(dst, add...)
}

// canonical is the value without comments or formatting, for comparison.
func canonical(n *node) string {
	var b strings.Builder
	n.write(&b, 0, false)
	var v any
	if err := json.Unmarshal([]byte(b.String()), &v); err != nil {
		return b.String()
	}
	out, _ := json.Marshal(v)
	return string(out)
}

// format prints the tree tab-indented, with comments.
func (n *node) format() string {
	var b strings.Builder
	n.write(&b, 0, true)
	b.WriteByte('\n')
	return b.String()
}

func (n *node) write(b *strings.Builder, depth int, comments bool) {
	if n.kind == scalar {
		b.WriteString(n.raw)
		return
	}
	open, closing := "{", "}"
	if n.kind == array {
		open, closing = "[", "]"
	}
	if len(n.members) == 0 && (!comments || len(n.tail) == 0) {
		b.WriteString(open + closing)
		return
	}
	in := strings.Repeat("\t", depth+1)
	b.WriteString(open + "\n")
	for i, m := range n.members {
		if comments {
			for _, c := range m.comments {
				b.WriteString(in + c + "\n")
			}
		}
		b.WriteString(in)
		if n.kind == object {
			b.WriteString(quote(m.key) + ": ")
		}
		m.value.write(b, depth+1, comments)
		if i < len(n.members)-1 {
			b.WriteByte(',')
		}
		if comments && m.trailing != "" {
			b.WriteString(" " + m.trailing)
		}
		b.WriteByte('\n')
	}
	if comments {
		for _, c := range n.tail {
			b.WriteString(in + c + "\n")
		}
	}
	b.WriteString(strings.Repeat("\t", depth) + closing)
}

// quote encodes s as a JSON string without escaping <, > and &.
func quote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}
