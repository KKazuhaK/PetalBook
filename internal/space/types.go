package space

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

// A page type is a file of the repository, .petalbook/types/<name>.json: a
// JSON Schema of which the server understands a small part (the kinds of
// field below). The file name, without .json, is the value of `type` in a
// page's header and never changes; the title inside is what people see.
//
// The server changes only the keys it understands. Everything else in the file
// (other keywords of a field, of the schema, the order of the fields) stays as
// it was, so a schema written by hand or by another tool is not lost.
const (
	typesDir = ".petalbook/types"

	// MaxTypes is the most types a space has, MaxTypeFile the most of a type's
	// file that is read or written, MaxTypeFields the most fields of a type.
	MaxTypes      = 100
	MaxTypeFile   = 256 << 10
	MaxTypeFields = 200

	maxTypeName   = 100
	maxFieldKey   = 64
	maxLabelBytes = 200
	maxOptions    = 500
)

// The kinds of field.
const (
	KindText        = "text"
	KindLongText    = "longtext"
	KindSelect      = "select"
	KindMultiSelect = "multiselect"
	KindDate        = "date"
	KindNumber      = "number"
	KindCheckbox    = "checkbox"
	KindURL         = "url"
	// KindOther is a field the server does not recognise: it is shown and
	// edited as the YAML it is in the page header, and never rewritten.
	KindOther = "other"
)

var fieldKinds = map[string]bool{KindText: true, KindLongText: true, KindSelect: true, KindMultiSelect: true,
	KindDate: true, KindNumber: true, KindCheckbox: true, KindURL: true, KindOther: true}

// TypeField is one field of a type.
type TypeField struct {
	Key     string
	Title   string // "" when the schema gives none
	Kind    string
	Options []string // select and multiselect
	// Schema is the field's schema as the file has it.
	Schema json.RawMessage
}

// TypeInfo is a type as the file says it. When the file cannot be used, Error
// says why and nothing else is set but the name.
type TypeInfo struct {
	Name   string
	Title  string
	Fields []TypeField
	Error  string
	// Rev is the Git blob id of the file's bytes, as the rev of a page is:
	// it changes whenever the file does, and PutType and DeleteType must be
	// given the one they were based on. A file too large or not a regular
	// file has no bytes to take it from and is UnreadableRev.
	Rev string
}

// UnreadableRev is the Rev of a type file that cannot be read at all (see
// TypeInfo.Rev); a change to such a file goes unnoticed, but it can only be
// deleted, which is the way to fix it.
const UnreadableRev = "unreadable"

// TypeConflictError means the type is not as the request thought it was:
// another change came first. Current is the type as it is now, nil when the
// type is gone.
type TypeConflictError struct{ Current *TypeInfo }

func (e *TypeConflictError) Error() string { return "the type was changed by someone else" }

// typeNow is the type in file data (as listed), for a conflict. data is nil
// for a file that is not there, which has no type to show.
func typeNow(name string, data []byte, readErr error) *TypeInfo {
	if data == nil && readErr == nil {
		return nil
	}
	t := TypeInfo{Name: name, Rev: UnreadableRev}
	if readErr == nil {
		t.Rev = page.Rev(data)
		if p, _, err := parseType(name, data); err == nil {
			t = p
			t.Rev = page.Rev(data)
			return &t
		}
	}
	t.Error = typeFileProblem(name).Error()
	return &t
}

// FieldInput is a field as a request gives it. An empty Kind keeps the kind
// the field has (a new field must say one); Options are those of a select or
// multiselect.
type FieldInput struct {
	Key     string
	Title   string
	Kind    string
	Options []string
}

// TypeInput is the body of PutType: the title, and the fields in the order
// they are to have. A field the type has that is not listed is removed from
// the type.
type TypeInput struct {
	Title  string
	Fields []FieldInput
}

// errTypeFile says a type's file is there but cannot be used as it is.
var errTypeFile = errors.New("the type file cannot be read")

func typeFileProblem(name string) error {
	return unavailable(nil, "%s/%s.json cannot be used as it is (it must be a JSON object with a properties object, a regular file, under %d KiB, with at most %d fields), and an earlier version of it could not be looked up just now; try again",
		typesDir, name, MaxTypeFile>>10, MaxTypeFields)
}

// ValidTypeName reports whether name can be the name of a type: it is a file
// name, and the value of a page's type.
func ValidTypeName(name string) error {
	switch {
	case name == "" || len(name) > maxTypeName || !utf8.ValidString(name):
		return invalidf("A type name is 1 to %d bytes of text.", maxTypeName)
	case strings.HasPrefix(name, ".") || strings.TrimSpace(name) != name || strings.TrimRight(name, ". ") != name:
		return invalidf("A type name must not start with a dot or start or end with a space or dot.")
	case strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"/\|?*`, r) }) >= 0:
		return invalidf("A type name must not have a character that file systems do not allow.")
	}
	return nil
}

// validFieldKey checks the key of a field: not a field the application owns,
// not a reserved one, 1 to 64 bytes, no control characters and no colon.
func validFieldKey(key string) error {
	switch {
	case key == "" || len(key) > maxFieldKey || !utf8.ValidString(key):
		return invalidf("A field key is 1 to %d bytes of text.", maxFieldKey)
	case page.IsBuiltinKey(key):
		return invalidf("%q is a field of every page and cannot be a field of a type.", key)
	case page.IsReservedKey(key):
		return invalidf("%q is reserved and cannot be a field of a type.", key)
	case strings.IndexFunc(key, func(r rune) bool { return r < 0x20 || r == 0x7f || r == ':' }) >= 0:
		return invalidf("A field key must not have a control character or a colon.")
	}
	return nil
}

func validLabel(what, s string) error {
	if len(s) > maxLabelBytes || !utf8.ValidString(s) || strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return invalidf("%s must be text of at most %d bytes on one line.", what, maxLabelBytes)
	}
	return nil
}

// --- ordered JSON -------------------------------------------------------------

// kv is a member of a JSON object, its value as written.
type kv struct {
	key string
	raw json.RawMessage
}

// decodeObject reads a JSON object keeping the order of its members and the
// text of their values. Repeated keys are an error, as is anything after the
// object.
func decodeObject(data []byte) ([]kv, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("not an object")
	}
	out := []kv{}
	seen := map[string]bool{}
	for dec.More() {
		t, err := dec.Token()
		key, ok := t.(string)
		if err != nil || !ok {
			return nil, errors.New("bad key")
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate key %q", key)
		}
		seen[key] = true
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		out = append(out, kv{key, raw})
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return nil, errors.New("not an object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("more than one value")
	}
	return out, nil
}

func lookup(o []kv, key string) (json.RawMessage, int) {
	for i, m := range o {
		if m.key == key {
			return m.raw, i
		}
	}
	return nil, -1
}

func jsonText(v any) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func encodeObject(o []kv) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(jsonText(m.key))
		b.WriteByte(':')
		b.Write(m.raw)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func set(o []kv, key string, raw json.RawMessage) []kv {
	if _, i := lookup(o, key); i >= 0 {
		o[i].raw = raw
		return o
	}
	return append(o, kv{key, raw})
}

func del(o []kv, key string) []kv {
	if _, i := lookup(o, key); i >= 0 {
		return append(o[:i:i], o[i+1:]...)
	}
	return o
}

// --- reading ------------------------------------------------------------------

// fieldFrom reads a field's schema.
func fieldFrom(key string, raw json.RawMessage) TypeField {
	f := TypeField{Key: key, Kind: KindOther, Schema: raw}
	obj, err := decodeObject(raw)
	if err != nil {
		return f
	}
	f.Title, _ = stringOf(obj, "title")
	f.Kind, f.Options = kindOf(obj)
	return f
}

// isString reports whether raw is a JSON string.
func isString(raw json.RawMessage) bool {
	var s string
	return json.Unmarshal(raw, &s) == nil
}

func stringOf(o []kv, key string) (string, bool) {
	raw, _ := lookup(o, key)
	var s string
	if raw == nil || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// stringList reads a JSON array of strings, and false for anything else.
func stringList(raw json.RawMessage) ([]string, bool) {
	var l []string
	if raw == nil || json.Unmarshal(raw, &l) != nil {
		return nil, false
	}
	return l, true
}

// uniqueOptions is opts without the values already seen (the first writing
// counts) and at most maxOptions of them. A type file written by hand is not
// checked as a write is, and a value listed twice would list its pages twice,
// and give a board two columns of one id.
func uniqueOptions(opts []string) []string {
	seen := make(map[string]bool, len(opts))
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		if seen[o] || len(out) >= maxOptions {
			continue
		}
		seen[o] = true
		out = append(out, o)
	}
	return out
}

// kindOf recognises a field's schema by the table of kinds.
func kindOf(o []kv) (string, []string) {
	typ, _ := stringOf(o, "type")
	switch typ {
	case "string":
		if raw, _ := lookup(o, "enum"); raw != nil {
			if opts, ok := stringList(raw); ok {
				return KindSelect, uniqueOptions(opts)
			}
			return KindOther, nil
		}
		format, _ := stringOf(o, "format")
		switch format {
		case "date":
			return KindDate, nil
		case "uri":
			return KindURL, nil
		}
		if raw, _ := lookup(o, "x-multiline"); string(raw) == "true" {
			return KindLongText, nil
		}
		return KindText, nil
	case "array":
		raw, _ := lookup(o, "items")
		items, err := decodeObject(raw)
		if err != nil {
			return KindOther, nil
		}
		if t, _ := stringOf(items, "type"); t != "string" {
			return KindOther, nil
		}
		if e, _ := lookup(items, "enum"); e != nil {
			if opts, ok := stringList(e); ok {
				return KindMultiSelect, uniqueOptions(opts)
			}
		}
		return KindOther, nil
	case "number", "integer":
		return KindNumber, nil
	case "boolean":
		return KindCheckbox, nil
	}
	return KindOther, nil
}

// parseType reads a type file. The error is errTypeFile for anything that is
// not a schema object whose properties, if any, are an object, within the
// limits.
func parseType(name string, data []byte) (TypeInfo, []kv, error) {
	top, err := decodeObject(data)
	if err != nil {
		return TypeInfo{}, nil, errTypeFile
	}
	t := TypeInfo{Name: name}
	t.Title, _ = stringOf(top, "title")
	if raw, _ := lookup(top, "properties"); raw != nil {
		props, err := decodeObject(raw)
		if err != nil || len(props) > MaxTypeFields {
			return TypeInfo{}, nil, errTypeFile
		}
		for _, p := range props {
			t.Fields = append(t.Fields, fieldFrom(p.key, p.raw))
		}
	}
	return t, top, nil
}

// typeSettings is the file of a type as the healer of the space knows it.
func typeSettings(name string) settingsFile {
	return settingsFile{rel: typesDir + "/" + name + ".json", max: MaxTypeFile, bad: errTypeFile,
		good: func(data []byte) bool { _, _, err := parseType(name, data); return err == nil }}
}

// readTypeFile reads the file of a type: nil data when there is none. A file
// that cannot be used (not a type, too large, not a regular file) is read as
// the newest version of it in the history that can, like a settings file (see
// restore.go); with none, there is no such type.
func (s *Space) readTypeFile(name string) ([]byte, error) {
	data, _, err := s.typeNow(name)
	return data, err
}

// typeNow is readTypeFile; from is set when the file cannot be used and data
// is what stands in for it.
func (s *Space) typeNow(name string) (data []byte, from *restored, err error) {
	data, from, _, err = s.typeNowSeen(name)
	return data, from, err
}

// typeNowSeen is typeNow, with what the read found in the place of the file.
func (s *Space) typeNowSeen(name string) (data []byte, from *restored, saw seen, err error) {
	f := typeSettings(name)
	data, saw, err = s.readTypeRaw(name)
	if err == nil && (data == nil || f.usable(data)) {
		s.heal.fine(f)
		return data, nil, saw, nil
	}
	if err != nil && !errors.Is(err, errTypeFile) {
		return nil, nil, saw, err
	}
	if s.heal == nil {
		return nil, nil, saw, errTypeFile
	}
	from, rerr := s.heal.restore(f)
	if rerr != nil {
		return nil, nil, saw, errTypeFile
	}
	s.heal.inUse(f, from)
	return from.text, from, saw, nil
}

// asideType is for a change that writes or removes the file of a type that
// cannot be used: the file as it is is kept beside it, in the same change.
func (s *Space) asideType(tx *txn, name string, from *restored, entries []string) (done func(), err error) {
	if (from == nil && len(entries) == 0) || s.heal == nil {
		return func() {}, nil
	}
	f := typeSettings(name)
	copy, err := s.heal.keep(f, func(rel string) error {
		if err := tx.record(jrec{Op: "t", Path: rel}); err != nil {
			return err
		}
		tx.touch(rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return func() { s.heal.repaired(f, &repair{from, entries}, copy) }, nil
}

func (s *Space) readTypeRaw(name string) ([]byte, seen, error) {
	data, fi, blocked, err := readIn(s.root, typesDir+"/"+name+".json", MaxTypeFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, seen{}, nil
	}
	if errors.Is(err, errChanged) {
		return nil, seen{}, errTypeFile
	}
	if err != nil {
		return nil, seen{}, err
	}
	saw := seen{fi, blocked}
	// Behind a link or a file in the place of a folder, it is read as restored.
	if blocked != "" || !fi.Mode().IsRegular() || data == nil || len(data) > MaxTypeFile {
		return nil, saw, errTypeFile
	}
	return data, saw, nil
}

// typeDirOK reports whether the folder of the types is a folder, not a link
// out of the space or a file. A missing folder is fine.
func (s *Space) typeDirOK() (bool, error) {
	d, blocked, err := openDirIn(s.root, typesDir)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if d != nil {
		d.Close()
	}
	return blocked == "", nil
}

// makeTypeDir is for a change that writes the type file rel: something that
// is not a folder where the folder of the types (or .petalbook) belongs is put
// aside, the folder is made again with the types it had (clearWay), and the
// copy is returned; "" when the way was clear.
func (s *Space) makeTypeDir(tx *txn, rel string) (string, error) {
	copy, err := s.clearWayIn(tx, rel, rel)
	if err != nil {
		return "", err
	}
	if b, err := wayBlocked(s.root, rel); err != nil || b != "" {
		return "", errors.Join(err, fmt.Errorf("%s is not a folder", b))
	}
	d, err := mkdirIn(s.root, typesDir)
	if err != nil {
		return copy, err
	}
	return copy, d.Close()
}

// typeNames lists the names of the type files, in name order. When something
// that is not a folder is in the place of the folder of the types (or of
// .petalbook), they are the types the folder had (see settingspath.go).
func (s *Space) typeNames() ([]string, error) {
	if ok, err := s.typeDirOK(); err != nil {
		return nil, err
	} else if !ok {
		blocked, err := wayBlocked(s.root, typesDir+"/x")
		if err != nil || blocked == "" {
			return nil, err
		}
		return s.heal.typeNamesFromHistory(blocked)
	}
	entries, err := readDirIn(s.root, typesDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".json") && !strings.HasPrefix(n, ".") {
			names = append(names, strings.TrimSuffix(n, ".json"))
		}
	}
	sort.Strings(names)
	return names, nil
}

// Types lists the types of the space, in name order, with the folder
// bindings. A type whose file cannot be used is listed with the reason. Only
// the first MaxTypes files are read; the others are listed as not read.
func (s *Space) Types() ([]TypeInfo, []Binding, error) {
	names, err := s.typeNames()
	if err != nil {
		return nil, nil, err
	}
	out := make([]TypeInfo, 0, len(names))
	for i, n := range names {
		if i >= MaxTypes {
			out = append(out, TypeInfo{Name: n, Error: fmt.Sprintf("a space has at most %d types; this file is not read", MaxTypes)})
			continue
		}
		data, err := s.readTypeFile(n)
		if err == nil && data == nil {
			continue // gone since the listing
		}
		if err != nil && !errors.Is(err, errTypeFile) {
			return nil, nil, err
		}
		out = append(out, *typeNow(n, data, err))
	}
	return out, s.Bindings(), nil
}

// --- writing ------------------------------------------------------------------

// recognised are the keys of a field's schema that say its kind.
var recognised = []string{"type", "enum", "items", "format", "x-multiline", "uniqueItems"}

func checkOptions(key string, opts []string) error {
	if len(opts) == 0 || len(opts) > maxOptions {
		return invalidf("Field %q needs 1 to %d options.", key, maxOptions)
	}
	seen := map[string]bool{}
	for _, o := range opts {
		if strings.TrimSpace(o) == "" {
			return invalidf("Field %q has an empty option.", key)
		}
		if err := validLabel("an option", o); err != nil {
			return err
		}
		if seen[o] {
			return invalidf("Field %q lists the option %q twice.", key, o)
		}
		seen[o] = true
	}
	return nil
}

// kindSchema is the keys that make a schema of kind k, in the order they are
// written.
func kindSchema(k string, opts []string) []kv {
	str := jsonText("string")
	switch k {
	case KindLongText:
		return []kv{{"type", str}, {"x-multiline", jsonText(true)}}
	case KindSelect:
		return []kv{{"type", str}, {"enum", jsonText(opts)}}
	case KindMultiSelect:
		items := encodeObject([]kv{{"type", str}, {"enum", jsonText(opts)}})
		return []kv{{"type", jsonText("array")}, {"items", items}, {"uniqueItems", jsonText(true)}}
	case KindDate:
		return []kv{{"type", str}, {"format", jsonText("date")}}
	case KindNumber:
		return []kv{{"type", jsonText("number")}}
	case KindCheckbox:
		return []kv{{"type", jsonText("boolean")}}
	case KindURL:
		return []kv{{"type", str}, {"format", jsonText("uri")}}
	}
	return []kv{{"type", str}}
}

// mergeField gives the schema of a field the title and kind of the request,
// touching only the keys that say them.
func mergeField(cur []kv, exists bool, in FieldInput) ([]kv, error) {
	curKind := KindOther
	if exists {
		curKind, _ = kindOf(cur)
	}
	kind := in.Kind
	if kind == "" {
		if !exists {
			return nil, invalidf("Field %q needs a kind.", in.Key)
		}
		kind = curKind
	}
	if !fieldKinds[kind] {
		return nil, invalidf("Field %q: unknown kind %q.", in.Key, kind)
	}
	if kind == KindOther && !exists {
		return nil, invalidf("Field %q: a field of a kind the server does not know cannot be made here.", in.Key)
	}
	if kind == KindOther && curKind != KindOther {
		return nil, invalidf("Field %q: a field cannot be changed to kind other.", in.Key)
	}
	if kind == KindSelect || kind == KindMultiSelect {
		if err := checkOptions(in.Key, in.Options); err != nil {
			return nil, err
		}
	} else if len(in.Options) > 0 {
		return nil, invalidf("Field %q: only a select or multiselect has options.", in.Key)
	}
	var out []kv
	switch {
	case !exists:
		out = kindSchema(kind, in.Options)
	case kind == curKind:
		out = append([]kv(nil), cur...)
		switch kind {
		case KindSelect:
			out = set(out, "enum", jsonText(in.Options))
		case KindMultiSelect:
			raw, _ := lookup(out, "items")
			items, _ := decodeObject(raw)
			out = set(out, "items", encodeObject(set(items, "enum", jsonText(in.Options))))
		}
	default:
		// The kind changes: its keys are replaced, everything else stays.
		out = append([]kv(nil), cur...)
		for _, k := range recognised {
			out = del(out, k)
		}
		out = append(kindSchema(kind, in.Options), out...)
	}
	if raw, _ := lookup(out, "title"); in.Title == "" && raw != nil && !isString(raw) {
		// The file's title is not text (written by hand), so what a listing
		// gave for it is "": a request that says "" is not asking to lose it.
	} else if in.Title == "" {
		out = del(out, "title")
	} else if exists {
		out = set(out, "title", jsonText(in.Title))
	} else {
		out = append([]kv{{"title", jsonText(in.Title)}}, out...)
	}
	return out, nil
}

// PutType makes the type name, or changes it: the file is read, the title and
// the fields of the request are merged into it, and it is written back. What
// the file has that the server does not understand stays. A file that cannot be
// used is read as its last version that can, and is kept beside the one
// written here (see restore.go).
//
// baseRev is the Rev of the type the request was made from, "" to make one. A
// type that is not as that says (changed, made or removed since) is a
// *TypeConflictError and nothing is written.
//
// Only a field that is new has its key checked: one the file already has keeps
// its key, whatever it is (a person may have written one the rules would not
// allow, and renaming it is not what a change of its title means). A title in
// the file that is not text is kept when the request gives none (see mergeField).
func (s *Space) PutType(by Author, name, baseRev string, in TypeInput) (TypeInfo, error) {
	if err := ValidTypeName(name); err != nil {
		return TypeInfo{}, err
	}
	if err := validLabel("the title", in.Title); err != nil {
		return TypeInfo{}, err
	}
	if len(in.Fields) > MaxTypeFields {
		return TypeInfo{}, invalidf("A type has at most %d fields.", MaxTypeFields)
	}
	seen := map[string]bool{}
	for _, f := range in.Fields {
		if f.Key == "" || !utf8.ValidString(f.Key) {
			return TypeInfo{}, invalidf("A field key is 1 to %d bytes of text.", maxFieldKey)
		}
		if seen[f.Key] {
			return TypeInfo{}, invalidf("The field %q is listed twice.", f.Key)
		}
		seen[f.Key] = true
		if err := validLabel("a field title", f.Title); err != nil {
			return TypeInfo{}, err
		}
	}
	return mutate(s, by, func(tx *txn) (TypeInfo, error) {
		names, err := s.typeNames()
		if err != nil {
			return TypeInfo{}, err
		}
		have := false
		for _, n := range names {
			switch {
			case n == name:
				have = true
			case page.NameKey(n) == page.NameKey(name):
				// On a file system that ignores case these are one file.
				return TypeInfo{}, ErrExists
			}
		}
		if !have && len(names) >= MaxTypes {
			return TypeInfo{}, invalidf("A space has at most %d types.", MaxTypes)
		}
		data, from, err := s.typeNow(name)
		var replacedFields []string
		var top, props []kv
		switch {
		case errors.Is(err, errTypeFile):
			return TypeInfo{}, typeFileProblem(name)
		case err != nil:
			return TypeInfo{}, err
		case data != nil:
			var perr error
			if _, top, perr = parseType(name, data); perr != nil {
				return TypeInfo{}, typeFileProblem(name)
			}
			raw, _ := lookup(top, "properties")
			if raw != nil {
				props, _ = decodeObject(raw)
			}
		}
		if (data == nil && baseRev != "") || (data != nil && baseRev != page.Rev(data)) {
			return TypeInfo{}, &TypeConflictError{typeNow(name, data, nil)}
		}
		var next []kv
		for _, f := range in.Fields {
			raw, i := lookup(props, f.Key)
			if i < 0 {
				if err := validFieldKey(f.Key); err != nil {
					return TypeInfo{}, err
				}
			}
			var cur []kv
			if i >= 0 {
				// A schema that is not an object (true, say) is kept as it is
				// and has no keys to merge into.
				if cur, err = decodeObject(raw); err != nil {
					if f.Kind == "" || f.Kind == KindOther {
						next = append(next, kv{f.Key, raw})
						continue
					}
					// The request gives the field a kind: the schema is written
					// anew, and the file as it was is kept.
					cur, i = nil, -1
					replacedFields = append(replacedFields, "properties: "+f.Key)
				}
			}
			merged, err := mergeField(cur, i >= 0, f)
			if err != nil {
				return TypeInfo{}, err
			}
			next = append(next, kv{f.Key, encodeObject(merged)})
		}
		if top == nil {
			top = []kv{}
		}
		if raw, _ := lookup(top, "title"); in.Title == "" && raw != nil && !isString(raw) {
			// Not text: listed as "", and not lost by a request that gives none.
		} else if in.Title == "" {
			top = del(top, "title")
		} else if _, i := lookup(top, "title"); i >= 0 {
			top = set(top, "title", jsonText(in.Title))
		} else {
			top = append([]kv{{"title", jsonText(in.Title)}}, top...)
		}
		top = set(top, "properties", encodeObject(next))
		var out bytes.Buffer
		if err := json.Indent(&out, encodeObject(top), "", "  "); err != nil {
			return TypeInfo{}, err
		}
		out.WriteByte('\n')
		if out.Len() > MaxTypeFile {
			return TypeInfo{}, invalidf("The type would be larger than %d KiB.", MaxTypeFile>>10)
		}
		if bytes.Equal(out.Bytes(), data) && from == nil {
			t, _, _ := parseType(name, data)
			t.Rev = page.Rev(data)
			return t, nil
		}
		rel := typesDir + "/" + name + ".json"
		// A type is a file a write makes: files.maxBytes judges it as a page.
		if err := by.checkFileSize(rel, int64(out.Len()), int64(len(data))); err != nil {
			return TypeInfo{}, err
		}
		moved, err := s.makeTypeDir(tx, rel)
		if err != nil {
			return TypeInfo{}, err
		}
		// A file that could not be used, or whose field could not, is kept as
		// it was beside the one written now; behind a folder that was not one,
		// what was in the place of the folder is that copy.
		done := func() {
			if s.heal != nil && (from != nil || len(replacedFields) > 0) {
				s.heal.repaired(typeSettings(name), &repair{from, replacedFields}, moved)
			}
		}
		if moved == "" {
			if done, err = s.asideType(tx, name, from, replacedFields); err != nil {
				return TypeInfo{}, err
			}
		}
		if err := tx.record(jrec{Op: "t", Path: rel}); err != nil {
			return TypeInfo{}, err
		}
		tx.touch(rel)
		s.typesDirty.Store(true)
		if err := writeIn(s.root, rel, out.Bytes()); err != nil {
			return TypeInfo{}, err
		}
		s.heal.wroteGood(typeSettings(name), out.Bytes())
		done()
		t, _, err := parseType(name, out.Bytes())
		t.Rev = page.Rev(out.Bytes())
		return t, err
	})
}

// DeleteType removes a type: its file, and in the same change the bindings of
// folders to it. Pages that name it in their header are not touched. ErrNotFound
// when there is no such type and no folder is bound to it. The file of a type
// that cannot be read can be deleted too: that is one way to fix it (its rev is
// UnreadableRev). baseRev is the Rev of the type the request was made from, as
// for PutType; a type that is not as that says is a *TypeConflictError and
// nothing is removed. Bindings to a type whose file is not there (put there by
// hand) are removed with baseRev "".
func (s *Space) DeleteType(by Author, name, baseRev string) error {
	if err := ValidTypeName(name); err != nil {
		return err
	}
	_, err := mutate(s, by, func(tx *txn) (struct{}, error) {
		rel := typesDir + "/" + name + ".json"
		// Behind something that is not a folder, the folder is made again with
		// the types it had first (the one deleted among them, as it is read).
		if b, err := wayBlocked(s.root, rel); err != nil {
			return struct{}{}, err
		} else if b != "" {
			if _, err := s.clearWayIn(tx, rel, ""); err != nil {
				return struct{}{}, err
			}
		}
		dirOK, err := s.typeDirOK()
		if err != nil {
			return struct{}{}, err
		}
		// A link in its place is removed as a link, never followed.
		_, _, err = lstatIn(s.root, rel)
		have := err == nil && dirOK
		if err != nil && !errors.Is(err, fs.ErrNotExist) && dirOK {
			return struct{}{}, err
		}
		bound := false
		for _, b := range s.Bindings() {
			if b.Type == name {
				bound = true
			}
		}
		if !have && !bound {
			return struct{}{}, ErrNotFound
		}
		if !have && baseRev != "" {
			return struct{}{}, &TypeConflictError{}
		}
		var from *restored
		if have {
			var data []byte
			if data, from, err = s.typeNow(name); err != nil && !errors.Is(err, errTypeFile) {
				return struct{}{}, err
			}
			cur := typeNow(name, data, err)
			// A file that cannot be used and has no version that can is no
			// type: it goes like one that is not there, from the rev "".
			if (cur == nil && baseRev != "") || (cur != nil && cur.Rev != baseRev) {
				return struct{}{}, &TypeConflictError{cur}
			}
			if cur == nil && !bound && from == nil {
				return struct{}{}, ErrNotFound
			}
		}
		if bound {
			if err := s.editSpaceFile(tx, func(root *yaml.Node) (bool, error) {
				return unbindWhere(root, func(_, t string) bool { return t == name })
			}); err != nil {
				return struct{}{}, err
			}
		}
		if have {
			// What the file was, when it could not be used, is kept.
			done, err := s.asideType(tx, name, from, nil)
			if err != nil {
				return struct{}{}, err
			}
			if err := tx.record(jrec{Op: "d", Path: rel}); err != nil {
				return struct{}{}, err
			}
			tx.touch(rel)
			s.typesDirty.Store(true)
			if err := removeIn(s.root, rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return struct{}{}, err
			}
			done()
		}
		return struct{}{}, nil
	})
	return err
}

// BindFolder binds a folder (and, by inheritance, the folders below it) to a
// type, or unbinds it when typ is "". The type must exist. It returns the
// folder's path as it is on disk.
func (s *Space) BindFolder(by Author, folder, typ string) (string, error) {
	if err := checkRel(folder); err != nil {
		return "", err
	}
	if typ != "" {
		if err := ValidTypeName(typ); err != nil {
			return "", err
		}
	}
	return mutate(s, by, func(tx *txn) (string, error) {
		canon, existed, err := s.resolveFolder(folder)
		if errors.Is(err, ErrExists) || (err == nil && !existed) {
			return "", ErrNoFolder
		}
		if err != nil {
			return "", err
		}
		if typ != "" {
			if _, err := s.typeDirOK(); err != nil {
				return "", err
			}
			switch data, err := s.readTypeFile(typ); {
			case errors.Is(err, errTypeFile):
				// A file that exists but is not usable is still a type to
				// bind to: fixing it is the person's.
			case err != nil:
				return "", err
			case data == nil:
				return "", invalidf("There is no type named %q.", typ)
			}
		}
		err = s.editSpaceFile(tx, func(root *yaml.Node) (bool, error) { return setBinding(root, canon, typ) })
		return canon, err
	})
}
