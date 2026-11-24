package store

import (
	"fmt"
	"testing"
)

func TestSplitStatementsKeepsDialectMarks(t *testing.T) {
	src := `-- a comment; with a semicolon
CREATE TABLE a (id TEXT COLLATE "C" PRIMARY KEY);
-- dialect: postgres
ALTER TABLE a SET (fillfactor = 85);
CREATE INDEX a_id ON a(id);
-- dialect: sqlite
CREATE INDEX a_x ON a(id)`
	got, err := splitStatements(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("%d statements: %q", len(got), got)
	}
	want := []Dialect{"", Postgres, "", SQLite}
	for i, st := range got {
		if st.dialect != want[i] {
			t.Errorf("statement %d is for %q, want %q: %s", i, st.dialect, want[i], st.sql)
		}
	}
	run := func(d Dialect) string {
		var out []string
		for _, st := range got {
			if s, ok := st.forDialect(d); ok {
				out = append(out, s)
			}
		}
		return fmt.Sprint(out)
	}
	if s := run(SQLite); s != `[CREATE TABLE a (id TEXT COLLATE BINARY PRIMARY KEY) CREATE INDEX a_id ON a(id) CREATE INDEX a_x ON a(id)]` {
		t.Errorf("SQLite runs %s", s)
	}
	if s := run(Postgres); s != `[CREATE TABLE a (id TEXT COLLATE "C" PRIMARY KEY) ALTER TABLE a SET (fillfactor = 85) CREATE INDEX a_id ON a(id)]` {
		t.Errorf("PostgreSQL runs %s", s)
	}
	for _, bad := range []string{"-- dialect: oracle\nSELECT 1", "SELECT\n-- dialect: postgres\n1"} {
		if _, err := splitStatements(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestLocaleProblem(t *testing.T) {
	for _, c := range []struct {
		l    map[string]string
		warn bool
	}{
		{map[string]string{"datcollate": "C", "datctype": "C"}, false},
		{map[string]string{"datcollate": "C", "datctype": "C.UTF-8", "datlocprovider": "c"}, false},
		{map[string]string{"datcollate": "POSIX", "datctype": "C.utf8"}, false},
		{map[string]string{"datcollate": "en_US.UTF-8", "datctype": "en_US.UTF-8", "datlocprovider": "b", "datlocale": "C.UTF-8"}, false},
		{map[string]string{"datcollate": "en_US.UTF-8", "datctype": "en_US.UTF-8", "datlocprovider": "c"}, true},
		{map[string]string{"datcollate": "C", "datctype": "en_US.UTF-8"}, true},
		{map[string]string{"datcollate": "C", "datctype": "C", "datlocprovider": "i", "datlocale": "und"}, true},
	} {
		if got := localeProblem(c.l) != ""; got != c.warn {
			t.Errorf("%v: warned %v, want %v", c.l, got, c.warn)
		}
	}
}
