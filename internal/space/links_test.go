package space

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func titleOf(t *testing.T, s *Space, id string) string {
	t.Helper()
	p, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Path
}

func mustSave(t *testing.T, s *Space, p *Page, title, body string) *Page {
	t.Helper()
	q, err := s.Save(Author{}, p.ID, p.Rev, meta("title", title), body)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestRenameRewritesReferrersAndKeepsEverythingElse(t *testing.T) {
	s := open(t)
	target := mustCreate(t, s, "Tang", meta("title", "暮雪"), "body\n")
	body := "前文 **[读  暮雪](../Tang/暮雪.md#小节 \"" + target.ID + "\")**，另见[再看](<../Tang/暮雪.md>)和 `[x](../Tang/暮雪.md)`。\n\n```\n[y](../Tang/暮雪.md)\n```\n"
	ref := mustCreate(t, s, "Notes", meta("title", "引用"), body)
	same := mustCreate(t, s, "Tang", meta("title", "同夹"), "[a](暮雪.md) [b](./暮雪.md)\n")

	n := mustSave(t, s, target, "夜雪", "body\n")
	if n.Path != "Tang/夜雪.md" {
		t.Fatal(n.Path)
	}
	got := readDisk(t, s, ref.Path)
	if !strings.Contains(got, "[读  暮雪](../Tang/夜雪.md#小节 \""+target.ID+"\")") || !strings.Contains(got, "[再看](<../Tang/夜雪.md> \""+target.ID+"\")") {
		t.Fatalf("not rewritten:\n%s", got)
	}
	if !strings.Contains(got, "`[x](../Tang/暮雪.md)`") || !strings.Contains(got, "[y](../Tang/暮雪.md)") {
		t.Fatalf("something else changed:\n%q", got)
	}
	sm := readDisk(t, s, same.Path)
	if !strings.Contains(sm, "[a](夜雪.md \""+target.ID+"\")") || !strings.Contains(sm, "[b](./夜雪.md \""+target.ID+"\")") {
		t.Fatalf("same folder:\n%s", sm)
	}
	bl, _, total, err := s.Backlinks(target.ID, "")
	if err != nil || total != 2 || len(bl) != 2 {
		t.Fatalf("%v %v %v", bl, total, err)
	}
	// One commit holds the rename and the files that follow it.
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if st := git(t, s.Root(), "status", "--porcelain"); st != "" {
		t.Fatalf("uncommitted: %q", st)
	}
	if b, _ := os.ReadFile(filepath.Join(s.StateDir(), "pending.journal")); strings.Contains(string(b), `"op":"l"`) && !strings.Contains(string(b), `"ld"`) {
		t.Fatalf("link record still open: %s", b)
	}
}

func TestMoveRewritesInboundAndOutboundLinks(t *testing.T) {
	s := open(t)
	a := mustCreate(t, s, "A", meta("title", "甲"), "x\n")
	b := mustCreate(t, s, "B", meta("title", "乙"), "x\n")
	c := mustCreate(t, s, "A", meta("title", "丙"), "[to b](../B/乙.md) [to a](甲.md) [ext](https://e.com/x.md) [self](#h)\n")
	r := mustCreate(t, s, "", meta("title", "根"), "[c](A/丙.md \""+c.ID+"\") [a](A/甲.md)\n")
	if _, err := s.Move(Author{}, c.ID, "C/D"); err != nil {
		t.Fatal(err)
	}
	if got := readDisk(t, s, "C/D/丙.md"); !strings.Contains(got, "[to b](../../B/乙.md \""+b.ID+"\") [to a](../../A/甲.md \""+a.ID+"\") [ext](https://e.com/x.md) [self](#h)") {
		t.Fatalf("outbound:\n%s", got)
	}
	if got := readDisk(t, s, r.Path); !strings.Contains(got, "[c](C/D/丙.md \""+c.ID+"\") [a](A/甲.md)") {
		t.Fatalf("inbound:\n%s", got)
	}
	ls, err := s.Links(c.ID)
	if err != nil || len(ls) != 2 || ls[0].Target == nil || ls[0].Target.Title != "乙" {
		t.Fatalf("%+v %v", ls, err)
	}
}

func TestFolderMoveRewritesNestedAndKeepsInternalLinks(t *testing.T) {
	s := open(t)
	out := mustCreate(t, s, "Out", meta("title", "外"), "x\n")
	in1 := mustCreate(t, s, "F/Sub", meta("title", "内一"), "[out](../../Out/外.md) [in2](../内二.md)\n")
	in2 := mustCreate(t, s, "F", meta("title", "内二"), "[in1](Sub/内一.md)\n")
	ref := mustCreate(t, s, "Out", meta("title", "引"), "[1](../F/Sub/内一.md) [2](../F/内二.md \""+in2.ID+"\")\n")
	if _, err := s.MoveFolder(Author{}, "F", "G/H"); err != nil {
		t.Fatal(err)
	}
	if got := readDisk(t, s, "G/H/Sub/内一.md"); !strings.Contains(got, "[out](../../../Out/外.md \""+out.ID+"\") [in2](../内二.md)") {
		t.Fatalf("moved page:\n%s", got)
	}
	if got := readDisk(t, s, "G/H/内二.md"); !strings.Contains(got, "[in1](Sub/内一.md)") {
		t.Fatalf("%s", got)
	}
	if got := readDisk(t, s, ref.Path); !strings.Contains(got, "[1](../G/H/Sub/内一.md \""+in1.ID+"\") [2](../G/H/内二.md \""+in2.ID+"\")") {
		t.Fatalf("referrer:\n%s", got)
	}
	if bl, _, total, _ := s.Backlinks(in1.ID, ""); total != 2 || len(bl) != 2 {
		t.Fatalf("%v", bl)
	}
}

func TestBacklinksPagingAndExcerpt(t *testing.T) {
	s := open(t)
	target := mustCreate(t, s, "", meta("title", "目标"), "x\n")
	long := strings.Repeat("很长的前文。", 60) + "这里[目标](../目标.md)在中间" + strings.Repeat("很长的后文。", 60)
	for i := range 45 {
		body := fmt.Sprintf("段落一\n\n提到 [目标](../目标.md) 第%d 次\n", i)
		if i == 0 {
			body = long + "\n"
		}
		mustCreate(t, s, "R", meta("title", fmt.Sprintf("r%02d", i)), body)
	}
	mustCreate(t, s, "R", meta("title", "无关"), "nothing\n")
	seen := map[string]bool{}
	cursor, pages := "", 0
	for {
		items, next, total, err := s.Backlinks(target.ID, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if total != 45 {
			t.Fatalf("total %d", total)
		}
		for _, b := range items {
			if seen[b.ID] || b.Excerpt == "" {
				t.Fatalf("dup or empty excerpt: %+v", b)
			}
			seen[b.ID] = true
			if b.Title == "r01" && b.Excerpt != "提到 目标 第1 次" {
				t.Fatalf("excerpt %q", b.Excerpt)
			}
			if b.Title == "r00" && (!strings.Contains(b.Excerpt, "目标") || len([]rune(b.Excerpt)) > excerptRunes+2) {
				t.Fatalf("long excerpt %q", b.Excerpt)
			}
		}
		pages++
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 45 || pages != 3 {
		t.Fatalf("%d %d", len(seen), pages)
	}
	if _, _, _, err := s.Backlinks(target.ID, "zz"); err == nil {
		t.Fatal("bad cursor accepted")
	}
	if _, _, _, err := s.Backlinks("01JD3K8Q7Z", ""); err != ErrNotFound {
		t.Fatalf("%v", err)
	}
}

func TestLinksFlagBrokenAndDeleted(t *testing.T) {
	s := open(t)
	a := mustCreate(t, s, "", meta("title", "甲"), "x\n")
	b := mustCreate(t, s, "", meta("title", "乙"), "[a](甲.md) [gone](没有.md) [byid](old/path.md \""+a.ID+"\")\n")
	ls, err := s.Links(b.ID)
	if err != nil || len(ls) != 3 {
		t.Fatal(ls, err)
	}
	if ls[0].Target == nil || ls[1].Target != nil || ls[2].Target == nil || ls[2].Target.ID != a.ID {
		t.Fatalf("%+v", ls)
	}
	if err := s.Delete(Author{}, a.ID); err != nil {
		t.Fatal(err)
	}
	ls, _ = s.Links(b.ID)
	if ls[0].Target != nil || ls[2].Target != nil {
		t.Fatalf("%+v", ls)
	}
	if got := readDisk(t, s, b.Path); !strings.Contains(got, "[a](甲.md)") {
		t.Fatal("a delete changed another file")
	}
}

func TestSaveAddsMissingIDs(t *testing.T) {
	s := open(t)
	a := mustCreate(t, s, "", meta("title", "甲"), "x\n")
	mustCreate(t, s, "", meta("title", "丙"), "x\n")
	b := mustCreate(t, s, "", meta("title", "乙"), "x\n")
	b2 := mustSave(t, s, b, "乙", "[a](甲.md#s) [c](丙.md \"我的标题\") [gone](无.md) [e](https://x.com/a.md)\n")
	got := readDisk(t, s, b2.Path)
	if !strings.Contains(got, "[a](甲.md#s \""+a.ID+"\")") || !strings.Contains(got, "[c](丙.md \"我的标题\")") || !strings.Contains(got, "[gone](无.md)") || !strings.Contains(got, "(https://x.com/a.md)") {
		t.Fatalf("%s", got)
	}
}

// A crash after the file moved and before (all of) the links were rewritten:
// the next start finishes it.
func TestCrashBetweenMoveAndRewriteIsFinished(t *testing.T) {
	needGit(t)
	for _, folder := range []bool{false, true} {
		t.Run(fmt.Sprintf("folder=%v", folder), func(t *testing.T) {
			root := t.TempDir()
			s := openAt(t, root)
			tg := mustCreate(t, s, "Old", meta("title", "目标"), "x\n")
			var refs []*Page
			for i := range 5 {
				refs = append(refs, mustCreate(t, s, "R", meta("title", fmt.Sprintf("r%d", i)), "[t](../Old/目标.md) [u](../Old/目标.md#a \""+tg.ID+"\")\n"))
			}
			s.Flush()
			from, to := "Old/目标.md", "New/目标.md"
			rec := jrec{Op: "l", From: from, To: to}
			if folder {
				rec = jrec{Op: "l", From: "Old", To: "New", Dir: true}
			}
			if err := s.cm.Record(Author{}, jrec{Op: "t", Path: refs[0].Path}, jrec{Op: "t", Path: from}, jrec{Op: "t", Path: to}, rec); err != nil {
				t.Fatal(err)
			}
			if folder {
				os.Rename(filepath.Join(root, "Old"), filepath.Join(root, "New"))
			} else {
				os.MkdirAll(filepath.Join(root, "New"), 0o755)
				os.Rename(filepath.Join(root, from), filepath.Join(root, to))
			}
			// One referrer was already rewritten (its journal record says so).
			one := filepath.Join(root, refs[0].Path)
			b, _ := os.ReadFile(one)
			os.WriteFile(one, []byte(strings.ReplaceAll(string(b), "../Old/目标.md", "../New/目标.md")), 0o644)
			crash(s)

			s2 := openAt(t, root)
			for _, r := range refs {
				got := readDisk(t, s2, r.Path)
				if strings.Contains(got, "Old/") || strings.Count(got, "../New/目标.md") != 2 || !strings.Contains(got, "[u](../New/目标.md#a \""+tg.ID+"\")") {
					t.Fatalf("%s:\n%s", r.Path, got)
				}
			}
			if bl, _, total, _ := s2.Backlinks(tg.ID, ""); total != 5 || len(bl) != 5 {
				t.Fatalf("total %d", total)
			}
			s2.Flush()
			if st := git(t, root, "status", "--porcelain"); st != "" {
				t.Fatalf("not committed: %q", st)
			}
			if b, _ := os.ReadFile(filepath.Join(s2.StateDir(), "pending.journal")); len(b) != 0 {
				t.Fatalf("journal: %s", b)
			}
		})
	}
}

func TestExternalRenameIsRepairedAtStart(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	tg := mustCreate(t, s, "", meta("title", "目标"), "x\n")
	dup := mustCreate(t, s, "", meta("title", "重复"), "x\n")
	ref := mustCreate(t, s, "", meta("title", "引"), "[t](目标.md \""+tg.ID+"\") [d](重复.md \""+dup.ID+"\") [z](x.md \"01JD3K8Q7Z\") [p](目标.md)\n")
	s.Close()
	os.MkdirAll(filepath.Join(root, "N"), 0o755)
	os.Rename(filepath.Join(root, "目标.md"), filepath.Join(root, "N/改名.md"))
	// Two files with the id of 重复: ambiguous, left alone.
	b, _ := os.ReadFile(filepath.Join(root, "重复.md"))
	os.WriteFile(filepath.Join(root, "重复 copy.md"), b, 0o644)
	os.Rename(filepath.Join(root, "重复.md"), filepath.Join(root, "N/重复.md"))

	s2 := openAt(t, root)
	got := readDisk(t, s2, ref.Path)
	if !strings.Contains(got, "[t](N/改名.md \""+tg.ID+"\")") || !strings.Contains(got, "[d](重复.md \""+dup.ID+"\")") || !strings.Contains(got, "[z](x.md \"01JD3K8Q7Z\")") || !strings.Contains(got, "[p](目标.md)") {
		t.Fatalf("%s", got)
	}
	s2.Flush()
	if c := commits(t, root); len(c) == 0 {
		t.Fatal("no commit")
	}
}

func TestRemoteRenameIsOnePushWithTheReferrers(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	tg := mustCreate(t, f.s, "T", meta("title", "目标"), "x\n")
	for i := range 3 {
		mustCreate(t, f.s, "R", meta("title", fmt.Sprintf("r%d", i)), "[t](../T/目标.md)\n")
	}
	before := len(f.remoteLog())
	if _, err := f.s.Save(Author{}, tg.ID, tg.Rev, meta("title", "新名"), "x\n"); err != nil {
		t.Fatal(err)
	}
	if got := f.remoteLog(); len(got) != before+1 {
		t.Fatalf("%v", got)
	}
	files := git(t, f.remote, "show", "main:R/r1.md")
	if !strings.Contains(files, "[t](../T/新名.md \""+tg.ID+"\")") {
		t.Fatalf("%s", files)
	}
	if st := git(t, f.root, "status", "--porcelain"); st != "" {
		t.Fatalf("status %q", st)
	}
	consistent(t, f.s)
}

func TestPullRepairsLinksAfterAnExternalRename(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	tg := mustCreate(t, f.s, "", meta("title", "目标"), "x\n")
	mustCreate(t, f.s, "", meta("title", "引"), "[t](目标.md \""+tg.ID+"\")\n")
	o := f.theirs()
	b, _ := os.ReadFile(filepath.Join(o.dir, "目标.md"))
	os.MkdirAll(filepath.Join(o.dir, "N"), 0o755)
	os.Remove(filepath.Join(o.dir, "目标.md"))
	o.write("N/改名.md", string(b))
	o.push("rename outside")
	before := len(f.remoteLog())
	if err := f.s.Pull(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	got := f.remoteLog()
	if len(got) != before+1 {
		t.Fatalf("expected one service commit: %v", got)
	}
	if txt := git(t, f.remote, "show", "main:引.md"); !strings.Contains(txt, "[t](N/改名.md \""+tg.ID+"\")") {
		t.Fatalf("%s", txt)
	}
	if st := git(t, f.root, "status", "--porcelain"); st != "" {
		t.Fatalf("status %q", st)
	}
	consistent(t, f.s)
}

// PETALBOOK_TIMING=1: renaming a page that 1,000 pages link to, in a space of
// 50,000 pages.
func TestTimingRenameWithManyReferrers(t *testing.T) {
	if os.Getenv("PETALBOOK_TIMING") == "" {
		t.Skip("set PETALBOOK_TIMING=1")
	}
	needGit(t)
	root := t.TempDir()
	const total, refs = 50000, 1000
	tid := "0000000001"
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Target/目标.md", pageText(tid, "目标", "x\n"))
	for i := 1; i < total; i++ {
		id := fmt.Sprintf("%010X", i+1)
		body := fmt.Sprintf("段落 %d。\n", i)
		if i <= refs {
			body += "提到 [目标](../Target/目标.md \"" + tid + "\") 在这里。\n"
		}
		write(fmt.Sprintf("d%03d/p%d.md", i%200, i), pageText(id, fmt.Sprintf("页 %d", i), body))
	}
	start := time.Now()
	s := openAt(t, root)
	t.Logf("open (index %d pages): %v", total, time.Since(start))
	p, err := s.Get(tid)
	if err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	n, err := s.Save(Author{}, tid, p.Rev, meta("title", "新目标"), "x\n")
	d := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rename with %d referrers: %v", refs, d)
	if _, _, tot, _ := s.Backlinks(tid, ""); tot != refs {
		t.Fatalf("backlinks %d", tot)
	}
	if got := readDisk(t, s, "d001/p1.md"); !strings.Contains(got, "../Target/新目标.md") {
		t.Fatalf("%s", got)
	}
	start = time.Now()
	if _, err := s.Move(Author{}, tid, "Other"); err != nil {
		t.Fatal(err)
	}
	t.Logf("move with %d referrers: %v (%s)", refs, time.Since(start), n.Path)
	start = time.Now()
	s.Flush()
	t.Logf("commit: %v", time.Since(start))
	start = time.Now()
	s.Backlinks(tid, "")
	t.Logf("first page of backlinks: %v", time.Since(start))
}
