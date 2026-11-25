package space

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// framed is text, one JSON body a line, as the frames of a journal numbered
// from 1. A last line with no end is a frame cut short, as a stop in the
// middle of an append leaves it.
func framed(text string) []byte {
	var buf bytes.Buffer
	var seq uint64
	for _, l := range strings.SplitAfter(text, "\n") {
		body := strings.TrimSuffix(l, "\n")
		if body == "" {
			continue
		}
		seq++
		var f bytes.Buffer
		putFrame(&f, seq, []byte(body))
		if !strings.HasSuffix(l, "\n") {
			f.Truncate(f.Len() - 1 - len(body)/2)
		}
		buf.Write(f.Bytes())
	}
	return buf.Bytes()
}

// journalOps lists the records of a journal file as "op path" (or "op
// from>to"), and checks that its frames are all sound.
func journalOps(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rd := readJournal(data)
	if rd.cut != "" || len(rd.damage) > 0 {
		t.Fatalf("the journal is not sound: %s at %d, %+v", rd.cut, rd.end, rd.damage)
	}
	var out []string
	for _, r := range rd.recs {
		s := r.Op + " " + r.Path
		if r.From != "" {
			s = r.Op + " " + r.From + ">" + r.To
		}
		out = append(out, fmt.Sprintf("%d %s", r.seq, s))
	}
	return out
}

// A journal whose end is cut short by a stop, or damaged from a point on (a
// sum that does not match, bytes that are not a frame, a block of zeros a
// file system left), is read up to there; the rest is cut off at startup, and
// what is appended next reads back.
func TestATornOrDamagedJournalTailIsCutOff(t *testing.T) {
	good := framed(`{"op":"t","p":"a.md"}` + "\n" + `{"op":"t","p":"b.md"}` + "\n")
	var third bytes.Buffer
	putFrame(&third, 3, []byte(`{"op":"t","p":"c.md"}`))
	flipped := bytes.Clone(third.Bytes())
	flipped[frameHead+12] ^= 1
	for _, c := range []struct {
		name, level string
		tail        []byte
	}{
		{"torn frame", "WARN", third.Bytes()[:third.Len()-5]},
		{"torn header", "WARN", third.Bytes()[:10]},
		{"zeros", "WARN", make([]byte, 4096)},
		{"wrong sum", "ERROR", append(flipped, good...)},
		{"not a frame", "ERROR", []byte("{\"op\":\"t\",\"p\":\"an-old-line-with-no-frame.md\"}\n")},
		{"number that does not rise", "ERROR", good[:len(good)/2]},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "pending.journal")
			if err := os.WriteFile(path, append(bytes.Clone(good), c.tail...), 0o644); err != nil {
				t.Fatal(err)
			}
			var logs logBuffer
			j, recs, err := openJournal(path, slog.New(slog.NewTextHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			if len(recs) != 2 || recs[0].Path != "a.md" || recs[1].Path != "b.md" {
				t.Errorf("records read: %+v", recs)
			}
			if l := logs.String(); !strings.Contains(l, "level="+c.level) {
				t.Errorf("the cut is not logged as %s:\n%s", c.level, l)
			}
			if err := j.append([]jrec{{Op: "t", Path: "d.md"}}); err != nil {
				t.Fatal(err)
			}
			j.close()
			if got := strings.Join(journalOps(t, path), ", "); got != "1 t a.md, 2 t b.md, 3 t d.md" {
				t.Errorf("after the next append: %s", got)
			}
		})
	}
}

// The trim keeps the frames after the point the commit took, and those of the
// records still open wherever they are, byte for byte and in their order; the
// paths to commit again follow, numbered on.
func TestTheTrimKeepsWhatCameAfterTheCommitAndWhatIsOpen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "pending.journal")
	j, _, err := openJournal(path, slog.New(slog.NewTextHandler(&logBuffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	var pages []string
	for i := range 3000 {
		pages = append(pages, fmt.Sprintf("Work/%s-%04d.md", strings.Repeat("p", 40), i))
	}
	recs := []jrec{
		{Op: "t", Path: "a.md"},
		{Op: "b", From: "A", To: "B"},
		{Op: "f", Path: ".petalbook/types/task.json", From: "status", To: "state", Rev: "r", Pages: pages},
		{Op: "t", Path: "c.md"},
	}
	if err := j.append(recs); err != nil {
		t.Fatal(err)
	}
	if recs[2].first != 3 || recs[2].seq <= recs[2].first {
		t.Fatalf("the rename's frames: %d to %d", recs[2].first, recs[2].seq)
	}
	upTo := j.lastSeq()
	if err := j.append([]jrec{{Op: "w", Path: "later.md", Rev: "x"}}); err != nil {
		t.Fatal(err)
	}
	last := j.lastSeq()
	if err := j.trim(upTo, []jrec{recs[1], recs[2]}, map[string]Author{"failed.md": {Name: "Ann"}}); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("2 b A>B, %d f status>state, %d w later.md, %d t failed.md", recs[2].seq, last, last+1)
	if got := strings.Join(journalOps(t, path), ", "); got != want {
		t.Errorf("after the trim:\n%s\nwant\n%s", got, want)
	}
	data, _ := os.ReadFile(path)
	if rd := readJournal(data); len(rd.recs[1].Pages) != len(pages) {
		t.Errorf("the open rename kept %d pages of %d", len(rd.recs[1].Pages), len(pages))
	}
	// Numbers go on from there, also after the journal is opened again.
	if err := j.append([]jrec{{Op: "t", Path: "next.md"}}); err != nil {
		t.Fatal(err)
	}
	j.close()
	j2, got, err := openJournal(path, slog.New(slog.NewTextHandler(&logBuffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if n := got[len(got)-1]; n.Path != "next.md" || n.seq != last+2 || j2.lastSeq() != last+2 {
		t.Errorf("the next record: %+v, last %d", n, j2.lastSeq())
	}
	j2.close()
}

// A save whose record is journaled while a commit runs, before or after the
// commit took the pending paths, and whose file is handed to the committer
// only after that commit trimmed the journal: the trim keeps the record, so a
// crash then still leaves the save to be committed by the next start.
func TestASaveJournaledDuringACommitSurvivesTheTrim(t *testing.T) {
	needGit(t)
	for _, when := range []string{"before the snapshot", "after the snapshot"} {
		t.Run(when, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			s := openAt(t, root)
			p := mustCreate(t, s, "", meta("title", "Kept"), "v1\n")
			if err := s.Flush(); err != nil {
				t.Fatal(err)
			}
			// Something pending, so that the commit has work and trims.
			mustCreate(t, s, "", meta("title", "Other"), "x\n")
			recorded, release := make(chan struct{}), make(chan struct{})
			var once, snapOnce sync.Once
			saved := make(chan error, 1)
			save := func() {
				go func() {
					_, err := s.Save(Author{}, p.ID, p.Rev, meta("title", "Kept"), "v2\n")
					saved <- err
				}()
				<-recorded
			}
			s.cm.mu.Lock()
			s.cm.recorded = func() { once.Do(func() { close(recorded); <-release }) }
			if when == "after the snapshot" {
				s.cm.snapshotted = func() { snapOnce.Do(save) }
			}
			s.cm.mu.Unlock()
			if when == "before the snapshot" {
				save()
			}
			if err := s.Flush(); err != nil {
				t.Fatal(err)
			}
			close(release)
			if err := <-saved; err != nil {
				t.Fatal(err)
			}
			crash(s)
			s2 := openAt(t, root)
			if err := s2.Flush(); err != nil {
				t.Fatal(err)
			}
			if got := git(t, root, "show", "HEAD:"+p.Path); !strings.Contains(got, "v2") {
				t.Fatalf("the save was not committed after the crash:\n%s", got)
			}
		})
	}
}

// An append that fails part way (a short write, or a write whose sync fails)
// is cut off again, so the next append is a record of its own: the journal
// reads back with the good records only, never one glued to a fragment.
func TestAFailedAppendLeavesTheJournalReadable(t *testing.T) {
	for _, c := range []struct {
		name  string
		write func(f *os.File, b []byte) (int, error)
		sync  func(f *os.File) error
	}{
		{name: "short write", write: func(f *os.File, b []byte) (int, error) {
			n, _ := f.Write(b[:len(b)/2])
			return n, errors.New("disk full")
		}},
		{name: "failed sync", sync: func(*os.File) error { return errors.New("I/O error") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "pending.journal")
			var logs logBuffer
			log := slog.New(slog.NewTextHandler(&logs, nil))
			j, _, err := openJournal(path, log)
			if err != nil {
				t.Fatal(err)
			}
			if err := j.append([]jrec{{Op: "t", Path: "first.md"}}); err != nil {
				t.Fatal(err)
			}
			j.write, j.sync = c.write, c.sync
			if err := j.append([]jrec{{Op: "w", Path: "lost.md", Rev: strings.Repeat("a", 40)}}); err == nil {
				t.Fatal("the append should have failed")
			}
			j.write, j.sync = nil, nil
			if err := j.append([]jrec{{Op: "t", Path: "good.md"}}); err != nil {
				t.Fatal(err)
			}
			j.close()
			_, recs, err := openJournal(path, log)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, r := range recs {
				got = append(got, r.Op+" "+r.Path)
			}
			if strings.Join(got, ", ") != "t first.md, t good.md" {
				t.Errorf("records read back: %q", got)
			}
			if l := logs.String(); l != "" {
				t.Errorf("the journal was not read cleanly:\n%s", l)
			}
		})
	}
}

// A trim that fails leaves the records it was to keep as they were: the
// numbers it would have given an open record are used again by the next
// append, and must not make the next trim take that append for the record.
func TestAFailedTrimDoesNotLoseAnOpenRecord(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "pending.journal")
	j, _, err := openJournal(path, slog.New(slog.NewTextHandler(&logBuffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	if err := j.append([]jrec{{Op: "t", Path: "a.md"}}); err != nil {
		t.Fatal(err)
	}
	// An open record whose frames are not in the journal: the trim journals it again.
	keep := []jrec{{Op: "b", From: "A", To: "B"}}
	j.replace = func(string, string) error { return errors.New("no space left on device") }
	if err := j.trim(j.lastSeq(), keep, nil); err == nil {
		t.Fatal("the trim should have failed")
	}
	j.replace = nil
	if err := j.append([]jrec{{Op: "t", Path: "x.md"}, {Op: "t", Path: "y.md"}}); err != nil {
		t.Fatal(err)
	}
	if err := j.trim(j.lastSeq(), keep, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(journalOps(t, path), ", "); got != "4 b A>B" {
		t.Errorf("after the trims: %s", got)
	}
	if _, err := os.Stat(path + ".new"); err == nil {
		t.Error("the failed trim left its file behind")
	}
}

// A frame whose length runs past the end of the journal is an append cut
// short only if nothing follows it; with whole lines after it, it is damage.
func TestAFrameCutShortWithLinesAfterItIsDamage(t *testing.T) {
	t.Parallel()
	good := framed(`{"op":"t","p":"a.md"}` + "\n")
	var bad bytes.Buffer
	putFrame(&bad, 2, []byte(`{"op":"t","p":"b.md"}`))
	b := bad.Bytes()
	copy(b, "000fffff") // a length far past the end
	data := append(append(bytes.Clone(good), b...), framed(`{"op":"t","p":"c.md"}`+"\n")...)
	if rd := readJournal(data); rd.cut != "corrupt" || len(rd.recs) != 1 {
		t.Errorf("%s with %d records", rd.cut, len(rd.recs))
	}
	if rd := readJournal(append(bytes.Clone(good), b[:20]...)); rd.cut != "torn" {
		t.Errorf("a short header at the end: %s", rd.cut)
	}
}
