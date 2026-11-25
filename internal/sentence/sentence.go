// Package sentence writes what the server says to people as sentences: a
// message that was made from another error (or from a name) has no capital or
// no full stop, or a second one.
package sentence

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// End gives msg a full stop unless it already ends a sentence, and makes
// a doubled one ("..") single. An ellipsis stays.
func End(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return msg
	}
	for strings.HasSuffix(msg, "..") && !strings.HasSuffix(msg, "...") {
		msg = msg[:len(msg)-1]
	}
	switch last, _ := utf8.DecodeLastRuneInString(msg); last {
	case '.', '?', '!', '…', '。', '？', '！':
		return msg
	}
	return msg + "."
}

// Of is End with a capital first letter, for a message made from the text of
// another error (which is written in lower case, as Go's are).
func Of(msg string) string {
	msg = End(msg)
	r, n := utf8.DecodeRuneInString(msg)
	if r == utf8.RuneError || !unicode.IsLower(r) {
		return msg
	}
	return string(unicode.ToUpper(r)) + msg[n:]
}
