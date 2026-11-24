package api

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Attribution is what a line of the administrators' own text says it is from.
type attribution struct {
	Text, Author, Source string
}

// attributionMarks are what may stand between a text and who said it, with
// the spaces around them: a dash inside a word or a sentence has none.
var attributionMarks = []string{" —— ", " — ", " -- "}

// latinParticles are the small words a name may have in lower case.
var latinParticles = map[string]bool{"de": true, "da": true, "del": true, "della": true, "di": true, "van": true, "von": true, "der": true, "den": true,
	"la": true, "le": true, "du": true, "dos": true, "bin": true, "ibn": true, "al": true, "el": true, "of": true, "the": true, "and": true, "y": true, "e": true}

// parseAttribution cuts the attribution off the end of a line:
//
//	text —— Author《Source》     text — Author, Source     text -- Author
//
// Only the last mark counts, and what follows it must look like a name (and a
// source): a short run of words without a sentence's punctuation, in Chinese a
// name of a few characters or anything with 《…》. A line that does not have
// that is text only, so a dash inside a sentence is never taken for one. The
// split is shown to the administrators, who can correct it.
func parseAttribution(line string) attribution {
	line = strings.TrimSpace(line)
	at, mark := -1, ""
	for _, m := range attributionMarks {
		if i := strings.LastIndex(line, m); i > at {
			at, mark = i, m
		}
	}
	if at <= 0 {
		return attribution{Text: line}
	}
	text, rest := strings.TrimSpace(line[:at]), strings.TrimSpace(line[at+len(mark):])
	if text == "" || rest == "" {
		return attribution{Text: line}
	}
	author, source, ok := readAttribution(rest)
	if !ok {
		return attribution{Text: line}
	}
	return attribution{Text: text, Author: author, Source: source}
}

func readAttribution(rest string) (author, source string, ok bool) {
	if strings.ContainsAny(rest, "\r\n") {
		return "", "", false // an attribution is the end of the last line
	}
	if open := strings.Index(rest, "《"); open >= 0 {
		end := strings.Index(rest[open:], "》")
		if end < 0 {
			return "", "", false
		}
		end += open
		source = strings.TrimSpace(rest[open+len("《") : end])
		author = strings.TrimSpace(rest[:open])
		if source == "" || strings.TrimSpace(rest[end+len("》"):]) != "" || utf8.RuneCountInString(source) > 60 || utf8.RuneCountInString(author) > 40 || sentencey(author) {
			return "", "", false
		}
		return author, source, true
	}
	if sentencey(rest) || strings.ContainsAny(rest, "《》") {
		return "", "", false
	}
	first, _ := utf8.DecodeRuneInString(rest)
	if unicode.Is(unicode.Han, first) {
		n := utf8.RuneCountInString(rest)
		if n > 12 && !(strings.ContainsRune(rest, '·') && n <= 20) {
			return "", "", false
		}
		return rest, "", true
	}
	// Latin and the like: "Robert Frost", "J. R. R. Tolkien", "Maya Angelou, Letters".
	name, src, _ := strings.Cut(rest, ", ")
	words := strings.Fields(name)
	if len(words) == 0 || len(words) > 5 || utf8.RuneCountInString(rest) > 60 {
		return "", "", false
	}
	for i, w := range words {
		r, _ := utf8.DecodeRuneInString(w)
		if unicode.IsUpper(r) || unicode.IsDigit(r) || unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			continue
		}
		if i > 0 && latinParticles[strings.ToLower(strings.Trim(w, ".,"))] {
			continue
		}
		return "", "", false
	}
	return strings.TrimSpace(name), strings.TrimSpace(src), true
}

// sentencey reports whether s has the punctuation that ends or splits a
// sentence (a name does not).
func sentencey(s string) bool {
	trimmed := strings.TrimRight(s, ".")
	return strings.ContainsAny(trimmed, "。！？!?；;：:，、\"“”") || strings.Contains(trimmed, "—") || strings.Contains(trimmed, " -- ")
}
