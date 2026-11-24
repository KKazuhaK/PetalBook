package space

import (
	"errors"
	"io/fs"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

// A template is an ordinary page file under .petalbook/templates/<name>.md: a
// header and a body, type fields allowed, with no id of its own. It is not
// listed as a page and not indexed (the folder is a dot folder), and it is
// read and written only through the folders-never-links functions of
// inroot.go. The folders' default templates and the quick-note default are
// kept in space.yml:
//
//	templates:
//	  folders:
//	    随笔: 日记
//	  quickNote: 随记
const (
	templatesDir = ".petalbook/templates"
	// MaxTemplates is the most templates a space has, MaxTemplateFile the most
	// of one template file.
	MaxTemplates    = 200
	MaxTemplateFile = 256 << 10
	maxTemplateName = 80
)

// Template is a template as the API returns it.
type Template struct {
	Name string
	Path string
	Rev  string
	Meta page.Meta
	Body string
}

// TemplateDefaults say which template a new page gets when none is asked for.
type TemplateDefaults struct {
	// Folders maps a folder to the name of its default template.
	Folders map[string]string
	// QuickNote is the template of quick notes, "" for none.
	QuickNote string
}

func (d TemplateDefaults) clone() TemplateDefaults {
	out := TemplateDefaults{Folders: make(map[string]string, len(d.Folders)), QuickNote: d.QuickNote}
	for k, v := range d.Folders {
		out.Folders[k] = v
	}
	return out
}

// TemplateConflictError means the template is not as the request thought.
// Current is how it is now, nil when there is none.
type TemplateConflictError struct{ Current *Template }

func (e *TemplateConflictError) Error() string { return "the template was changed by someone else" }

// TemplateError is a refusal with a code of its own: template_limit,
// template_too_large, template_not_found.
type TemplateError struct{ Code, Msg string }

func (e *TemplateError) Error() string { return e.Msg }

func templateNotFound(name string) error {
	return &TemplateError{"template_not_found", "there is no template named " + quote(name)}
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `'`) + `"` }

// ValidTemplateName checks a template name by the rules of page names: what
// would be a page's file name as it is. "defaults" is the address of the
// defaults, so it is not a name.
func ValidTemplateName(name string) error {
	if name == "" || name != page.FileStem(name) || name == "defaults" || len(name) > maxTemplateName*4 {
		return invalidf("a template name is a file name of at most %d characters, without %s, and not \"defaults\"", maxTemplateName, `<>:"/\|?*`)
	}
	return nil
}

func templateRel(name string) string { return templatesDir + "/" + name + ".md" }

// templateDirOK reports whether the folder of the templates, and .petalbook,
// are folders: a link or a file in their place leads nowhere. A missing folder
// is fine.
func (s *Space) templateDirOK() (bool, error) {
	d, blocked, err := openDirIn(s.root, templatesDir)
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

// templateNames lists the names of the template files, in name order: regular
// files only, never what a link leads to.
func (s *Space) templateNames() ([]string, error) {
	ok, err := s.templateDirOK()
	if err != nil || !ok {
		return nil, err
	}
	entries, err := readDirIn(s.root, templatesDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(n, ".md") || strings.HasPrefix(n, ".") {
			continue
		}
		if stem := strings.TrimSuffix(n, ".md"); ValidTemplateName(stem) == nil {
			names = append(names, stem)
		}
	}
	sort.Strings(names)
	return names, nil
}

// readTemplateFile reads the file of a template: nil when there is none, or
// what is there is not a regular file (a link, say, is never followed) or is
// larger than a template may be.
func (s *Space) readTemplateFile(name string) ([]byte, error) {
	data, fi, blocked, err := readIn(s.root, templateRel(name), MaxTemplateFile)
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, errChanged):
		return nil, nil
	case err != nil:
		return nil, err
	case blocked != "" || fi == nil || !fi.Mode().IsRegular() || data == nil || len(data) > MaxTemplateFile:
		return nil, nil
	}
	return data, nil
}

func templateFrom(name string, data []byte) *Template {
	doc := page.Parse(data)
	m := doc.Meta()
	if m == nil {
		m = page.Meta{}
	}
	return &Template{Name: name, Path: templateRel(name), Rev: page.Rev(data), Meta: m, Body: doc.Body}
}

// Templates lists the templates of the space in name order, without bodies,
// and the defaults. Only the first MaxTemplates files are read.
func (s *Space) Templates() ([]Template, TemplateDefaults, error) {
	names, err := s.templateNames()
	if err != nil {
		return nil, TemplateDefaults{}, err
	}
	if len(names) > MaxTemplates {
		names = names[:MaxTemplates]
	}
	out := make([]Template, 0, len(names))
	for _, n := range names {
		data, err := s.readTemplateFile(n)
		if err != nil {
			return nil, TemplateDefaults{}, err
		}
		if data == nil {
			continue
		}
		t := templateFrom(n, data)
		t.Body = ""
		out = append(out, *t)
	}
	return out, s.TemplateDefaults(), nil
}

// Template returns one template with its body; ErrNotFound when there is none.
func (s *Space) Template(name string) (*Template, error) {
	if ValidTemplateName(name) != nil {
		return nil, ErrNotFound
	}
	data, err := s.readTemplateFile(name)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, ErrNotFound
	}
	return templateFrom(name, data), nil
}

// TemplateDefaults returns the defaults of the space.
func (s *Space) TemplateDefaults() TemplateDefaults {
	d := s.config().templates
	if d == nil {
		return TemplateDefaults{Folders: map[string]string{}}
	}
	return d.clone()
}

// PutTemplate makes a template (baseRev "") or replaces one (baseRev its rev),
// as one change. A template that is not as baseRev says is a
// *TemplateConflictError.
func (s *Space) PutTemplate(by Author, name, baseRev string, meta page.Meta, body string) (*Template, error) {
	if err := ValidTemplateName(name); err != nil {
		return nil, err
	}
	if err := checkBody(body); err != nil {
		return nil, err
	}
	doc := page.Parse(nil)
	if err := doc.SetMeta(meta.Clone()); err != nil {
		return nil, invalidf("%v", err)
	}
	doc.SetBody(body)
	data := doc.Bytes()
	if len(data) > MaxTemplateFile {
		return nil, &TemplateError{"template_too_large", "a template is at most 256 KiB"}
	}
	rel := templateRel(name)
	return mutate(s, by, func(tx *txn) (*Template, error) {
		names, err := s.templateNames()
		if err != nil {
			return nil, err
		}
		have := false
		for _, n := range names {
			switch {
			case n == name:
				have = true
			case page.NameKey(n) == page.NameKey(name):
				// On a file system that ignores case these are one file.
				return nil, ErrExists
			}
		}
		cur, err := s.readTemplateFile(name)
		if err != nil {
			return nil, err
		}
		var curT *Template
		if cur != nil {
			curT = templateFrom(name, cur)
		} else if have {
			// A file that is there and cannot be used as a template (too large):
			// it is not overwritten.
			return nil, unavailable(nil, "%s is there and cannot be used as a template (it is larger than %d KiB); remove it first", rel, MaxTemplateFile>>10)
		}
		if (curT == nil && baseRev != "") || (curT != nil && curT.Rev != baseRev) {
			return nil, &TemplateConflictError{curT}
		}
		if curT == nil && len(names) >= MaxTemplates {
			return nil, &TemplateError{"template_limit", "a space has at most 200 templates"}
		}
		if curT != nil && string(cur) == string(data) {
			return curT, nil
		}
		// A template is a file a write makes: files.maxBytes judges it as a page.
		if err := by.checkFileSize(rel, int64(len(data)), int64(len(cur))); err != nil {
			return nil, err
		}
		// Something that is not a folder where the folder of the templates
		// belongs is put aside and the folder made again, as for the types.
		if _, err := s.clearWayIn(tx, rel, rel); err != nil {
			return nil, err
		}
		if b, err := wayBlocked(s.root, rel); err != nil {
			return nil, err
		} else if b != "" {
			return nil, unavailable(nil, "%s is not a folder, so the template has nowhere to be kept", b)
		}
		if d, err := mkdirIn(s.root, templatesDir); err != nil {
			return nil, err
		} else {
			d.Close()
		}
		if err := tx.record(jrec{Op: "t", Path: rel}); err != nil {
			return nil, err
		}
		tx.touch(rel)
		if err := writeIn(s.root, rel, data); err != nil {
			return nil, err
		}
		return templateFrom(name, data), nil
	})
}

// DeleteTemplate removes a template and, in the same change, the defaults that
// name it. baseRev is the rev the request was made from. ErrNotFound when
// there is no such template and no default names it; defaults that name a
// template that is not there are removed with baseRev "".
func (s *Space) DeleteTemplate(by Author, name, baseRev string) error {
	if err := ValidTemplateName(name); err != nil {
		return ErrNotFound
	}
	rel := templateRel(name)
	_, err := mutate(s, by, func(tx *txn) (struct{}, error) {
		cur, err := s.readTemplateFile(name)
		if err != nil {
			return struct{}{}, err
		}
		named := s.namedByDefaults(name)
		switch {
		case cur == nil && !named:
			return struct{}{}, ErrNotFound
		case cur == nil && baseRev != "":
			return struct{}{}, &TemplateConflictError{}
		case cur != nil && page.Rev(cur) != baseRev:
			return struct{}{}, &TemplateConflictError{templateFrom(name, cur)}
		}
		if named {
			if err := s.editSpaceFile(tx, func(root *yaml.Node) (bool, error) { return forgetTemplateNamed(root, name) }); err != nil {
				return struct{}{}, err
			}
		}
		if cur != nil {
			if err := tx.record(jrec{Op: "d", Path: rel}); err != nil {
				return struct{}{}, err
			}
			tx.touch(rel)
			if err := removeIn(s.root, rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	return err
}

func (s *Space) namedByDefaults(name string) bool {
	d := s.config().templates
	if d == nil {
		return false
	}
	if d.QuickNote == name {
		return true
	}
	for _, t := range d.Folders {
		if t == name {
			return true
		}
	}
	return false
}

// SetFolderTemplate sets the default template of a folder, or removes it with
// template "". The template must exist; the folder must exist unless the
// default is being removed. It returns the defaults as they are now.
func (s *Space) SetFolderTemplate(by Author, folder, template string) (TemplateDefaults, error) {
	return s.setDefault(by, template, func(tx *txn) (func(*yaml.Node) (bool, error), error) {
		if err := checkRel(folder); err != nil {
			return nil, err
		}
		key := bindingKey(folder)
		if canon, existed, err := s.resolveFolder(key); err == nil && existed {
			key = canon
		} else if template != "" {
			return nil, ErrNoFolder
		}
		if key == "" {
			return nil, ErrNoFolder
		}
		return func(root *yaml.Node) (bool, error) { return setFolderTemplate(root, key, template) }, nil
	})
}

// SetQuickNoteTemplate sets the template of quick notes, "" for none.
func (s *Space) SetQuickNoteTemplate(by Author, template string) (TemplateDefaults, error) {
	return s.setDefault(by, template, func(*txn) (func(*yaml.Node) (bool, error), error) {
		return func(root *yaml.Node) (bool, error) { return setQuickNoteTemplate(root, template) }, nil
	})
}

func (s *Space) setDefault(by Author, template string, plan func(tx *txn) (func(*yaml.Node) (bool, error), error)) (TemplateDefaults, error) {
	if template != "" {
		if err := ValidTemplateName(template); err != nil {
			return TemplateDefaults{}, templateNotFound(template)
		}
	}
	return mutate(s, by, func(tx *txn) (TemplateDefaults, error) {
		if template != "" {
			data, err := s.readTemplateFile(template)
			if err != nil {
				return TemplateDefaults{}, err
			}
			if data == nil {
				return TemplateDefaults{}, templateNotFound(template)
			}
		}
		edit, err := plan(tx)
		if err != nil {
			return TemplateDefaults{}, err
		}
		if err := s.editSpaceFile(tx, edit); err != nil {
			return TemplateDefaults{}, err
		}
		return s.TemplateDefaults(), nil
	})
}

// --- creating a page from a template ----------------------------------------

// ApplyTemplate fills the input of a create from a template. name is the
// template the request names (nil: not named, "": none wanted); without one,
// a quick note takes the quick-note default and any page the default of its
// folder. A template that is asked for and is not there is a refusal; a
// default that names one that is gone is skipped. tz is minutes east of UTC.
// The request's header fields and body win over the template's, field by
// field, except that the template's type is kept.
func (s *Space) ApplyTemplate(in *CreateInput, name *string, quick bool, tz int) error {
	pick := ""
	switch {
	case name != nil:
		pick = *name
	default:
		d := s.config().templates
		if d == nil {
			return nil
		}
		if quick {
			pick = d.QuickNote
		}
		if pick == "" && len(d.Folders) > 0 {
			key := bindingKey(in.Folder)
			if canon, ok, _ := s.CanonicalFolder(key); ok {
				key = canon
			}
			pick = d.Folders[key]
		}
	}
	if pick == "" {
		return nil
	}
	t, err := s.Template(pick)
	if errors.Is(err, ErrNotFound) {
		if name != nil {
			return templateNotFound(pick)
		}
		return nil
	}
	if err != nil {
		return err
	}
	in.Meta, in.Body = mergeTemplate(*t, in.Meta, in.Body, s.now().UTC().Add(time.Duration(tz)*time.Minute))
	return nil
}

// mergeTemplate is the header and body of a new page made from t, with the
// variables filled in for local, the time where the client is.
func mergeTemplate(t Template, req page.Meta, body string, local time.Time) (page.Meta, string) {
	fill := variables(local)
	out := make(page.Meta, 0, len(t.Meta)+len(req))
	for _, f := range t.Meta {
		if f.Key == "id" || f.Key == "created" {
			continue
		}
		v := fillValue(f.Value, fill)
		if f.Key != "type" || cleared(page.Field{Key: "type", Value: v}) {
			if r := req.Index(f.Key); r >= 0 && !cleared(req[r]) {
				v = req[r].Value
			}
		}
		out = append(out, page.Field{Key: f.Key, Value: v})
	}
	for _, f := range req {
		if t.Meta.Index(f.Key) < 0 || f.Key == "id" || f.Key == "created" {
			out = append(out, f)
		}
	}
	if body == "" {
		body = fill(t.Body)
	}
	return out, body
}

// variables returns the function that fills in {{date}}, {{time}} and
// {{datetime}}; any other {{ stays as written.
func variables(local time.Time) func(string) string {
	r := strings.NewReplacer(
		"{{date}}", local.Format("2006-01-02"),
		"{{time}}", local.Format("15:04"),
		"{{datetime}}", local.Format("2006-01-02 15:04"))
	return func(s string) string {
		if !strings.Contains(s, "{{") {
			return s
		}
		return r.Replace(s)
	}
}

func fillValue(v any, fill func(string) string) any {
	switch x := v.(type) {
	case string:
		return fill(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = fillValue(e, fill)
		}
		return out
	case page.Meta:
		out := make(page.Meta, len(x))
		for i, f := range x {
			out[i] = page.Field{Key: f.Key, Value: fillValue(f.Value, fill)}
		}
		return out
	}
	return v
}

// --- the defaults in space.yml ------------------------------------------------

// templatesOf reads the defaults of a settings document: nil when it names none.
func templatesOf(doc *yaml.Node) *TemplateDefaults {
	if doc == nil || len(doc.Content) == 0 {
		return nil
	}
	v, _ := mapValue(doc.Content[0], "templates")
	if v == nil || v.Kind != yaml.MappingNode {
		return nil
	}
	out := TemplateDefaults{Folders: map[string]string{}}
	var r reading
	if q, _ := r.value(v, "quickNote"); q != nil {
		if t, ok := scalarText(q); ok {
			out.QuickNote = strings.TrimSpace(t)
		}
	}
	if f, _ := r.value(v, "folders"); f != nil && f.Kind == yaml.MappingNode {
		eachPair(f, func(k string, val *yaml.Node, _ int) {
			if t, ok := scalarText(val); ok && strings.TrimSpace(t) != "" {
				out.Folders[bindingKey(k)] = strings.TrimSpace(t)
			} else {
				delete(out.Folders, bindingKey(k)) // the last writing counts
			}
		})
	}
	if out.QuickNote == "" && len(out.Folders) == 0 {
		return nil
	}
	return &out
}

// templatesNode returns the `templates` mapping of the settings, the writer's
// own to change, made when create is set; nil when it is not there and not made.
func templatesNode(top *yaml.Node, create bool) *yaml.Node {
	cur, at := mapValue(top, "templates")
	if cur != nil && at < 0 {
		// Only a merge key brings it in: written out first, so there is a key to change.
		flattenMerges(top)
		cur, at = mapValue(top, "templates")
	}
	fresh := func() *yaml.Node { return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"} }
	switch {
	case cur != nil && cur.Kind == yaml.MappingNode:
		return own(top, at+1)
	case cur == nil && !create:
		return nil
	case cur == nil:
		n := fresh()
		top.Content = append(top.Content, textNode("templates"), n)
		return n
	case !create:
		return nil
	}
	if !(cur.Kind == yaml.ScalarNode && (cur.Tag == "!!null" || cur.Value == "")) {
		noteReplaced(top, "templates")
	}
	n := fresh()
	top.Content[at+1] = n
	return n
}

// templateFolders is the `folders` mapping of the templates mapping, the
// writer's own, made when create is set.
func templateFolders(tpl *yaml.Node, create bool) *yaml.Node {
	cur, at := mapValue(tpl, "folders")
	if cur != nil && at < 0 {
		flattenMerges(tpl)
		cur, at = mapValue(tpl, "folders")
	}
	switch {
	case cur != nil && cur.Kind == yaml.MappingNode:
		return own(tpl, at+1)
	case !create:
		return nil
	}
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if cur == nil {
		tpl.Content = append(tpl.Content, textNode("folders"), n)
		return n
	}
	if !(cur.Kind == yaml.ScalarNode && (cur.Tag == "!!null" || cur.Value == "")) {
		noteReplaced(tpl, "templates: folders")
	}
	tpl.Content[at+1] = n
	return n
}

// tidyTemplates removes what is left empty: the folders mapping, then the
// templates mapping.
func tidyTemplates(top *yaml.Node) {
	cur, at := mapValue(top, "templates")
	if cur == nil || at < 0 || cur.Kind != yaml.MappingNode {
		return
	}
	if f, fat := mapValue(cur, "folders"); f != nil && fat >= 0 && f.Kind == yaml.MappingNode && len(f.Content) == 0 {
		removeKey(own(top, at+1), "folders")
	}
	if cur, at = mapValue(top, "templates"); cur != nil && at >= 0 && cur.Kind == yaml.MappingNode && len(cur.Content) == 0 {
		removeKey(top, "templates")
	}
}

// setFolderTemplate sets, or with name "" removes, the default template of a folder.
func setFolderTemplate(top *yaml.Node, folder, name string) (bool, error) {
	if name == "" {
		if d := templatesOf(docOf(top)); d == nil || d.Folders[folder] == "" {
			return false, nil
		}
		return dropFolderTemplates(top, func(f, _ string) bool { return f == folder })
	}
	if d := templatesOf(docOf(top)); d != nil && d.Folders[folder] == name {
		return false, nil
	}
	f := templateFolders(templatesNode(top, true), true)
	// The entry that counts is the last; an earlier one must not come back.
	kept := make([]*yaml.Node, 0, len(f.Content)+2)
	var last *yaml.Node
	for i := 0; i+1 < len(f.Content); i += 2 {
		if k, ok := keyText(f.Content[i]); ok && bindingKey(k) == folder {
			if last != nil {
				kept = removeEntry(kept, last)
			}
			last = f.Content[i]
		}
		kept = append(kept, f.Content[i], f.Content[i+1])
	}
	f.Content = kept
	if last != nil {
		for i := 0; i+1 < len(f.Content); i += 2 {
			if f.Content[i] == last {
				nv := textNode(name)
				old := f.Content[i+1]
				nv.HeadComment, nv.LineComment, nv.FootComment = old.HeadComment, old.LineComment, old.FootComment
				f.Content[i+1] = nv
			}
		}
		return true, nil
	}
	f.Content = append(f.Content, textNode(folder), textNode(name))
	return true, nil
}

func removeEntry(list []*yaml.Node, key *yaml.Node) []*yaml.Node {
	for i := 0; i+1 < len(list); i += 2 {
		if list[i] == key {
			return append(list[:i:i], list[i+2:]...)
		}
	}
	return list
}

// docOf wraps a top mapping so it can be read with templatesOf.
func docOf(top *yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{top}}
}

// setQuickNoteTemplate sets, or with name "" removes, the quick-note template.
func setQuickNoteTemplate(top *yaml.Node, name string) (bool, error) {
	cur := ""
	if d := templatesOf(docOf(top)); d != nil {
		cur = d.QuickNote
	}
	if cur == name {
		return false, nil
	}
	if name == "" {
		tpl := templatesNode(top, false)
		if tpl == nil {
			return false, nil
		}
		removeKey(tpl, "quickNote")
		tidyTemplates(top)
		return true, nil
	}
	tpl := templatesNode(top, true)
	if q, _ := mapValue(tpl, "quickNote"); q != nil && q.Kind != yaml.ScalarNode {
		noteReplaced(top, "templates: quickNote")
	}
	setKey(tpl, "quickNote", textNode(name))
	return true, nil
}

// dropFolderTemplates removes the default of every folder for which drop says
// so (given the folder and its template); the entry that counts for a folder is
// its last, and all of the folder's entries go with it.
func dropFolderTemplates(top *yaml.Node, drop func(folder, tmpl string) bool) (bool, error) {
	tpl := templatesNode(top, false)
	if tpl == nil {
		return false, nil
	}
	f := templateFolders(tpl, false)
	if f == nil {
		return false, nil
	}
	last := map[string]int{}
	for i := 0; i+1 < len(f.Content); i += 2 {
		if k, ok := keyText(f.Content[i]); ok {
			last[bindingKey(k)] = i
		}
	}
	gone := map[string]bool{}
	for k, i := range last {
		t, _ := scalarText(deref(f.Content[i+1]))
		if drop(k, strings.TrimSpace(t)) {
			gone[k] = true
		}
	}
	if len(gone) == 0 {
		return false, nil
	}
	kept := make([]*yaml.Node, 0, len(f.Content))
	for i := 0; i+1 < len(f.Content); i += 2 {
		if k, ok := keyText(f.Content[i]); ok && gone[bindingKey(k)] {
			continue
		}
		kept = append(kept, f.Content[i], f.Content[i+1])
	}
	f.Content = kept
	tidyTemplates(top)
	return true, nil
}

// forgetTemplateNamed removes every default that names the template.
func forgetTemplateNamed(top *yaml.Node, name string) (bool, error) {
	changed, err := dropFolderTemplates(top, func(_, t string) bool { return t == name })
	if err != nil {
		return false, err
	}
	if d := templatesOf(docOf(top)); d != nil && d.QuickNote == name {
		c, err := setQuickNoteTemplate(top, "")
		return changed || c, err
	}
	return changed, nil
}

// forgetTemplateFolders removes the defaults of the folders that are gone.
func forgetTemplateFolders(top *yaml.Node, gone func(folder string) bool) (bool, error) {
	return dropFolderTemplates(top, func(folder, _ string) bool { return gone(folder) })
}

// rebindTemplates makes the folder defaults follow a folder moved from `from`
// to `to` (and the folders in it). Entries written for `to` or under it were
// stale (nothing was there), and go. With onlyIfBound they go only when there
// is a default to move: that is for the recovery of a move, where a repeat must
// not drop what a first run moved.
func rebindTemplates(top *yaml.Node, from, to string, onlyIfBound bool) (bool, error) {
	tpl := templatesNode(top, false)
	if tpl == nil {
		return false, nil
	}
	f := templateFolders(tpl, false)
	if f == nil {
		return false, nil
	}
	n := len(f.Content) / 2
	newKey := make([]string, n)
	arriving := map[string]int{}
	for e := 0; e < n; e++ {
		key, ok := folderAt(f, 2*e)
		if !ok {
			continue
		}
		switch {
		case key == from:
			newKey[e] = to
		case strings.HasPrefix(key, from+"/"):
			newKey[e] = to + key[len(from):]
		default:
			continue
		}
		arriving[newKey[e]] = e
	}
	if onlyIfBound && len(arriving) == 0 {
		return false, nil
	}
	changed := len(arriving) > 0
	out := make([]*yaml.Node, 0, len(f.Content))
	for e := 0; e < n; e++ {
		k, v := f.Content[2*e], f.Content[2*e+1]
		key, isText := folderAt(f, 2*e)
		switch {
		case newKey[e] != "":
			if arriving[newKey[e]] != e {
				continue
			}
			nk := textNode(newKey[e])
			nk.HeadComment, nk.LineComment, nk.FootComment = k.HeadComment, k.LineComment, k.FootComment
			k = nk
		case isText:
			if key == to || strings.HasPrefix(key, to+"/") {
				changed = true
				continue
			}
		}
		out = append(out, k, v)
	}
	f.Content = out
	tidyTemplates(top)
	return changed, nil
}
