package space

import "testing"

// A rebased save logs its rewrites as steps of the text it was sent (TextRev).
// When that text is an older version of the file, the step that version
// already has is the one that counts: a draft based on it still follows it.
func TestRebasedSaveKeepsTheStepOfAnOlderVersion(t *testing.T) {
	t.Parallel()
	s := open(t)
	b := mustCreate(t, s, "B", meta("title", "乙"), "x\n")
	c := mustCreate(t, s, "C", meta("title", "丁"), "x\n")
	const v0Body = "[看](../B/乙.md \"看乙\") [和](../C/丁.md \"看丁\")\n"
	a := mustCreate(t, s, "A", meta("title", "甲"), v0Body)
	mustSave(t, s, b, "丙", "x\n") // a: v0 -> v1
	v1, err := s.Get(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	v2 := mustSave(t, s, v1, "甲", v1.Body+"又\n")
	mustSave(t, s, c, "戊", "x\n") // a: v2 -> v3
	// Saved on v2 with the text of v0: the step of C's rename is made to it.
	p, err := s.Save(Author{}, a.ID, v2.Rev, meta("title", "甲"), v0Body)
	if err != nil {
		t.Fatal(err)
	}
	if p.TextRev != a.Rev {
		t.Fatalf("the sent text is v0: textRev %s, v0 %s", p.TextRev, a.Rev)
	}
	if ch := s.rb.chain(a.ID, a.Rev); len(ch) == 0 || ch[0].New != v1.Rev {
		t.Fatalf("the step of v0 was replaced: %+v", ch)
	}
}
