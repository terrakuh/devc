// Package jsonc converts JSONC (JSON with comments and trailing commas, the
// dialect VS Code uses for devcontainer.json) into plain JSON that
// encoding/json accepts.
//
// Strip replaces every comment byte and every trailing comma with a space
// rather than deleting it, so byte offsets are preserved: a json.SyntaxError
// reported against the stripped output still points at the right position in
// the original source.
package jsonc

import "fmt"

// Strip returns src with // line comments, /* block */ comments, and trailing
// commas (a comma before a closing } or ]) replaced by spaces. Comments and
// commas inside string literals are left untouched. The returned slice has the
// same length as src.
//
// It returns an error only for an unterminated string or block comment; malformed
// JSON that is otherwise lexically closed is left for the JSON parser to report.
func Strip(src []byte) ([]byte, error) {
	out := make([]byte, len(src))
	copy(out, src)

	const (
		normal = iota
		inString
		inLineComment
		inBlockComment
	)
	state := normal

	// Track the offset of the most recent non-space, non-comment byte so a
	// comma can be blanked once we see the following '}' or ']'.
	lastSignificant := -1
	lastSignificantByte := byte(0)
	// Offset of a pending comma that we may still need to blank.
	pendingComma := -1

	blank := func(i int) { out[i] = ' ' }

	for i := 0; i < len(src); i++ {
		c := src[i]
		switch state {
		case inString:
			if c == '\\' {
				i++ // skip escaped byte
				continue
			}
			if c == '"' {
				state = normal
				lastSignificant, lastSignificantByte = i, c
			}
		case inLineComment:
			if c == '\n' {
				state = normal
			} else {
				blank(i)
			}
		case inBlockComment:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				blank(i)
				blank(i + 1)
				i++
				state = normal
			} else {
				blank(i)
			}
		default: // normal
			switch {
			case c == '/' && i+1 < len(src) && src[i+1] == '/':
				blank(i)
				blank(i + 1)
				i++
				state = inLineComment
			case c == '/' && i+1 < len(src) && src[i+1] == '*':
				blank(i)
				blank(i + 1)
				i++
				state = inBlockComment
			case c == '"':
				state = inString
				pendingComma = -1
				lastSignificant, lastSignificantByte = i, c
			case c == ',':
				pendingComma = i
				lastSignificant, lastSignificantByte = i, c
			case c == '}' || c == ']':
				// A comma immediately preceding this close (only whitespace or
				// comments between) is trailing; blank it.
				if pendingComma >= 0 && lastSignificantByte == ',' && lastSignificant == pendingComma {
					blank(pendingComma)
				}
				pendingComma = -1
				lastSignificant, lastSignificantByte = i, c
			case c == ' ' || c == '\t' || c == '\r' || c == '\n':
				// whitespace: does not change significance tracking
			default:
				pendingComma = -1
				lastSignificant, lastSignificantByte = i, c
			}
		}
	}

	switch state {
	case inString:
		return nil, fmt.Errorf("jsonc: unterminated string literal")
	case inBlockComment:
		return nil, fmt.Errorf("jsonc: unterminated block comment")
	}
	return out, nil
}
