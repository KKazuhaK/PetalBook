package page

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	maxStemRunes = 80
	// Most file systems cap a name at 255 bytes; leave room for ".md" and
	// a " 12" collision suffix.
	maxStemBytes = 200
)

var reservedNames = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}

func init() {
	for i := 1; i <= 9; i++ {
		reservedNames[fmt.Sprintf("COM%d", i)] = true
		reservedNames[fmt.Sprintf("LPT%d", i)] = true
	}
}

// illegalNameRune reports characters that cannot appear in a file name on at
// least one of Windows, macOS and Linux.
func illegalNameRune(r rune) bool {
	return r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"/\|?*`, r) || r == unicode.ReplacementChar
}

// FileStem turns a title into a file name without the extension. It returns
// "" when nothing usable is left, in which case the caller falls back to
// UntitledStem.
func FileStem(title string) string {
	// NFC, so the same title is the same name however it was typed; file
	// systems differ on whether composed and decomposed forms are one name.
	title = norm.NFC.String(title)
	s := strings.Map(func(r rune) rune {
		if illegalNameRune(r) {
			return -1
		}
		return r
	}, title)
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, ".")
	s = trimStem(s)
	if utf8.RuneCountInString(s) > maxStemRunes {
		s = string([]rune(s)[:maxStemRunes])
	}
	for len(s) > maxStemBytes {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	s = trimStem(s)
	if reservedNames[strings.ToUpper(s)] {
		s += "_"
	}
	return s
}

// trimStem removes what Windows does not allow at the end of a name.
func trimStem(s string) string {
	return strings.TrimRightFunc(strings.TrimSpace(s), func(r rune) bool { return r == '.' || unicode.IsSpace(r) })
}

// UntitledStem is the file name of a page without a title: the creation date
// and the short id, like 2026-11-21-k3f9. created is a YYYY-MM-DD string; when
// it is not a date, now is used.
func UntitledStem(created string, id string, now time.Time) string {
	day := now.UTC().Format("2006-01-02")
	if t, err := time.Parse("2006-01-02", created); err == nil {
		day = t.Format("2006-01-02")
	}
	return day + "-" + ShortID(id)
}

// NumberedStem is stem for n == 1 and "stem 2", "stem 3"... after that.
func NumberedStem(stem string, n int) string {
	if n <= 1 {
		return stem
	}
	return fmt.Sprintf("%s %d", stem, n)
}

var numberedSuffix = regexp.MustCompile(` \d+$`)

// SameStem reports whether base (a file name without extension) is stem or
// stem with a collision number, so a page that already has such a name is not
// renamed again.
func SameStem(base, stem string) bool {
	if base == stem {
		return true
	}
	if strings.HasPrefix(base, stem+" ") {
		return numberedSuffix.MatchString(base) && numberedSuffix.ReplaceAllString(base, "") == stem
	}
	return false
}

// NameKey is what file names are compared by: NFC and case-folded, the
// coarsest equality any of the common file systems uses.
func NameKey(name string) string { return strings.ToLower(norm.NFC.String(name)) }
