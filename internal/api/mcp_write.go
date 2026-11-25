package api

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"petalbook.app/server/internal/page"
	"petalbook.app/server/internal/space"
)

func (t *mcpTools) addWriteTools() {
	addTool(t, "page_create", "Make a page. Everything but space is optional; the server picks the id, the creation date and the file name, and applies the folder's template as the app does (template: \"\" for none). props are header fields that are not built in. A requestId makes a repeated call (a retry) return the first result instead of making a second page. Needs write.", true, map[string]string{"props": "object"}, t.pageCreate)
	addTool(t, "page_update", "Change a page. baseRev is the rev from page_get or a page item (required): if the page changed since, the answer is conflict, with the current rev, and nothing is written. Say what changes and the rest stays byte for byte: title, subtitle, source, tags, type (\"\" or [] removes), set and unset for other header fields, and the body, either whole (body) or as edits, a list of {old, new} replacements applied in order, each old text occurring exactly once (edit_no_match or edit_ambiguous otherwise; nothing is written). Use edits for a change to a long text. A change of title renames the file and the links to it follow. Needs write.", true, map[string]string{"set": "object"}, t.pageUpdate)
	addTool(t, "page_move", "Move a page to a folder (made if needed; \"\" is the top level). The links to the page follow. With baseRev the answer is conflict if the page changed since. Needs write.", true, nil, t.pageMove)
	addTool(t, "page_tag", "Add and remove tags of a page, keeping the other tags in their order and everything else byte for byte. Without baseRev it applies to the page as it is now; with it, conflict if the page changed since. Needs write.", true, nil, t.pageTag)
}

// tooBig is the refusal of text over the limit of a write.
func tooBig(what string, size int) *mcpError {
	return &mcpError{Code: "too_large", Message: what + " is larger than a write may carry.",
		Details: map[string]any{"limitBytes": mcpMaxWriteBody, "sizeBytes": size}}
}

// metaFrom reads a JSON object of header fields in the order it was written,
// numbers kept as they are.
func metaFrom(raw json.RawMessage, name string) (page.Meta, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var m page.Meta
	if err := m.UnmarshalJSON(raw); err != nil {
		return nil, mcpInvalid("%s must be an object of header fields: %v", name, err)
	}
	return m, nil
}

// builtinAt puts a built-in field after the last of the fields a header starts
// with (as a new page's header has them), or replaces its value.
func putBuiltin(meta page.Meta, key string, v any) page.Meta {
	if i := meta.Index(key); i >= 0 {
		meta[i].Value = v
		return meta
	}
	at := 0
	for i, f := range meta {
		switch f.Key {
		case "id", "title", "subtitle", "source", "tags":
			at = i + 1
		}
	}
	out := make(page.Meta, 0, len(meta)+1)
	out = append(out, meta[:at]...)
	out = append(out, page.Field{Key: key, Value: v})
	return append(out, meta[at:]...)
}

func dropField(meta page.Meta, key string) page.Meta {
	if i := meta.Index(key); i >= 0 {
		return append(meta[:i], meta[i+1:]...)
	}
	return meta
}

// cleanTags trims tag names, drops empty ones and repeats, and checks the rest.
func cleanTags(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, tag := range in {
		if tag = strings.TrimSpace(tag); tag == "" {
			continue
		}
		if err := space.CheckTagName(tag); err != nil {
			return nil, err
		}
		if !slices.Contains(out, tag) {
			out = append(out, tag)
		}
	}
	return out, nil
}

func tagValue(tags []string) []any {
	v := make([]any, len(tags))
	for i, t := range tags {
		v[i] = t
	}
	return v
}

// --- page_create ------------------------------------------------------------------

type createIn struct {
	Space     string          `json:"space,omitempty" jsonschema:"space id from spaces_list; may be left out in single-user mode"`
	Folder    string          `json:"folder,omitempty" jsonschema:"folder to make the page in (made if needed); the top level when left out"`
	Title     string          `json:"title,omitempty"`
	Subtitle  string          `json:"subtitle,omitempty"`
	Source    []string        `json:"source,omitempty" jsonschema:"where the text comes from: a list, like tags"`
	Tags      []string        `json:"tags,omitempty"`
	Type      string          `json:"type,omitempty" jsonschema:"the page type, from types_list"`
	Props     json.RawMessage `json:"props,omitempty" jsonschema:"an object of other header fields, e.g. the typed fields of the type"`
	Body      string          `json:"body,omitempty" jsonschema:"the Markdown text, at most 1 MiB"`
	Template  *string         `json:"template,omitempty" jsonschema:"name of a template to make the page from; \"\" for none; left out for the folder's default"`
	TZ        *int            `json:"tz,omitempty" jsonschema:"minutes east of UTC, for the date and time in a template"`
	RequestID string          `json:"requestId,omitempty" jsonschema:"at most 100 characters; the same requestId within an hour returns the first result"`
}

func (t *mcpTools) pageCreate(c *mcpCall, in createIn) (any, error) {
	if len(in.Body) > mcpMaxWriteBody {
		return nil, tooBig("body", len(in.Body))
	}
	if len(in.RequestID) > mcpMaxRequestID {
		return nil, mcpInvalid("requestId is at most %d characters", mcpMaxRequestID)
	}
	props, err := metaFrom(in.Props, "props")
	if err != nil {
		return nil, err
	}
	tags, err := cleanTags(in.Tags)
	if err != nil {
		return nil, err
	}
	if in.TZ != nil && (*in.TZ < -840 || *in.TZ > 840) {
		return nil, mcpInvalid("tz is minutes east of UTC, from -840 to 840")
	}
	var meta page.Meta
	for _, f := range []struct{ k, v string }{{"title", in.Title}, {"subtitle", in.Subtitle}} {
		if v := strings.TrimSpace(f.v); v != "" {
			meta = append(meta, page.Field{Key: f.k, Value: v})
		}
	}
	sources, err := cleanTags(in.Source)
	if err != nil {
		return nil, err
	}
	if len(sources) > 0 {
		meta = append(meta, page.Field{Key: "source", Value: tagValue(sources)})
	}
	if len(tags) > 0 {
		meta = append(meta, page.Field{Key: "tags", Value: tagValue(tags)})
	}
	if v := strings.TrimSpace(in.Type); v != "" {
		meta = append(meta, page.Field{Key: "type", Value: v})
	}
	if meta, err = space.PatchMeta(meta, props, nil); err != nil {
		return nil, err
	}
	create := func() (*mcpItem, error) {
		h, err := c.open(in.Space, true)
		if err != nil {
			return nil, err
		}
		defer h.Release()
		ci := space.CreateInput{Folder: in.Folder, Meta: meta, Body: in.Body}
		tz := 0
		if in.TZ != nil {
			tz = *in.TZ
		}
		quick := in.Folder != "" && page.NameKey(strings.Trim(in.Folder, "/")) == page.NameKey(h.Info.Inbox)
		if err := h.Space.ApplyTemplate(&ci, in.Template, quick, tz); err != nil {
			return nil, err
		}
		p, err := h.Space.Create(c.author(), ci)
		if err != nil {
			return nil, err
		}
		item, err := c.itemOf(h.Info.ID, h.Space, p)
		if err != nil {
			return nil, err
		}
		return &item, nil
	}
	if in.RequestID == "" {
		item, err := create()
		if err != nil {
			return nil, err
		}
		return map[string]any{"page": item, "replayed": false}, nil
	}
	g := c.p.Grant
	item, replayed, err := t.idem.do(c.ctx, g.Kind+"\x00"+g.ID+"\x00"+in.RequestID, create)
	if err != nil {
		return nil, err
	}
	if replayed {
		// The owner may have lost the space, or the right to write in it, since.
		h, err := c.open(in.Space, true)
		if err != nil {
			return nil, err
		}
		h.Release()
	}
	return map[string]any{"page": item, "replayed": replayed}, nil
}

// --- page_update ------------------------------------------------------------------

type editIn struct {
	Old string `json:"old" jsonschema:"the text to replace; not empty, and it must occur exactly once"`
	New string `json:"new" jsonschema:"what to put in its place"`
}

type updateIn struct {
	Space    string          `json:"space,omitempty" jsonschema:"space id from spaces_list; may be left out in single-user mode"`
	ID       string          `json:"id" jsonschema:"the page's id"`
	BaseRev  string          `json:"baseRev" jsonschema:"the rev you read the page at (from page_get or a page item)"`
	Title    *string         `json:"title,omitempty" jsonschema:"\"\" removes it"`
	Subtitle *string         `json:"subtitle,omitempty" jsonschema:"\"\" removes it"`
	Source   *[]string       `json:"source,omitempty" jsonschema:"all the sources the page is to have; [] removes them"`
	Tags     *[]string       `json:"tags,omitempty" jsonschema:"all the tags the page is to have; [] removes them (page_tag adds and removes single tags)"`
	Type     *string         `json:"type,omitempty" jsonschema:"\"\" removes it"`
	Set      json.RawMessage `json:"set,omitempty" jsonschema:"an object of other header fields to set; a null or empty value removes the field"`
	Unset    []string        `json:"unset,omitempty" jsonschema:"other header fields to remove"`
	Body     *string         `json:"body,omitempty" jsonschema:"the whole new body, at most 1 MiB; not together with edits"`
	Edits    []editIn        `json:"edits,omitempty" jsonschema:"replacements applied to the body in order, at most 50"`
}

// applyEdits makes the replacements of edits, each of which must find its text
// exactly once in the body as it is by then.
func applyEdits(body string, edits []editIn) (string, error) {
	for i, e := range edits {
		old := strings.ReplaceAll(e.Old, "\r\n", "\n")
		at := strings.Index(body, old)
		if at < 0 {
			return "", &mcpError{Code: "edit_no_match", Message: "The text of edit " + itoa(i) + " is not in the page; read it again with page_get.", Details: map[string]any{"index": i}}
		}
		// A second occurrence may overlap the first.
		_, size := utf8.DecodeRuneInString(body[at:])
		if strings.Contains(body[at+size:], old) {
			return "", &mcpError{Code: "edit_ambiguous", Message: "The text of edit " + itoa(i) + " is in the page more than once; add some of the text around it to say which.", Details: map[string]any{"index": i}}
		}
		body = body[:at] + strings.ReplaceAll(e.New, "\r\n", "\n") + body[at+len(old):]
	}
	return body, nil
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func (t *mcpTools) pageUpdate(c *mcpCall, in updateIn) (any, error) {
	if in.BaseRev == "" {
		return nil, mcpInvalid("baseRev is required: the rev you read the page at")
	}
	if in.Body != nil && len(in.Edits) > 0 {
		return nil, mcpInvalid("give the body whole or as edits, not both")
	}
	if in.Body != nil && len(*in.Body) > mcpMaxWriteBody {
		return nil, tooBig("body", len(*in.Body))
	}
	if len(in.Edits) > mcpMaxEdits {
		return nil, mcpInvalid("at most %d edits", mcpMaxEdits)
	}
	size := 0
	for i, e := range in.Edits {
		if e.Old == "" {
			return nil, mcpInvalid("edit %d has no old text", i)
		}
		size += len(e.Old) + len(e.New)
	}
	if size > mcpMaxWriteBody {
		return nil, tooBig("the edits", size)
	}
	set, err := metaFrom(in.Set, "set")
	if err != nil {
		return nil, err
	}
	if in.Title == nil && in.Subtitle == nil && in.Source == nil && in.Tags == nil && in.Type == nil && len(set) == 0 && len(in.Unset) == 0 && in.Body == nil && len(in.Edits) == 0 {
		return nil, mcpInvalid("nothing to change: give title, subtitle, source, tags, type, set, unset, body or edits")
	}
	var tags, sources []string
	if in.Tags != nil {
		if tags, err = cleanTags(*in.Tags); err != nil {
			return nil, err
		}
	}
	if in.Source != nil {
		if sources, err = cleanTags(*in.Source); err != nil {
			return nil, err
		}
	}
	if !page.ValidID(in.ID) {
		return nil, mcpErr("not_found", "no such page")
	}
	produce := func(meta page.Meta, body string) (page.Meta, string, error) {
		for _, f := range []struct {
			k string
			v *string
		}{{"title", in.Title}, {"subtitle", in.Subtitle}, {"type", in.Type}} {
			if f.v == nil {
				continue
			}
			if v := strings.TrimSpace(*f.v); v != "" {
				meta = putBuiltin(meta, f.k, v)
			} else {
				meta = dropField(meta, f.k)
			}
		}
		if in.Source != nil {
			if len(sources) > 0 {
				meta = putBuiltin(meta, "source", tagValue(sources))
			} else {
				meta = dropField(meta, "source")
			}
		}
		if in.Tags != nil {
			if len(tags) > 0 {
				meta = putBuiltin(meta, "tags", tagValue(tags))
			} else {
				meta = dropField(meta, "tags")
			}
		}
		meta, err := space.PatchMeta(meta, set, in.Unset)
		if err != nil {
			return nil, "", err
		}
		switch {
		case in.Body != nil:
			body = *in.Body
		case len(in.Edits) > 0:
			if body, err = applyEdits(body, in.Edits); err != nil {
				return nil, "", err
			}
		}
		return meta, body, nil
	}
	// A whole body is the caller's text, based on baseRev, as a save is.
	return t.writePage(c, in.Space, in.ID, in.BaseRev, in.Body != nil, produce)
}

// writePage changes a page as the credential's owner through space.Update (or
// UpdateBody, when the body produce returns is the caller's, based on
// baseRev), as the save of a token does: the owner's draft of it is left alone.
func (t *mcpTools) writePage(c *mcpCall, spaceID, id, baseRev string, ownBody bool, produce func(page.Meta, string) (page.Meta, string, error)) (any, error) {
	h, err := c.open(spaceID, true)
	if err != nil {
		return nil, err
	}
	defer h.Release()
	p, err := c.s.drafts.Write(c.ctx, h.Space, h.Info.ID, c.p.User.ID, id, func(by space.Author) (*space.Page, error) {
		if ownBody {
			return h.Space.UpdateBody(by, id, baseRev, produce)
		}
		return h.Space.Update(by, id, baseRev, produce)
	})
	if err != nil {
		return nil, err
	}
	item, err := c.itemOf(h.Info.ID, h.Space, p)
	if err != nil {
		return nil, err
	}
	return map[string]any{"page": item}, nil
}

// --- page_move --------------------------------------------------------------------

type moveIn struct {
	Space   string `json:"space,omitempty" jsonschema:"space id from spaces_list; may be left out in single-user mode"`
	ID      string `json:"id" jsonschema:"the page's id"`
	Folder  string `json:"folder" jsonschema:"the folder to move it to; \"\" is the top level"`
	BaseRev string `json:"baseRev,omitempty" jsonschema:"the rev you read the page at: conflict if it changed since"`
}

func (t *mcpTools) pageMove(c *mcpCall, in moveIn) (any, error) {
	if !page.ValidID(in.ID) {
		return nil, mcpErr("not_found", "no such page")
	}
	h, err := c.open(in.Space, true)
	if err != nil {
		return nil, err
	}
	defer h.Release()
	p, err := h.Space.MoveIf(c.author(), in.ID, in.Folder, in.BaseRev)
	if err != nil {
		return nil, err
	}
	item, err := c.itemOf(h.Info.ID, h.Space, p)
	if err != nil {
		return nil, err
	}
	return map[string]any{"page": item}, nil
}

// --- page_tag ---------------------------------------------------------------------

type tagIn struct {
	Space   string   `json:"space,omitempty" jsonschema:"space id from spaces_list; may be left out in single-user mode"`
	ID      string   `json:"id" jsonschema:"the page's id"`
	Add     []string `json:"add,omitempty" jsonschema:"tags to add (at most 50)"`
	Remove  []string `json:"remove,omitempty" jsonschema:"tags to remove (at most 50)"`
	BaseRev string   `json:"baseRev,omitempty" jsonschema:"the rev you read the page at: conflict if it changed since"`
}

func (t *mcpTools) pageTag(c *mcpCall, in tagIn) (any, error) {
	if len(in.Add) > mcpMaxTagChanges || len(in.Remove) > mcpMaxTagChanges {
		return nil, mcpInvalid("at most %d tags to add and %d to remove", mcpMaxTagChanges, mcpMaxTagChanges)
	}
	add, err := cleanTags(in.Add)
	if err != nil {
		return nil, err
	}
	remove, err := cleanTags(in.Remove)
	if err != nil {
		return nil, err
	}
	if len(add) == 0 && len(remove) == 0 {
		return nil, mcpInvalid("nothing to change: give add or remove")
	}
	for _, a := range add {
		if slices.Contains(remove, a) {
			return nil, mcpInvalid("the tag %q is both added and removed", a)
		}
	}
	if !page.ValidID(in.ID) {
		return nil, mcpErr("not_found", "no such page")
	}
	produce := func(meta page.Meta, body string) (page.Meta, string, error) {
		cur := meta.Tags()
		next := slices.DeleteFunc(slices.Clone(cur), func(t string) bool { return slices.Contains(remove, t) })
		for _, a := range add {
			if !slices.Contains(next, a) {
				next = append(next, a)
			}
		}
		if slices.Equal(cur, next) {
			return meta, body, nil
		}
		if len(next) == 0 {
			return dropField(meta, "tags"), body, nil
		}
		return putBuiltin(meta, "tags", tagValue(next)), body, nil
	}
	return t.writePage(c, in.Space, in.ID, in.BaseRev, false, produce)
}
