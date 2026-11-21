package page

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Crockford base32: digits and letters without I, L, O, U.
const idAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const (
	idLen     = 10
	idTimeLen = 6 // 32^6 seconds is about 34 years
)

// idEpoch keeps the time part small; ids sort by creation time until about 2054.
var idEpoch = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

var idPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{10}$`)

// ValidID reports whether s has the shape of a page id.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// NewID makes a page id: six characters of creation time (seconds, so ids sort
// roughly by age and neighbouring files in Git share a prefix) and four random
// ones (20 bits). Callers that need certainty check against existing ids.
func NewID(now time.Time) string {
	secs := int64(now.Sub(idEpoch) / time.Second)
	if secs < 0 {
		secs = 0
	}
	var b [idLen]byte
	for i := idTimeLen - 1; i >= 0; i-- {
		b[i] = idAlphabet[secs&31]
		secs >>= 5
	}
	var r [idLen - idTimeLen]byte
	if _, err := rand.Read(r[:]); err != nil {
		panic("page: no randomness: " + err.Error())
	}
	for i, v := range r {
		b[idTimeLen+i] = idAlphabet[v&31]
	}
	return string(b[:])
}

// ShortID is the part of an id used in the file name of an untitled page: the
// last four characters (the random ones), lower case.
func ShortID(id string) string {
	if len(id) < 4 {
		return strings.ToLower(id)
	}
	return strings.ToLower(id[len(id)-4:])
}

// Rev is the revision of a page file: the Git blob id of its content, so it
// matches what `git hash-object` prints and can be compared with Git's own
// view of the file. It is computed here, without running git.
func Rev(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}
