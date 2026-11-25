// Search and listing at scale: can a local SQLite index serve tens of
// thousands of Chinese pages without a separate search service?
//
// It generates N Markdown pages on disk, then for each full-text scheme:
// rebuilds the index from the files, and times searches, listings and a
// single-page update.
//
//	go run . -n 50000 -dir /tmp
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

// --- corpus ---------------------------------------------------------------

var words = strings.Fields(`栀子 玫瑰 玉兰 白玉兰 蔷薇 山茶 百合 雏菊 向日葵 夜来香 曼陀罗 桂花 银杏 红梅 牡丹 月季 满天星 干花 花瓣 花海 花期 花香 花开 花落 落花 鲜花 白花 野花 荆棘 芳草 葳蕤 枯藤 落叶 叶子 土壤 泥土 露珠 朝露 细雨 雨水 雨季 霜雪 飘雪 寒风 海风 微风 狂风 晚风 风浪 海浪 海水 大海 深海 海底 海边 沙滩 沙砾 礁石 鲸鱼 溪流 流水 河道 月光 月亮 月夜 繁星 星辰 星光 夕阳 落日 日落 残阳 余晖 暮色 暮光 黄昏 黎明 朝阳 太阳 白昼 夜晚 长夜 冬夜 盛夏 仲夏 夏天 初春 春天 春末 深冬 冬天 冬日 严冬 秋风 蝉鸣 炊烟 灯火 烟火 万家灯火 小巷 青石 柴门 天台 窗外 床头 枕头 冰箱 玻璃瓶 香薰 香槟 橘子 请柬 情书 锦书 诗 誓言 诺言 夙愿 遗愿 回忆 记忆 梦 美梦 梦里 梦中 理想 灵魂 生命 一生 一辈子 下辈子 来世 轮回 永恒 永远 尽头 结局 真相 深渊 尘埃 尘世 红尘 人间 乱世 浮生 蜉蝣 众生 世间 上帝 救赎 罪孽 忏悔 坟墓 死亡 尸体 腐烂 腐朽 凋零 枯萎 泯灭 消失 沉睡 长眠 搁浅 殉情 陪葬 吻别 拥吻 共舞 相遇 相见 相恋 相伴 分离 错过 遗落 遗忘 忘却 铭记 记得 思念 挂念 眷恋 痴迷 爱慕 悸动 期盼 等待 释怀 庆幸 可惜 遗憾 忧伤 忧郁 落寞 孤独 寂寞 悲观 矫情 自私 幼稚 真诚 虚伪 热烈 素雅 妩媚 妖娆 凄美 浪漫 暧昧 缠绵 朦胧 澄澈 皎洁 炽热 温婉 冰冷 湿冷 潮湿 清冷 昏暗 绯红 殷红 洁白 枯黄 微蓝 姑娘 少年 女子 爱人 故人 旧人 路人 朋友 男孩 老妇人 种花人 赏花人 护花使者 采花大盗 摆渡人 辩手 仙子 大灰狼 小红帽 我们 他们 你我 自己 什么 时候 为什么 怎么 如果 那么 所以 因为 但是 可是 只是 原来 后来 曾经 如今 今年 去年 那年 昨夜 今夜 今朝 下次 下一次 最后 从未 早已 终究 或许 也许 大概 一定 永不 不会 不再 没有 还有 只有 唯有 还好 至少 哪怕 即使 一切 所有 许多 无数 每一朵 那朵 一束 一支 一片 一抹 一瞥 一刹`)

var punct = []string{"，", "，", "，", "。", "。", "？", "！", "；", "、", "……"}

type page struct {
	id, path, folder, title, subtitle, typ string
	tags                                   []string
	created, updated                       int64
	body                                   string
}

// word picks with a skew so some words are common and some rare, as in real text.
func word(r *rand.Rand) string {
	return words[int(float64(len(words))*r.Float64()*r.Float64())]
}

func sentence(r *rand.Rand) string {
	var b strings.Builder
	n := 4 + r.Intn(12)
	for i := 0; i < n; i++ {
		b.WriteString(word(r))
		if i < n-1 && r.Intn(5) == 0 {
			b.WriteString(punct[r.Intn(3)])
		}
	}
	b.WriteString(punct[3+r.Intn(len(punct)-3)])
	return b.String()
}

func body(r *rand.Rand, chars int) string {
	var b strings.Builder
	for b.Len()/3 < chars {
		for i, n := 0, 1+r.Intn(5); i < n; i++ {
			b.WriteString(sentence(r))
		}
		b.WriteString("\n\n")
	}
	return b.String()
}

func generate(n int, seed int64) []page {
	r := rand.New(rand.NewSource(seed))
	folders := make([]string, 0, 400)
	for i := 0; i < 40; i++ {
		top := word(r) + fmt.Sprint(i)
		folders = append(folders, top)
		for j := 0; j < 9; j++ {
			folders = append(folders, top+"/"+word(r)+fmt.Sprint(j))
		}
	}
	types := []string{"", "", "文案", "文案", "笔记", "随笔"}
	pages := make([]page, n)
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	for i := range pages {
		var size int
		switch k := r.Intn(100); {
		case k < 70:
			size = 50 + r.Intn(250) // short copy
		case k < 95:
			size = 500 + r.Intn(2500) // notes
		default:
			size = 5000 + r.Intn(15000) // long essays
		}
		folder := folders[r.Intn(len(folders))]
		title := word(r) + word(r)
		id := fmt.Sprintf("%010x", r.Uint64()&0xffffffffff)
		tags := make([]string, r.Intn(5))
		for t := range tags {
			tags[t] = word(r)
		}
		created := base + r.Int63n(6*365*86400)
		pages[i] = page{
			id: id, folder: folder, path: fmt.Sprintf("%s/%s-%s.md", folder, title, id[:4]),
			title: title, subtitle: sentence(r), typ: types[r.Intn(len(types))], tags: tags,
			created: created, updated: created + r.Int63n(90*86400), body: body(r, size),
		}
	}
	// Needles: a phrase that exists in exactly three pages, and a Latin word.
	for _, i := range []int{n / 7, n / 3, n - 5} {
		pages[i].body += "这里藏着一句独一无二的话：琥珀色的鲸落在第七个雨夜。\n"
	}
	for _, i := range []int{n / 9, n / 2} {
		pages[i].body += "Deployed with PetalBook on a rainy night.\n"
	}
	return pages
}

func (p page) file() string {
	var b strings.Builder
	fmt.Fprintf(&b, "---\nid: %s\ntitle: %s\nsubtitle: %s\ntags: [%s]\ncreated: %s\n", p.id, p.title, p.subtitle,
		strings.Join(p.tags, ", "), time.Unix(p.created, 0).UTC().Format("2006-01-02"))
	if p.typ != "" {
		fmt.Fprintf(&b, "type: %s\n", p.typ)
	}
	b.WriteString("---\n")
	b.WriteString(p.body)
	return b.String()
}

// parse reads the handful of header fields the index needs. A real
// implementation uses a YAML parser; the cost is of the same order.
func parse(rel string, data []byte, mod int64) page {
	p := page{path: rel, folder: filepath.Dir(rel), updated: mod}
	s := string(data)
	if strings.HasPrefix(s, "---\n") {
		if end := strings.Index(s[4:], "\n---\n"); end >= 0 {
			for _, line := range strings.Split(s[4:4+end], "\n") {
				k, v, _ := strings.Cut(line, ": ")
				switch k {
				case "id":
					p.id = v
				case "title":
					p.title = v
				case "subtitle":
					p.subtitle = v
				case "type":
					p.typ = v
				case "tags":
					if v = strings.Trim(v, "[]"); v != "" {
						p.tags = strings.Split(v, ", ")
					}
				case "created":
					if t, err := time.Parse("2006-01-02", v); err == nil {
						p.created = t.Unix()
					}
				}
			}
			s = s[4+end+5:]
		}
	}
	p.body = s
	return p
}

// --- full-text schemes ------------------------------------------------------

type scheme struct {
	name, tokenizer string
	// index transforms text before it goes into the FTS table.
	index func(string) string
	// query turns what a person typed into an FTS5 MATCH expression, or ""
	// when this scheme cannot answer it from the index.
	query func(string) string
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

// splitCJK puts every CJK character in its own token so the stock tokenizer
// indexes single characters; a phrase query then matches any run of them.
func splitCJK(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 4 / 3)
	for _, r := range s {
		if isCJK(r) {
			b.WriteByte(' ')
			b.WriteRune(r)
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func phrase(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

var schemes = []scheme{
	{
		name: "trigram", tokenizer: "trigram",
		index: func(s string) string { return s },
		query: func(q string) string {
			var parts []string
			for _, term := range strings.Fields(q) {
				if len([]rune(term)) < 3 {
					return "" // trigram cannot match fewer than three characters
				}
				parts = append(parts, phrase(term))
			}
			return strings.Join(parts, " AND ")
		},
	},
	{
		name: "per-character", tokenizer: "unicode61",
		index: splitCJK,
		query: func(q string) string {
			var parts []string
			for _, term := range strings.Fields(q) {
				parts = append(parts, phrase(strings.TrimSpace(splitCJK(term))))
			}
			return strings.Join(parts, " AND ")
		},
	},
}

// --- timing helpers ---------------------------------------------------------

func median(run func() int, times int) (time.Duration, int) {
	ds := make([]time.Duration, times)
	n := 0
	for i := range ds {
		t := time.Now()
		n = run()
		ds[i] = time.Since(t)
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds[times/2], n
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func ms(d time.Duration) string { return fmt.Sprintf("%.1f ms", float64(d.Microseconds())/1000) }

func count(db *sql.DB, q string, args ...any) int {
	rows, err := db.Query(q, args...)
	must(err)
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	must(rows.Err())
	return n
}

// --- main -------------------------------------------------------------------

func main() {
	n := flag.Int("n", 50000, "number of pages")
	dir := flag.String("dir", "", "existing directory to work in; a petalbook-search-scale subdirectory is created there (required)")
	keep := flag.Bool("keep", false, "keep generated files")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "-dir is required")
		os.Exit(2)
	}
	// Everything goes into one subdirectory of -dir that this program creates
	// and owns; -dir itself is never emptied or removed.
	work := filepath.Join(*dir, "petalbook-search-scale")
	if _, err := os.Stat(work); err == nil {
		fmt.Fprintf(os.Stderr, "%s already exists; remove it or choose another -dir\n", work)
		os.Exit(2)
	}
	space := filepath.Join(work, "space")
	must(os.MkdirAll(space, 0o755))
	if !*keep {
		defer os.RemoveAll(work)
	}

	t := time.Now()
	pages := generate(*n, 1)
	var chars, bytes int
	for _, p := range pages {
		f := p.file()
		full := filepath.Join(space, p.path)
		must(os.MkdirAll(filepath.Dir(full), 0o755))
		must(os.WriteFile(full, []byte(f), 0o644))
		bytes += len(f)
		chars += len([]rune(p.body))
	}
	fmt.Printf("corpus: %d pages, %.1f M characters, %.0f MB on disk, generated in %s\n\n",
		*n, float64(chars)/1e6, float64(bytes)/1e6, time.Since(t).Round(time.Millisecond))

	searches := []string{"栀子花", "琥珀色的鲸落", "第七个雨夜", "栀子", "黄昏", "海", "栀子 黄昏", "白玉兰 搁浅 殉情", "PetalBook"}

	for _, sc := range schemes {
		dbPath := filepath.Join(work, sc.name+".db")
		db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-65536)")
		must(err)
		db.SetMaxOpenConns(1)
		_, err = db.Exec(`
			CREATE TABLE pages (
				rowid INTEGER PRIMARY KEY, id TEXT UNIQUE NOT NULL, path TEXT NOT NULL, folder TEXT NOT NULL,
				title TEXT, subtitle TEXT, type TEXT, created INTEGER, updated INTEGER, excerpt TEXT);
			CREATE INDEX pages_folder ON pages(folder, updated DESC);
			CREATE INDEX pages_updated ON pages(updated DESC);
			CREATE TABLE tags (tag TEXT NOT NULL, page INTEGER NOT NULL, PRIMARY KEY (tag, page)) WITHOUT ROWID;
			CREATE VIRTUAL TABLE fts USING fts5(title, subtitle, body, content='', contentless_delete=1, tokenize='` + sc.tokenizer + `');`)
		must(err)

		// Full rebuild: walk the space, read every file, index it.
		t = time.Now()
		tx, err := db.Begin()
		must(err)
		insPage, _ := tx.Prepare(`INSERT INTO pages(id,path,folder,title,subtitle,type,created,updated,excerpt) VALUES(?,?,?,?,?,?,?,?,?)`)
		insTag, _ := tx.Prepare(`INSERT OR IGNORE INTO tags(tag,page) VALUES(?,?)`)
		insFts, _ := tx.Prepare(`INSERT INTO fts(rowid,title,subtitle,body) VALUES(?,?,?,?)`)
		must(filepath.WalkDir(space, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			info, _ := d.Info()
			rel, _ := filepath.Rel(space, path)
			p := parse(rel, data, info.ModTime().Unix())
			excerpt := []rune(p.body)
			if len(excerpt) > 200 {
				excerpt = excerpt[:200]
			}
			res, err := insPage.Exec(p.id, p.path, p.folder, p.title, p.subtitle, p.typ, p.created, p.updated, string(excerpt))
			if err != nil {
				return err
			}
			row, _ := res.LastInsertId()
			for _, tag := range p.tags {
				if _, err := insTag.Exec(tag, row); err != nil {
					return err
				}
			}
			_, err = insFts.Exec(row, sc.index(p.title), sc.index(p.subtitle), sc.index(p.body))
			return err
		}))
		must(tx.Commit())
		rebuild := time.Since(t)
		_, err = db.Exec(`INSERT INTO fts(fts) VALUES('optimize'); PRAGMA wal_checkpoint(TRUNCATE);`)
		must(err)
		st, _ := os.Stat(dbPath)
		fmt.Printf("== %s\n", sc.name)
		fmt.Printf("  full rebuild from files   %s   (%.0f pages/s)\n", rebuild.Round(time.Millisecond), float64(*n)/rebuild.Seconds())
		fmt.Printf("  index size                %.0f MB  (%.0f%% of the text)\n", float64(st.Size())/1e6, 100*float64(st.Size())/float64(bytes))

		fmt.Println("  search (top 20 by relevance, median of 15):")
		for _, q := range searches {
			m := sc.query(q)
			if m == "" {
				fmt.Printf("    %-22s not answerable from this index\n", q)
				continue
			}
			var total int
			must(db.QueryRow(`SELECT count(*) FROM fts WHERE fts MATCH ?`, m).Scan(&total))
			d, _ := median(func() int {
				return count(db, `SELECT p.id, p.title, p.excerpt FROM fts JOIN pages p ON p.rowid = fts.rowid WHERE fts MATCH ? ORDER BY rank LIMIT 20`, m)
			}, 15)
			fmt.Printf("    %-22s %9s   %d matching pages\n", q, ms(d), total)
		}

		var folder, tag string
		must(db.QueryRow(`SELECT folder FROM pages GROUP BY folder ORDER BY count(*) DESC LIMIT 1`).Scan(&folder))
		must(db.QueryRow(`SELECT tag FROM tags GROUP BY tag ORDER BY count(*) DESC LIMIT 1`).Scan(&tag))
		fmt.Println("  listings (median of 15):")
		for _, l := range []struct {
			name, q string
			args    []any
		}{
			{"largest folder, first 50", `SELECT id,title,subtitle,excerpt FROM pages WHERE folder = ? ORDER BY updated DESC LIMIT 50`, []any{folder}},
			{"most used tag, first 50", `SELECT p.id,p.title FROM tags t JOIN pages p ON p.rowid = t.page WHERE t.tag = ? ORDER BY p.updated DESC LIMIT 50`, []any{tag}},
			{"recent across space, 50", `SELECT id,title FROM pages ORDER BY updated DESC LIMIT 50`, nil},
			{"folder tree with counts", `SELECT folder, count(*) FROM pages GROUP BY folder`, nil},
			{"all tags with counts", `SELECT tag, count(*) FROM tags GROUP BY tag`, nil},
			{"tag + search together", `SELECT p.id FROM fts JOIN pages p ON p.rowid = fts.rowid JOIN tags t ON t.page = p.rowid WHERE fts MATCH ? AND t.tag = ? ORDER BY rank LIMIT 20`, []any{sc.query("栀子花"), tag}},
		} {
			if len(l.args) > 0 && l.args[0] == "" {
				continue
			}
			d, rows := median(func() int { return count(db, l.q, l.args...) }, 15)
			fmt.Printf("    %-26s %9s   %d rows\n", l.name, ms(d), rows)
		}

		// One page saved: replace its rows. This is what every commit costs.
		r := rand.New(rand.NewSource(2))
		d, _ := median(func() int {
			p := pages[r.Intn(len(pages))]
			var row int64
			must(db.QueryRow(`SELECT rowid FROM pages WHERE id = ?`, p.id).Scan(&row))
			tx, _ := db.Begin()
			_, err := tx.Exec(`DELETE FROM fts WHERE rowid = ?`, row)
			must(err)
			_, err = tx.Exec(`INSERT INTO fts(rowid,title,subtitle,body) VALUES(?,?,?,?)`, row, sc.index(p.title), sc.index(p.subtitle), sc.index(p.body+"新加的一句。"))
			must(err)
			_, err = tx.Exec(`UPDATE pages SET updated = ? WHERE rowid = ?`, time.Now().Unix(), row)
			must(err)
			must(tx.Commit())
			return 1
		}, 200)
		fmt.Printf("  update one page           %s (median of 200)\n\n", ms(d))
		must(db.Close())
	}

	// What a short query costs without an index that can answer it: scan the text.
	t = time.Now()
	hits := 0
	must(filepath.WalkDir(space, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), "栀子") {
			hits++
		}
		return err
	}))
	fmt.Printf("for comparison, scanning every file for a 2-character word: %s (%d files match)\n", time.Since(t).Round(time.Millisecond), hits)
}
