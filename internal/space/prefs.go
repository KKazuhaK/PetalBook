package space

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"

	"go.yaml.in/yaml/v3"
)

// PrefsFile is where a person's own preferences are kept, in their own personal
// space: the columns and widths of the tables they use, the sort, whether
// folders come first, which parts of the tour they have seen, and the like. It
// is a file of the repository (so it travels with it) and nothing of a page:
// the index does not see it, no page's history lists it, and the settings stamp
// of the offline sync leaves it out. Who may read it is not decided here but by
// whom the space may be opened for (a personal space only by its person; see
// api/prefs.go, which never takes a space from the request).
const PrefsFile = ".petalbook/prefs.yml"

// MaxPrefsBytes is the most the file holds.
const MaxPrefsBytes = 256 << 10

const prefsHead = "Preferences of the owner of this space (table columns, sort, tour). Written by the server\nin quiet commits; it can be deleted, and the app starts again from its defaults."

// ReadPrefs is what the file says, an empty map when there is none or it cannot
// be used (a preference is never worth failing for). It reads the file each
// time: it is small, and may have come in with a pull.
func (s *Space) ReadPrefs() (map[string]any, error) {
	if s.evicted {
		return nil, unavailable(nil, "the space has no files here right now")
	}
	return readPrefsFile(s.root)
}

func readPrefsFile(root string) (map[string]any, error) {
	data, _, blocked, err := readIn(root, PrefsFile, MaxPrefsBytes)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if data == nil || blocked != "" {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&m); err != nil || m == nil {
		return map[string]any{}, nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = jsonable(v)
	}
	return out, nil
}

// jsonable turns what YAML read into what JSON can say: keys that are not
// text become text.
func jsonable(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = jsonable(e)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[fmt.Sprint(k)] = jsonable(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = jsonable(e)
		}
		return out
	}
	return v
}

// encodePrefs is the file for prefs: nothing when there are none.
func encodePrefs(prefs map[string]any) ([]byte, error) {
	if len(prefs) == 0 {
		return nil, nil
	}
	doc := &yaml.Node{}
	if err := doc.Encode(prefs); err != nil {
		return nil, err
	}
	return yaml.Marshal(&yaml.Node{Kind: yaml.DocumentNode, HeadComment: prefsHead, Content: []*yaml.Node{doc}})
}

// PrefsSize is how large the file would be with these preferences (to be
// held to MaxPrefsBytes before a write is taken).
func PrefsSize(prefs map[string]any) (int, error) {
	data, err := encodePrefs(prefs)
	return len(data), err
}

// Writable says whether a change of the space could be made now: not while it
// has no files here, nor while the server's storage policy holds it read only.
func (s *Space) Writable() error {
	if s.evicted {
		return unavailable(nil, "the space has no files here right now")
	}
	if s.gate != nil {
		return s.gate()
	}
	return nil
}

// WritePrefs lets edit change what the file says and writes it back if edit
// says it changed (a change of the space like any other: journaled, committed
// by the committer's quiet period, pushed when the space follows a remote). It
// is for a few writes a day: the caller batches (api/prefs.go). With nothing
// left the file goes.
func (s *Space) WritePrefs(by Author, edit func(prefs map[string]any) (changed bool)) error {
	if s.evicted {
		return unavailable(nil, "the space has no files here right now")
	}
	_, err := mutate(s, by, func(tx *txn) (struct{}, error) {
		cur, err := readPrefsFile(s.root)
		if err != nil {
			return struct{}{}, err
		}
		if !edit(cur) {
			return struct{}{}, nil
		}
		data, err := encodePrefs(cur)
		if err != nil {
			return struct{}{}, err
		}
		if len(data) > MaxPrefsBytes {
			return struct{}{}, invalidf("The preferences are larger than %d bytes", MaxPrefsBytes)
		}
		if _, err := s.clearWayIn(tx, PrefsFile, PrefsFile); err != nil {
			return struct{}{}, err
		}
		if len(data) == 0 {
			return struct{}{}, s.removeFileIn(tx, PrefsFile)
		}
		return struct{}{}, s.writeFileIn(tx, PrefsFile, data)
	})
	return err
}
