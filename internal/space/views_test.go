package space

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/index"
)

func folderScope(f string) ViewScope { return ViewScope{Folder: &f} }
func tagScope(t string) ViewScope    { return ViewScope{Tag: t} }

func tagsYML(root string) string {
	b, _ := os.ReadFile(filepath.Join(root, ".petalbook", "tags.yml"))
	return string(b)
}

func setTagsYML(t *testing.T, root, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".petalbook"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".petalbook", "tags.yml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// putViews saves views over the ones there are now.
func putViews(s *Space, scope ViewScope, views ...View) (ViewList, error) {
	cur, err := s.Views(scope)
	if err != nil {
		return ViewList{}, err
	}
	return s.PutViews(Author{}, scope, cur.Rev, views)
}

func mustPutViews(t *testing.T, s *Space, scope ViewScope, views ...View) ViewList {
	t.Helper()
	l, err := putViews(s, scope, views...)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func names(l ViewList) string {
	var out []string
	for _, v := range l.Views {
		out = append(out, v.Name)
	}
	return strings.Join(out, ",")
}

func board() View {
	return View{Name: "按状态", Layout: "board", Group: "status", Sort: "-prop:written",
		Filter: []ViewFilter{{Key: "series", Op: "in", Values: []any{"冬季"}}}}
}

func TestViewsAreSavedUnderTheFolderAndComeBackInOrder(t *testing.T) {
	s := open(t)
	mustCreate(t, s, "文案", meta("title", "x"), "x\n")
	l, err := s.Views(folderScope("文案"))
	if err != nil || len(l.Views) != 0 || l.Rev == "" {
		t.Fatalf("%+v %v", l, err)
	}
	got := mustPutViews(t, s, folderScope("文案"), board(),
		View{Name: "表格", Layout: "table", Hidden: []string{"series", "written"}, Filter: []ViewFilter{{Key: "points", Op: "range", Values: []any{1, nil}}}},
		View{Name: "读", Layout: "read", Sort: "title"})
	if names(got) != "按状态,表格,读" || got.Rev == l.Rev {
		t.Fatalf("%+v", got)
	}
	seen := map[string]bool{}
	for _, v := range got.Views {
		if len(v.ID) != viewIDLen || seen[v.ID] {
			t.Errorf("ids: %+v", got.Views)
		}
		seen[v.ID] = true
	}
	// The file says what the design says.
	y := spaceYML(s.Root())
	for _, want := range []string{"文案:", "views:", "name: 按状态", "layout: board", "group: status", "sort: -prop:written",
		"filter: [{key: series, op: in, values: [冬季]}]", "hidden: [series, written]", "{key: points, op: range, values: [1, null]}"} {
		if !strings.Contains(y, want) {
			t.Errorf("space.yml does not say %q:\n%s", want, y)
		}
	}
	// What comes back is what was saved, with the ids; and the same from a fresh read of the file.
	again, _ := s.Views(folderScope("文案"))
	if !reflect.DeepEqual(again, got) {
		t.Errorf("read back:\n%+v\nsaved:\n%+v", again, got)
	}
	if v := got.Views[1]; !reflect.DeepEqual(v.Filter, []ViewFilter{{"points", "range", []any{int64(1), nil}}}) || !reflect.DeepEqual(v.Hidden, []string{"series", "written"}) {
		t.Errorf("%+v", v)
	}
	// The committed settings are the same.
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if st := git(t, s.Root(), "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Errorf("not committed: %q", st)
	}
	// The root folder is "".
	mustPutViews(t, s, folderScope(""), View{Name: "all", Layout: "cards"})
	if !strings.Contains(spaceYML(s.Root()), `"":`) {
		t.Errorf("%s", spaceYML(s.Root()))
	}
	if l, _ := s.Views(folderScope("")); names(l) != "all" {
		t.Errorf("%+v", l)
	}
	// A view is written back without change: nothing is written.
	before := spaceYML(s.Root())
	cur, _ := s.Views(folderScope("文案"))
	same, err := s.PutViews(Author{}, folderScope("文案"), cur.Rev, cur.Views)
	if err != nil || same.Rev != cur.Rev || spaceYML(s.Root()) != before {
		t.Errorf("%v\n%s", err, spaceYML(s.Root()))
	}
	// Removing them all takes the key and the entry with them (the type stays).
	if _, err := putCurrent(s, "t", TypeInput{}); err != nil {
		t.Fatal(err)
	}
	s.BindFolder(Author{}, "文案", "t")
	mustPutViews(t, s, folderScope(""))
	if l := mustPutViews(t, s, folderScope("文案")); len(l.Views) != 0 {
		t.Errorf("%+v", l)
	}
	if y := spaceYML(s.Root()); strings.Contains(y, "views:") || !strings.Contains(y, "type: t") {
		t.Errorf("%s", y)
	}
}

func TestViewsAreCheckedAndTheFolderMustExist(t *testing.T) {
	s := open(t)
	mustCreate(t, s, "F", meta("title", "x"), "x\n")
	cur, _ := s.Views(folderScope("F"))
	ok := View{Name: "v", Layout: "list"}
	for name, c := range map[string]struct {
		views []View
		want  string
	}{
		"no name":          {[]View{{Layout: "list"}}, "needs a name"},
		"a blank name":     {[]View{{Name: "  ", Layout: "list"}}, "needs a name"},
		"a long name":      {[]View{{Name: strings.Repeat("长", 100), Layout: "list"}}, "name"},
		"a line break":     {[]View{{Name: "a\nb", Layout: "list"}}, "name"},
		"no layout":        {[]View{{Name: "v"}}, "layout"},
		"an unknown one":   {[]View{{Name: "v", Layout: "gantt"}}, "layout"},
		"a bad sort":       {[]View{{Name: "v", Layout: "list", Sort: "size"}}, "unknown sort"},
		"a sort of a prop": {[]View{{Name: "v", Layout: "list", Sort: "prop:"}}, "property"},
		"a bad filter op":  {[]View{{Name: "v", Layout: "list", Filter: []ViewFilter{{Key: "a", Op: "like"}}}}, "unknown filter op"},
		"a filter, no key": {[]View{{Name: "v", Layout: "list", Filter: []ViewFilter{{Op: "empty"}}}}, "key"},
		"hidden twice":     {[]View{{Name: "v", Layout: "table", Hidden: []string{"a", "a"}}}, "hidden"},
		"a bad id":         {[]View{{ID: "a b", Name: "v", Layout: "list"}}, "id"},
		"an unknown id":    {[]View{{ID: "zzzz", Name: "v", Layout: "list"}}, "no view with the id"},
		"21 views":         {make21(), "at most 20"},
	} {
		_, err := s.PutViews(Author{}, folderScope("F"), cur.Rev, c.views)
		wantInvalid(t, name, err, c.want)
	}
	// 20 are fine.
	if l, err := s.PutViews(Author{}, folderScope("F"), cur.Rev, make21()[:20]); err != nil || len(l.Views) != 20 {
		t.Fatalf("%v", err)
	}
	// An id twice.
	l, _ := s.Views(folderScope("F"))
	_, err := s.PutViews(Author{}, folderScope("F"), l.Rev, []View{l.Views[0], l.Views[0]})
	wantInvalid(t, "an id twice", err, "twice")
	// The scope.
	if _, err := s.PutViews(Author{}, folderScope("Nowhere"), cur.Rev, []View{ok}); !errors.Is(err, ErrNoFolder) {
		t.Errorf("a folder that is not there: %v", err)
	}
	_, err = s.PutViews(Author{}, ViewScope{}, cur.Rev, nil)
	wantInvalid(t, "no scope", err, "folder or the tag")
	f := "F"
	_, err = s.PutViews(Author{}, ViewScope{Folder: &f, Tag: "t"}, cur.Rev, nil)
	wantInvalid(t, "both", err, "not both")
	bad := "../x"
	_, err = s.PutViews(Author{}, ViewScope{Folder: &bad}, cur.Rev, nil)
	wantInvalid(t, "a path", err, "")
	_, err = s.PutViews(Author{}, tagScope(" "), cur.Rev, nil)
	wantInvalid(t, "a tag", err, "tag")
	if _, err := s.Views(ViewScope{}); err == nil {
		t.Error("no scope for a read")
	}
	// A tag that is in no page is fine.
	if _, err := putViews(s, tagScope("nobody-has-it"), ok); err != nil {
		t.Error(err)
	}
}

func make21() []View {
	var out []View
	for i := 0; i < 21; i++ {
		out = append(out, View{Name: fmt.Sprintf("v%d", i), Layout: "list"})
	}
	return out
}

func TestViewsConflictWhenTheRevIsNotTheCurrentOne(t *testing.T) {
	s := open(t)
	mustCreate(t, s, "F", meta("title", "x"), "x\n")
	empty, _ := s.Views(folderScope("F"))
	first := mustPutViews(t, s, folderScope("F"), View{Name: "one", Layout: "list"})
	// The rev of the empty list is stale now.
	_, err := s.PutViews(Author{}, folderScope("F"), empty.Rev, []View{{Name: "mine", Layout: "list"}})
	var ce *ViewsConflictError
	if !errors.As(err, &ce) || !reflect.DeepEqual(ce.Current, first) {
		t.Fatalf("%v", err)
	}
	if names(first) != "one" || spaceYMLHas(s, "mine") {
		t.Error("something was written")
	}
	// A bogus rev is a conflict too, and so is a hand edit of the file.
	_, err = s.PutViews(Author{}, folderScope("F"), "0123", nil)
	if !errors.As(err, &ce) {
		t.Errorf("%v", err)
	}
	setSpaceYML(t, s.Root(), strings.Replace(spaceYML(s.Root()), "name: one", "name: edited", 1))
	_, err = s.PutViews(Author{}, folderScope("F"), first.Rev, []View{{Name: "mine", Layout: "list"}})
	if !errors.As(err, &ce) || names(ce.Current) != "edited" {
		t.Errorf("a hand edit: %v", err)
	}
	// The same for a tag.
	te, _ := s.Views(tagScope("t"))
	mustPutViews(t, s, tagScope("t"), View{Name: "one", Layout: "list"})
	if _, err := s.PutViews(Author{}, tagScope("t"), te.Rev, nil); !errors.As(err, &ce) {
		t.Errorf("a tag: %v", err)
	}
}

func spaceYMLHas(s *Space, text string) bool { return strings.Contains(spaceYML(s.Root()), text) }

func TestSavingViewsKeepsCommentsAndKeysItDoesNotKnow(t *testing.T) {
	s := open(t)
	root := s.Root()
	mustCreate(t, s, "F", meta("title", "x"), "x\n")
	setSpaceYML(t, root, `# the settings of my space
name: My notes
colour: green        # a key of mine
folders:
  # the writing folder
  F:
    type: wenan      # bound
    note: mine
    views:
      # the default
      - id: k3f9
        name: 按状态    # board
        layout: board
        group: status
        owner: ann
        meta: {a: [1, 2], b: x}
        filter: [{key: series, op: in, values: [冬季]}]
      - id: m2p7
        name: 读
        layout: read
      - not a view
`)
	cur, _ := s.Views(folderScope("F"))
	if len(cur.Views) != 2 || cur.Views[0].ID != "k3f9" || cur.Views[0].Extra["owner"] != "ann" ||
		!reflect.DeepEqual(cur.Views[0].Extra["meta"], map[string]any{"a": []any{int64(1), int64(2)}, "b": "x"}) {
		t.Fatalf("%+v", cur.Views)
	}
	// Rename the first, change its sort, swap the order, add one, drop none.
	first, second := cur.Views[0], cur.Views[1]
	first.Name = "按状态（新）"
	first.Sort = "title"
	first.Extra = map[string]any{"owner": "bob"} // the other extra key is not mentioned: it stays
	got, err := s.PutViews(Author{}, folderScope("F"), cur.Rev, []View{second, first, {Name: "new", Layout: "cards", Extra: map[string]any{"x": int64(3)}}})
	if err != nil {
		t.Fatal(err)
	}
	if names(got) != "读,按状态（新）,new" || got.Views[1].Extra["meta"] == nil || got.Views[1].Extra["owner"] != "bob" || got.Views[2].Extra["x"] != int64(3) {
		t.Fatalf("%+v", got.Views)
	}
	y := spaceYML(root)
	for _, want := range []string{"# the settings of my space", "colour: green # a key of mine", "# the writing folder", "type: wenan # bound",
		"note: mine", "# the default", "name: 按状态（新） # board", "owner: bob", "meta: {a: [1, 2], b: x}", "- not a view", "x: 3",
		"filter: [{key: series, op: in, values: [冬季]}]", "sort: title"} {
		if !strings.Contains(y, want) {
			t.Errorf("the file lost %q:\n%s", want, y)
		}
	}
	if strings.Index(y, "id: m2p7") > strings.Index(y, "id: k3f9") {
		t.Errorf("the views were not reordered:\n%s", y)
	}
	// The binding is read as before.
	if bindingMap(s)["F"] != "wenan" {
		t.Errorf("%v", bindingMap(s))
	}
	// Dropping a view drops its comment and nothing else; dropping the last one
	// leaves the entry's other settings.
	if _, err := s.PutViews(Author{}, folderScope("F"), got.Rev, []View{got.Views[0]}); err != nil {
		t.Fatal(err)
	}
	if _, err := putViews(s, folderScope("F")); err != nil {
		t.Fatal(err)
	}
	y = spaceYML(root)
	if strings.Contains(y, "views:") || strings.Contains(y, "# the default") || !strings.Contains(y, "note: mine") || !strings.Contains(y, "type: wenan") || !strings.Contains(y, "# the writing folder") {
		t.Errorf("%s", y)
	}
	// A file that is only comments and a name keeps them when the first view is saved.
	setSpaceYML(t, root, "# only a comment\n")
	mustPutViews(t, s, folderScope("F"), View{Name: "v", Layout: "list"})
	if y := spaceYML(root); !strings.Contains(y, "# only a comment") || !strings.Contains(y, "views:") {
		t.Errorf("%s", y)
	}
}

func TestValuesOfAFilterKeepTheirTypes(t *testing.T) {
	s := open(t)
	mustCreate(t, s, "F", meta("title", "x"), "x\n")
	vals := []any{"冬季", "2026-11-22", "5", "true", "null", "a: b", "", int64(7), 2.5, true}
	got := mustPutViews(t, s, folderScope("F"), View{Name: "v", Layout: "list", Filter: []ViewFilter{{Key: "k", Op: "in", Values: vals}}})
	again, _ := s.Views(folderScope("F"))
	if !reflect.DeepEqual(again.Views[0].Filter[0].Values, vals) || !reflect.DeepEqual(got, again) {
		t.Errorf("%#v\n%s", again.Views[0].Filter[0].Values, spaceYML(s.Root()))
	}
	// Other tools read the same: the texts that look like something else are quoted.
	y := spaceYML(s.Root())
	for _, want := range []string{`"2026-11-22"`, `"5"`, `"true"`, `"null"`} {
		if !strings.Contains(y, want) {
			t.Errorf("%s is not quoted:\n%s", want, y)
		}
	}
	if strings.Contains(y, "!!") {
		t.Errorf("a tag was written:\n%s", y)
	}
}

func TestViewIDsAreUniqueInTheWholeSpace(t *testing.T) {
	s := open(t)
	for _, f := range []string{"A", "B", "C"} {
		mustCreate(t, s, f, meta("title", "x"), "x\n")
	}
	seen := map[string]bool{}
	add := func(scope ViewScope) {
		var vs []View
		for i := 0; i < MaxViews; i++ {
			vs = append(vs, View{Name: fmt.Sprintf("v%d", i), Layout: "list"})
		}
		for _, v := range mustPutViews(t, s, scope, vs...).Views {
			if seen[v.ID] || !validViewID(v.ID) {
				t.Fatalf("%q is used twice or not an id", v.ID)
			}
			seen[v.ID] = true
		}
	}
	add(folderScope("A"))
	add(folderScope("B"))
	add(tagScope("t1"))
	add(tagScope("t2"))
	add(folderScope("C"))
	if len(seen) != 5*MaxViews {
		t.Errorf("%d", len(seen))
	}
	// An id of one place cannot be claimed in another.
	var a View
	l, _ := s.Views(folderScope("A"))
	a = l.Views[0]
	cur, _ := s.Views(folderScope("B"))
	_, err := s.PutViews(Author{}, folderScope("B"), cur.Rev, []View{a})
	wantInvalid(t, "an id of another folder", err, "no view with the id")
	// Making an id takes one that is free.
	taken := map[string]bool{}
	for i := 0; i < 2000; i++ {
		id, err := newViewID(taken)
		if err != nil || len(id) != viewIDLen {
			t.Fatal(id, err)
		}
	}
	if len(taken) != 2000 {
		t.Errorf("an id was made twice")
	}
}

func TestViewsOfATagLiveBesideItsColour(t *testing.T) {
	s := open(t)
	root := s.Root()
	setTagsYML(t, root, "# my tags\ntags:\n  栀子:\n    color: rose   # pretty\n    note: x\n  其他:\n    color: blue\n")
	l := mustPutViews(t, s, tagScope("栀子"), View{Name: "表格", Layout: "table", Hidden: []string{"a"}}, View{Name: "连读", Layout: "read"})
	y := tagsYML(root)
	for _, want := range []string{"# my tags", "color: rose # pretty", "note: x", "views:", "name: 表格", "hidden: [a]", "color: blue"} {
		if !strings.Contains(y, want) {
			t.Errorf("tags.yml lost %q:\n%s", want, y)
		}
	}
	if got, _ := s.Views(tagScope("栀子")); !reflect.DeepEqual(got, l) {
		t.Errorf("%+v", got)
	}
	if c := s.TagColors(); c["栀子"] != "rose" || c["其他"] != "blue" {
		t.Errorf("%v", c)
	}
	// Changing the colour keeps the views, and removing it too.
	if err := s.SetTagColor(Author{}, "栀子", "teal"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTagColor(Author{}, "栀子", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Views(tagScope("栀子")); !reflect.DeepEqual(got, l) || strings.Contains(tagsYML(root), "rose") {
		t.Errorf("%+v\n%s", got, tagsYML(root))
	}
	// A tag with views and no colour: the colour is made beside them.
	if err := s.SetTagColor(Author{}, "栀子", "red"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Views(tagScope("栀子")); !reflect.DeepEqual(got, l) || s.TagColors()["栀子"] != "red" {
		t.Errorf("%+v", got)
	}
	// The views of the other tag are not touched.
	if got, _ := s.Views(tagScope("其他")); len(got.Views) != 0 {
		t.Errorf("%+v", got)
	}
	// Removing the views leaves the colour, and with neither the entry goes, the
	// file goes when nothing is left to say.
	mustPutViews(t, s, tagScope("栀子"))
	if y := tagsYML(root); strings.Contains(y, "views") || !strings.Contains(y, "color: red") {
		t.Errorf("%s", y)
	}
	if err := s.SetTagColor(Author{}, "栀子", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTagColor(Author{}, "其他", ""); err != nil {
		t.Fatal(err)
	}
	if y := tagsYML(root); strings.Contains(y, "color") || !strings.Contains(y, "# my tags") || !strings.Contains(y, "note: x") {
		t.Errorf("what is not the server's stays:\n%s", y)
	}
	os.Remove(filepath.Join(root, ".petalbook", "tags.yml"))
	mustPutViews(t, s, tagScope("新"), View{Name: "v", Layout: "list"})
	mustPutViews(t, s, tagScope("新"))
	if _, err := os.Stat(filepath.Join(root, ".petalbook", "tags.yml")); !os.IsNotExist(err) {
		t.Errorf("the empty file is still there: %v", err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if st := git(t, root, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Errorf("%q", st)
	}
}

func TestViewsInSettingsThatCannotBeReadAreNotWrittenOver(t *testing.T) {
	s := open(t)
	mustCreate(t, s, "F", meta("title", "x"), "x\n")
	broken := []byte("name: x\nfolders: [unclosed\n")
	file := filepath.Join(s.Root(), ".petalbook", "space.yml")
	os.MkdirAll(filepath.Dir(file), 0o755)
	os.WriteFile(file, broken, 0o644)
	cur, _ := s.Views(folderScope("F"))
	if len(cur.Views) != 0 {
		t.Errorf("%+v", cur)
	}
	_, err := s.PutViews(Author{}, folderScope("F"), cur.Rev, []View{{Name: "v", Layout: "list"}})
	wantInvalid(t, "space.yml", err, "fixed by hand")
	if got, _ := os.ReadFile(file); string(got) != string(broken) {
		t.Errorf("the file was written: %s", got)
	}
	setTagsYML(t, s.Root(), "tags: [unclosed\n")
	_, err = s.PutViews(Author{}, tagScope("t"), cur.Rev, []View{{Name: "v", Layout: "list"}})
	wantInvalid(t, "tags.yml", err, "fixed by hand")
	// `folders` or an entry that is not a mapping.
	os.WriteFile(file, []byte("folders: 5\n"), 0o644)
	_, err = putViews(s, folderScope("F"), View{Name: "v", Layout: "list"})
	wantInvalid(t, "folders is a number", err, "fixed by hand")
	os.WriteFile(file, []byte("folders:\n  F: 5\n"), 0o644)
	_, err = putViews(s, folderScope("F"), View{Name: "v", Layout: "list"})
	wantInvalid(t, "an entry that is a number", err, "fixed by hand")
	// An entry with nothing under it is made a mapping.
	os.WriteFile(file, []byte("folders:\n  F:\n"), 0o644)
	if _, err = putViews(s, folderScope("F"), View{Name: "v", Layout: "list"}); err != nil {
		t.Error(err)
	}
}

func TestViewsFollowTheirFolderWhenItMovesAndGoWhenItIsDeleted(t *testing.T) {
	s := open(t)
	root := s.Root()
	if _, err := putCurrent(s, "t", TypeInput{}); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, "随笔/2026", meta("title", "一"), "x\n")
	mustCreate(t, s, "随笔/2026/深", meta("title", "二"), "x\n")
	mustCreate(t, s, "随笔twin", meta("title", "孪"), "x\n")
	mustCreate(t, s, "别处", meta("title", "三"), "x\n")
	if _, err := s.BindFolder(Author{}, "随笔", "t"); err != nil {
		t.Fatal(err)
	}
	for f, n := range map[string]string{"随笔": "top", "随笔/2026": "year", "随笔/2026/深": "deep", "随笔twin": "twin", "别处": "else"} {
		mustPutViews(t, s, folderScope(f), View{Name: n, Layout: "list"})
	}
	viewNames := func() map[string]string {
		out := map[string]string{}
		for f := range s.config().views {
			l, _ := s.Views(folderScope(f))
			out[f] = names(l)
		}
		return out
	}
	idsBefore := map[string]string{}
	for f := range s.config().views {
		l, _ := s.Views(folderScope(f))
		idsBefore[l.Views[0].Name] = l.Views[0].ID
	}
	if _, err := s.MoveFolder(Author{}, "随笔", "归档/旧"); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"归档/旧": "top", "归档/旧/2026": "year", "归档/旧/2026/深": "deep", "随笔twin": "twin", "别处": "else"}
	if got := viewNames(); !reflect.DeepEqual(got, want) {
		t.Errorf("after the move: %v", got)
	}
	// The views are the same views: the ids are, and the type goes with them.
	if l, _ := s.Views(folderScope("归档/旧/2026")); l.Views[0].ID != idsBefore["year"] {
		t.Errorf("the id changed: %+v", l)
	}
	if bindingMap(s)["归档/旧"] != "t" {
		t.Errorf("%v", bindingMap(s))
	}
	if l, _ := s.Views(folderScope("随笔")); len(l.Views) != 0 {
		t.Errorf("the old place still has views: %+v", l)
	}
	if _, err := s.MoveFolder(Author{}, "归档/旧", "归档/新"); err != nil {
		t.Fatal(err)
	}
	if got := viewNames(); got["归档/新/2026"] != "year" || got["归档/旧"] != "" {
		t.Errorf("after the rename: %v", got)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := git(t, root, "show", "HEAD:.petalbook/space.yml"); !strings.Contains(got, "归档/新/2026") || strings.Contains(got, "归档/旧") {
		t.Errorf("committed settings:\n%s", got)
	}
	// A move onto stale entries: nothing was at the place, so its views are stale.
	setSpaceYML(t, root, spaceYML(root)+"  stale:\n    views:\n      - {id: stale1, name: stale, layout: list}\n")
	if _, err := s.MoveFolder(Author{}, "别处", "stale"); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.Views(folderScope("stale")); names(l) != "else" {
		t.Errorf("%+v", l)
	}
	// Deleting a folder takes its views and those of the folders in it.
	if _, err := s.DeleteFolder(Author{}, "归档/新", true); err != nil {
		t.Fatal(err)
	}
	if got := viewNames(); !reflect.DeepEqual(got, map[string]string{"随笔twin": "twin", "stale": "else"}) {
		t.Errorf("after the delete: %v", got)
	}
	if bindingMap(s)["归档/新"] != "" {
		t.Errorf("%v", bindingMap(s))
	}
	// A folder that stays (a file in it is not a page) keeps them.
	os.WriteFile(filepath.Join(root, "stale", "note.txt"), []byte("mine"), 0o644)
	if _, err := s.DeleteFolder(Author{}, "stale", true); err != nil {
		t.Fatal(err)
	}
	if got := viewNames(); got["stale"] != "else" {
		t.Errorf("a folder that is still there: %v", got)
	}
	if _, err := s.DeleteFolder(Author{}, "随笔twin", true); err != nil {
		t.Fatal(err)
	}
	if y := spaceYML(root); strings.Contains(y, "随笔twin") || !strings.Contains(y, "stale") {
		t.Errorf("%s", y)
	}
}

func TestDeletingATypeKeepsTheViewsOfItsFolders(t *testing.T) {
	s := open(t)
	if _, err := putCurrent(s, "a", TypeInput{}); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, "F", meta("title", "x"), "x\n")
	mustCreate(t, s, "G", meta("title", "y"), "x\n")
	s.BindFolder(Author{}, "F", "a")
	s.BindFolder(Author{}, "G", "a")
	mustPutViews(t, s, folderScope("F"), View{Name: "v", Layout: "list"})
	if err := deleteCurrent(s, "a"); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.Views(folderScope("F")); names(l) != "v" || len(s.Bindings()) != 0 {
		t.Errorf("%+v %v", l, s.Bindings())
	}
	if y := spaceYML(s.Root()); strings.Contains(y, "G") || !strings.Contains(y, "F:") {
		t.Errorf("%s", y)
	}
	// Unbinding a folder keeps its views, and binding keeps them.
	s.BindFolder(Author{}, "F", "")
	if l, _ := s.Views(folderScope("F")); names(l) != "v" {
		t.Errorf("%+v", l)
	}
}

func TestViewWritesAreJournaledAndCommitted(t *testing.T) {
	s := open(t)
	mustCreate(t, s, "F", meta("title", "x"), "x\n")
	mustPutViews(t, s, folderScope("F"), View{Name: "v", Layout: "list"})
	mustPutViews(t, s, tagScope("t"), View{Name: "v", Layout: "list"})
	b, err := os.ReadFile(filepath.Join(s.StateDir(), "pending.journal"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`.petalbook/space.yml`, `.petalbook/tags.yml`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the journal does not name %s:\n%s", want, b)
		}
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	tree := git(t, s.Root(), "ls-tree", "-r", "--name-only", "HEAD")
	for _, want := range []string{".petalbook/space.yml", ".petalbook/tags.yml"} {
		if !strings.Contains(tree, want) {
			t.Errorf("%s is not committed:\n%s", want, tree)
		}
	}
}

// --- a crash in the middle of a folder move or delete ----------------------------------

func viewsAt(s *Space, folder string) string {
	l, _ := s.Views(folderScope(folder))
	return names(l)
}

func TestCrashAfterAFolderRenameFinishesTheViewsAtRecovery(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	mustCreate(t, s, "A/sub", meta("title", "P"), "x\n")
	mustCreate(t, s, "Keep", meta("title", "K"), "x\n")
	mustPutViews(t, s, folderScope("A"), View{Name: "top", Layout: "list"})
	mustPutViews(t, s, folderScope("A/sub"), View{Name: "low", Layout: "board", Group: "status"})
	mustPutViews(t, s, folderScope("Keep"), View{Name: "keep", Layout: "list"})
	s.Flush()
	ids := map[string]string{}
	l, _ := s.Views(folderScope("A/sub"))
	ids["low"] = l.Views[0].ID
	crash(s)
	// The rename was made; the edit of space.yml never was.
	if err := os.Rename(filepath.Join(root, "A"), filepath.Join(root, "B")); err != nil {
		t.Fatal(err)
	}
	rec := []byte(`{"op":"b","f":"A","to":"B"}` + "\n")
	if err := os.WriteFile(journalFile(root), rec, 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := openAt(t, root)
	if viewsAt(s2, "B") != "top" || viewsAt(s2, "B/sub") != "low" || viewsAt(s2, "A") != "" || viewsAt(s2, "Keep") != "keep" {
		t.Fatalf("after recovery:\n%s", spaceYML(root))
	}
	if l, _ := s2.Views(folderScope("B/sub")); l.Views[0].ID != ids["low"] || l.Views[0].Group != "status" {
		t.Errorf("%+v", l)
	}
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := git(t, root, "show", "HEAD:.petalbook/space.yml"); !strings.Contains(got, "B/sub") {
		t.Errorf("the recovered settings are not committed:\n%s", got)
	}
	// Once more, as if the crash came again before the journal was trimmed.
	crash(s2)
	before := spaceYML(root)
	os.WriteFile(journalFile(root), rec, 0o644)
	s3 := openAt(t, root)
	if spaceYML(root) != before || viewsAt(s3, "B/sub") != "low" {
		t.Errorf("a second recovery changed the file:\n%s", spaceYML(root))
	}
	// A move that never happened (the old folder is there) moves nothing.
	crash(s3)
	os.Rename(filepath.Join(root, "B"), filepath.Join(root, "A"))
	os.WriteFile(journalFile(root), []byte(`{"op":"b","f":"A","to":"B"}`+"\n"), 0o644)
	s4 := openAt(t, root)
	if viewsAt(s4, "B") != "top" || viewsAt(s4, "A") != "" {
		t.Errorf("%s", spaceYML(root))
	}
}

func TestCrashAfterAFolderDeleteDropsItsViewsAtRecovery(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	if _, err := putCurrent(s, "t", TypeInput{}); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, "G/deep", meta("title", "P"), "x\n")
	mustCreate(t, s, "Keep", meta("title", "Q"), "x\n")
	for _, f := range []string{"G", "G/deep", "Keep"} {
		mustPutViews(t, s, folderScope(f), View{Name: f, Layout: "list"})
	}
	s.BindFolder(Author{}, "G", "t")
	s.Flush()
	crash(s)
	// The files are gone; space.yml was not edited.
	if err := os.RemoveAll(filepath.Join(root, "G")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journalFile(root), []byte(`{"op":"u","p":"G"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := openAt(t, root)
	if viewsAt(s2, "G") != "" || viewsAt(s2, "G/deep") != "" || viewsAt(s2, "Keep") != "Keep" || len(s2.Bindings()) != 0 {
		t.Errorf("after recovery:\n%s", spaceYML(root))
	}
	// A folder that is still there keeps its views.
	crash(s2)
	mustCreate2 := func() {
		os.MkdirAll(filepath.Join(root, "Keep"), 0o755)
	}
	mustCreate2()
	os.WriteFile(journalFile(root), []byte(`{"op":"u","p":"Keep"}`+"\n"), 0o644)
	s3 := openAt(t, root)
	if viewsAt(s3, "Keep") != "Keep" {
		t.Errorf("%s", spaceYML(root))
	}
}

func TestAViewsEntryThatIsNotAListIsNotAView(t *testing.T) {
	s := open(t)
	mustCreate(t, s, "F", meta("title", "x"), "x\n")
	for _, y := range []string{"folders:\n  F:\n    views: 5\n", "folders:\n  F:\n    views:\n      - plain\n      - [a]\n", "folders:\n  F:\n    views:\n"} {
		setSpaceYML(t, s.Root(), y)
		l, err := s.Views(folderScope("F"))
		if err != nil || len(l.Views) != 0 {
			t.Errorf("%q: %+v %v", y, l, err)
		}
		// Saving a view replaces what is not a list; a list keeps its odd items.
		if _, err := putViews(s, folderScope("F"), View{Name: "v", Layout: "list"}); err != nil {
			t.Errorf("%q: %v", y, err)
		}
	}
	if y := spaceYML(s.Root()); !strings.Contains(y, "- plain") && !strings.Contains(y, "name: v") {
		t.Errorf("%s", y)
	}
}

func TestViewJSON(t *testing.T) {
	var v View
	if err := json.Unmarshal([]byte(`{"id":"a1","name":"n","layout":"table","hidden":["x"],"filter":[{"key":"k","op":"in","values":[1,"a",true]}],"custom":{"n":2},"sort":null}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.Sort != "" || !reflect.DeepEqual(v.Filter[0].Values, []any{int64(1), "a", true}) || !reflect.DeepEqual(v.Extra, map[string]any{"custom": map[string]any{"n": int64(2)}}) {
		t.Errorf("%+v", v)
	}
	out, _ := json.Marshal(View{Name: "n", Layout: "list"})
	if string(out) != `{"filter":[],"group":"","hidden":[],"id":"","layout":"list","name":"n","sort":""}` {
		t.Errorf("%s", out)
	}
	for _, bad := range []string{`[]`, `5`, `{"name":5}`, `{"filter":{}}`, `{"filter":[{"key":"k","extra":1}]}`, `{"hidden":"x"}`, `{"id":[1]}`} {
		if err := json.Unmarshal([]byte(bad), &v); err == nil {
			t.Errorf("%s was taken", bad)
		}
	}
}

// --- through the remote commit path ----------------------------------------------------

func TestViewsGoThroughTheRemoteCommitPath(t *testing.T) {
	f := newRemoteSpace(t)
	remoteFile := func(rel string) string { return git(t, f.remote, "show", "main:"+rel) }
	mustCreate(t, f.s, "Tang/Sub", meta("title", "暮雪"), "x\n")
	n := len(f.remoteLog())
	l := mustPutViews(t, f.s, folderScope("Tang"), View{Name: "表", Layout: "table"})
	if got := f.remoteLog(); len(got) != n+1 || !strings.Contains(remoteFile(".petalbook/space.yml"), "name: 表") {
		t.Fatalf("%v", got)
	}
	mustPutViews(t, f.s, tagScope("花"), View{Name: "连", Layout: "read"})
	if got := f.remoteLog(); len(got) != n+2 || !strings.Contains(remoteFile(".petalbook/tags.yml"), "name: 连") {
		t.Fatalf("%v", got)
	}
	// The move and the views are one commit.
	if _, err := f.s.MoveFolder(Author{}, "Tang", "唐"); err != nil {
		t.Fatal(err)
	}
	if got := f.remoteLog(); len(got) != n+3 {
		t.Fatalf("%v", got)
	}
	if y := remoteFile(".petalbook/space.yml"); !strings.Contains(y, "唐:") || strings.Contains(y, "Tang") {
		t.Errorf("%s", y)
	}
	// With the remote down, a write leaves nothing behind.
	f.net.setDown(true)
	before := snapshot(t, f.s)
	cur, _ := f.s.Views(folderScope("唐"))
	_, err := f.s.PutViews(Author{}, folderScope("唐"), cur.Rev, nil)
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("%v", err)
	}
	tcur, _ := f.s.Views(tagScope("花"))
	_, err = f.s.PutViews(Author{}, tagScope("花"), tcur.Rev, nil)
	if !errors.As(err, &ue) {
		t.Fatalf("a tag: %v", err)
	}
	if now := snapshot(t, f.s); !reflect.DeepEqual(before, now) {
		t.Errorf("changed something:\n%+v\n%+v", before, now)
	}
	if got, _ := f.s.Views(folderScope("唐")); names(got) != names(l) {
		t.Errorf("%+v", got)
	}
	f.net.setDown(false)
	consistent(t, f.s)
}

// --- sorting by the options of a select field ---------------------------------------------

func TestAListingSortedByASelectFollowsTheOrderOfItsOptions(t *testing.T) {
	s := open(t)
	if _, err := putCurrent(s, "task", TypeInput{Title: "task", Fields: []FieldInput{
		{Key: "status", Kind: KindSelect, Options: []string{"todo", "doing", "done"}},
		{Key: "labels", Kind: KindMultiSelect, Options: []string{"b", "a"}},
		{Key: "note", Kind: KindText},
	}}); err != nil {
		t.Fatal(err)
	}
	mk := func(folder, title string, kv ...any) {
		mustCreate(t, s, folder, meta(append([]any{"title", title}, kv...)...), "x\n")
	}
	mk("Work", "A", "status", "done", "labels", []any{"a", "b"})
	mk("Work", "B", "status", "todo", "labels", []any{"b"})
	mk("Work", "C", "status", "doing", "labels", []any{"a"})
	mk("Work", "D", "status", "zzz", "note", "n")
	mk("Work", "E")
	mk("Plain", "P", "status", "todo")
	mk("Plain", "Q", "status", "doing")
	mk("Plain", "R", "status", "aaa")
	if _, err := s.BindFolder(Author{}, "Work", "task"); err != nil {
		t.Fatal(err)
	}
	order := func(q index.Query) string {
		t.Helper()
		var out []string
		for {
			res, err := s.List(q)
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range res.Items {
				out = append(out, it.Label)
			}
			if res.Next == "" {
				return strings.Join(out, "")
			}
			q.Cursor = res.Next
		}
	}
	work, plain := "Work", "Plain"
	for _, c := range []struct {
		name string
		q    index.Query
		want string
	}{
		{"a select, ascending", index.Query{Folder: &work, Sort: "prop:status", Limit: 2}, "BCADE"},
		{"a select, descending", index.Query{Folder: &work, Sort: "-prop:status", Limit: 2}, "DACBE"},
		{"a multiselect by its first value", index.Query{Folder: &work, Sort: "prop:labels", Limit: 2}, "BACDE"},
		{"a field that is not a select", index.Query{Folder: &work, Sort: "prop:note", Limit: 2}, "DABCE"},
		{"a field the type does not have", index.Query{Folder: &work, Sort: "prop:other", Limit: 2}, "ABCDE"},
		{"a folder with no type: the text", index.Query{Folder: &plain, Sort: "prop:status", Limit: 2}, "RQP"},
		{"without a folder: the text", index.Query{Sort: "prop:status", Limit: 20, Filters: []index.Filter{{Key: "status", Op: "in", Values: []any{"todo", "doing", "done"}}}}, ""},
	} {
		got := order(c.q)
		if c.want == "" {
			// Not a folder: the order of the text (doing, doing, done, todo, todo), the ids of the pages decide among equals.
			if len(got) != 5 || got[2] != 'A' {
				t.Errorf("%s: %s", c.name, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	// Editing the options changes the order at once.
	if _, err := putCurrent(s, "task", TypeInput{Title: "task", Fields: []FieldInput{
		{Key: "status", Kind: KindSelect, Options: []string{"done", "doing", "todo"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if got := order(index.Query{Folder: &work, Sort: "prop:status", Limit: 20}); got != "ACBDE" {
		t.Errorf("after reordering the options: %s", got)
	}
	// A type that is broken by hand leaves the order of the text.
	writeType(t, s.Root(), "task", "{not json")
	if got := order(index.Query{Folder: &work, Sort: "prop:status", Limit: 20}); got != "CABDE" {
		t.Errorf("a broken type: %s", got)
	}
}

// --- the bodies of a listing --------------------------------------------------------------

func TestBodiesOfAListingAreReadFromTheirFiles(t *testing.T) {
	s := open(t)
	a := mustCreate(t, s, "F", meta("title", "A"), "第一篇\n\n正文\n")
	b := mustCreate(t, s, "F", meta("title", "B"), "")
	res, err := s.List(index.Query{Folder: strp("F"), Sort: "title"})
	if err != nil {
		t.Fatal(err)
	}
	bodies, err := s.Bodies(res.Items)
	if err != nil || len(bodies) != 2 || bodies[0] != a.Body || bodies[1] != "" {
		t.Errorf("%q %v", bodies, err)
	}
	// A file that was changed since the index took it: the body is of the file.
	os.WriteFile(filepath.Join(s.Root(), a.Path), []byte("---\nid: "+a.ID+"\ntitle: A\n---\n改过了\n"), 0o644)
	if bodies, _ = s.Bodies(res.Items); bodies[0] != "改过了\n" {
		t.Errorf("%q", bodies)
	}
	// A file that is not the page any more, or is gone, has no body.
	os.WriteFile(filepath.Join(s.Root(), a.Path), []byte("---\nid: ZZZZZZZZZZ\n---\n别的\n"), 0o644)
	os.Remove(filepath.Join(s.Root(), b.Path))
	if bodies, _ = s.Bodies(res.Items); bodies[0] != "" || bodies[1] != "" {
		t.Errorf("%q", bodies)
	}
	many := make([]index.Summary, MaxBodies+1)
	_, err = s.Bodies(many)
	wantInvalid(t, "too many", err, "at most")
}

func strp(s string) *string { return &s }
