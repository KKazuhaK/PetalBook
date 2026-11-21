package page

import (
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// ExcerptRunes is the length of a page summary's excerpt.
	ExcerptRunes = 200
	// LabelRunes is where the label of an untitled page is cut.
	LabelRunes = 30
)

var (
	reHR        = regexp.MustCompile(`^([-*_]\s*){3,}$`)
	reSetext    = regexp.MustCompile(`^=+$`)
	reTableSep  = regexp.MustCompile(`^\|?\s*:?-{1,}:?\s*(\|\s*:?-{1,}:?\s*)*\|?$`)
	reQuote     = regexp.MustCompile(`^(\s*>)+ ?`)
	reHeading   = regexp.MustCompile(`^#{1,6}(\s+|$)`)
	reHeadClose = regexp.MustCompile(`\s+#+\s*$`)
	reList      = regexp.MustCompile(`^\s*([-*+]|\d{1,9}[.)])\s+`)
	reTask      = regexp.MustCompile(`^\[[ xX]\]\s+`)
	reFootDef   = regexp.MustCompile(`^\[\^[^\]]+\]:\s*`)
	reRefDef    = regexp.MustCompile(`^\[[^\]^]+\]:\s*\S+(\s+("[^"]*"|'[^']*'|\([^)]*\)))?\s*$`)
	reFootRef   = regexp.MustCompile(`\[\^[^\]]+\]`)
	reImage     = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	reLink      = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	reRefLink   = regexp.MustCompile(`\[([^\]]+)\]\[[^\]]*\]`)
	reAutolink  = regexp.MustCompile(`<((?:https?|mailto):[^>\s]+)>`)
	reBR        = regexp.MustCompile(`(?i)<br\s*/?>`)
	reTag       = regexp.MustCompile(`</?[A-Za-z][^>]*>|<!--.*?-->`)
	reCode      = regexp.MustCompile("`+([^`]+)`+")
	reEscape    = regexp.MustCompile("\\\\([!-/:-@\\[-`{-~])")
	reSpaces    = regexp.MustCompile(`[ \t\x{3000}]+`)
)

// markupChars lets a line without any Markdown punctuation skip the regular
// expressions, which is most lines of short copy.
const escapeBase = 0xF0000

const markupChars = "*_`[]<>!~\\#|&"

// PlainText strips Markdown syntax from body: heading marks, emphasis, link
// and image syntax (keeping the visible text), code fences, quotes, list
// markers, tables and HTML tags. It is not a Markdown parser; it only has to
// make a readable excerpt and something sensible to index. Lines are joined
// with "\n" and a blank line marks a paragraph break.
func PlainText(body string) string {
	var out []string
	inFence := false
	var fenceChar byte
	for _, raw := range strings.Split(body, "\n") {
		t := strings.TrimSpace(raw)
		if c, n := fenceRun(t); n >= 3 {
			if !inFence {
				inFence, fenceChar = true, c
				continue
			}
			if c == fenceChar && strings.Trim(t, string(c)) == "" {
				inFence = false
				continue
			}
		}
		if inFence {
			out = append(out, collapseSpaces(t))
			continue
		}
		l := plainLine(t)
		if l == skipLine {
			continue
		}
		out = append(out, l)
	}
	// Squeeze runs of blank lines to one and trim the ends.
	var b strings.Builder
	pendingBlank := false
	for _, l := range out {
		if l == "" {
			pendingBlank = b.Len() > 0
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
			if pendingBlank {
				b.WriteByte('\n')
			}
		}
		pendingBlank = false
		b.WriteString(l)
	}
	return b.String()
}

func fenceRun(t string) (byte, int) {
	if t == "" || (t[0] != '`' && t[0] != '~') {
		return 0, 0
	}
	n := 0
	for n < len(t) && t[n] == t[0] {
		n++
	}
	return t[0], n
}

func collapseSpaces(s string) string {
	return strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
}

// skipLine marks a line that carries no text and is not a paragraph break
// either, such as the separator row of a table.
const skipLine = "\x00skip"

func plainLine(t string) string {
	if t == "" || reHR.MatchString(t) || reSetext.MatchString(t) {
		return ""
	}
	if strings.Contains(t, "|") && reTableSep.MatchString(t) {
		return skipLine
	}
	if reRefDef.MatchString(t) {
		return skipLine
	}
	t = reQuote.ReplaceAllString(t, "")
	t = reHeading.ReplaceAllString(t, "")
	t = reHeadClose.ReplaceAllString(t, "")
	t = reList.ReplaceAllString(t, "")
	t = reTask.ReplaceAllString(t, "")
	t = reFootDef.ReplaceAllString(t, "")
	if strings.HasPrefix(t, "|") {
		t = strings.ReplaceAll(strings.Trim(t, "|"), "|", " ")
	}
	if strings.ContainsAny(t, markupChars) {
		t = reBR.ReplaceAllString(t, "")
		t = reAutolink.ReplaceAllString(t, "$1")
		t = reImage.ReplaceAllString(t, "$1")
		t = reLink.ReplaceAllString(t, "$1")
		t = reRefLink.ReplaceAllString(t, "$1")
		t = reFootRef.ReplaceAllString(t, "")
		t = reCode.ReplaceAllString(t, "$1")
		t = reTag.ReplaceAllString(t, "")
		// Escaped characters are literal text: hide them from the stripping
		// below by moving them into a private-use block, then bring them back.
		t = reEscape.ReplaceAllStringFunc(t, func(m string) string { return string(rune(escapeBase) + rune(m[1])) })
		t = stripEmphasis(t)
		t = strings.Map(func(r rune) rune {
			if r > escapeBase && r < escapeBase+128 {
				return r - escapeBase
			}
			return r
		}, t)
		if strings.Contains(t, "&") && strings.Contains(t, ";") {
			t = html.UnescapeString(t)
		}
	}
	return collapseSpaces(t)
}

// stripEmphasis drops *, ** and ~~ markers, and _ markers where they open or
// close a word, so snake_case survives.
func stripEmphasis(s string) string {
	s = strings.ReplaceAll(s, "*", "")
	s = strings.ReplaceAll(s, "~~", "")
	if !strings.Contains(s, "_") {
		return s
	}
	r := []rune(s)
	var b strings.Builder
	for i, c := range r {
		if c == '_' {
			prevWord := i > 0 && wordRune(r[i-1])
			nextWord := i+1 < len(r) && wordRune(r[i+1])
			// Inside a word (word on both sides) it is part of the text.
			if prevWord && nextWord {
				b.WriteRune(c)
			}
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

func wordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// Excerpt returns the first ExcerptRunes characters of the body as plain text.
// Line breaks inside the text are kept as single newlines; blank lines are not.
func Excerpt(body string) string {
	return cutRunes(strings.ReplaceAll(PlainText(body), "\n\n", "\n"), ExcerptRunes, false)
}

// Label is what a listing shows for a page: the title, or for a page without
// one the first line of the body cut to about LabelRunes characters. untitled
// says which. The label of an empty untitled page is "".
func Label(title, body string) (label string, untitled bool) {
	if t := strings.TrimSpace(title); t != "" {
		return t, false
	}
	for _, l := range strings.Split(PlainText(body), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return cutRunes(l, LabelRunes, true), true
		}
	}
	return "", true
}

func cutRunes(s string, n int, ellipsis bool) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	s = strings.TrimRightFunc(string([]rune(s)[:n]), unicode.IsSpace)
	if ellipsis {
		s += "…"
	}
	return s
}
