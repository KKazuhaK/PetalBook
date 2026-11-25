package space

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/page"
)

// A journal record says what the server is about to do to a file (or did),
// durably, so a crash between a write and its commit cannot leave the change
// outside Git for good.
//
//	w  Path was (or is being) written with content whose blob id is Rev
//	d  Path is being deleted
//	m  From is being moved to To (hard link, then remove the old name)
//	t  Path changed; no further check
//	r  From is being renamed to To, another spelling of its name, through Path
//	b  the folder From is being moved to To: the bindings in space.yml of it
//	   and of the folders in it have to follow (done at recovery if the folder
//	   is at To, not at From, and the old keys are still there)
//	u  the folder Path is being deleted: its bindings have to go with it
//	bd, ud  the bindings of the "b" or "u" record with the same fields have
//	   been dealt with: that record is not made again
//	f  the field From of the type whose file is Path is being renamed To: the
//	   type's file is written first (with the content whose blob id is Rev),
//	   then the Pages, then the views in space.yml. At recovery, a type file
//	   with that content has the rest finished; another has nothing done
//	fd the rename of the "f" record with the same fields is finished
//	fp some of the pages of the "f" record with the same fields that follows
//	   it: a rename can rewrite more pages than fit on one line, so its page
//	   list is written as parts before the record (in the same append, so a
//	   readable "f" record has every part before it), and the record says how
//	   many pages there are in all
//	x  transaction Tx begins (Tx is the number of this frame): the "p"
//	   records that follow it in the same append are its files
//	p  file Path of transaction Tx goes from the content whose blob id is Old
//	   ("" for none) to the content whose blob id is Rev. The new content is in
//	   the record (D, or DB when it is not text) up to txInline bytes, else at
//	   BlobAt in the file tx-<Tx>.blob beside the journal, BlobLen bytes. At
//	   recovery a file at Rev is left as it is, a file at Old gets the new
//	   content, and a file at anything else was changed outside PetalBook and
//	   is left alone: nothing is planned again
//	a  transaction Tx is applied (not synced: the replay does not need it)
//	k  transaction Tx was given up before it was applied: it is not replayed
//	ld the links of the "l" record with the same fields are dealt with, or
//	   will be by the transaction it was journaled with
//
// A "w" record only counts at recovery if the file really has that content,
// so a name the server tried to take but another file held is never committed.
type jrec struct {
	Op   string `json:"op"`
	Path string `json:"p,omitempty"`
	From string `json:"f,omitempty"`
	To   string `json:"to,omitempty"`
	Rev  string `json:"rev,omitempty"`
	// Dir marks an "r" record of a folder: its files are journaled one by
	// one, and the folder's own names are not paths to commit.
	Dir bool `json:"dir,omitempty"`
	// AuthorName and AuthorEmail say who made the change, so a commit after a
	// crash is still theirs. A record without them gets the space's default
	// author.
	AuthorName  string `json:"an,omitempty"`
	AuthorEmail string `json:"ae,omitempty"`
	// Via is the means of the change (Author.Via).
	Via string `json:"av,omitempty"`
	// Pages are the pages an "f" record rewrites (in memory: on disk they are
	// in its "fp" parts), or the part of them an "fp" record carries.
	Pages []string `json:"ps,omitempty"`
	// N is how many pages an "f" record rewrites, all its parts together.
	N int `json:"pn,omitempty"`

	// Tx, Old and the content are those of the "x", "p", "a" and "k"
	// records (see above).
	Tx      uint64 `json:"tx,omitempty"`
	Old     string `json:"old,omitempty"`
	Text    string `json:"d,omitempty"`
	Bin     []byte `json:"db,omitempty"`
	BlobAt  int64  `json:"bo,omitempty"`
	BlobLen int64  `json:"bn,omitempty"`
	// blob is the file a "p" record's content is staged in (set when the
	// journal is read), "" for content in the record.
	blob string

	// seq is the sequence number of the record's frame in the journal, and
	// first that of its first frame (an "f" record's first "fp" part; else
	// seq). Zero for a record that was never journaled. Not part of the body.
	seq, first uint64
}

// journalPart bounds the bytes of the page paths (as JSON) in one "fp" record,
// so that no frame this server writes is longer than maxJournalLine (a path is
// at most a few kilobytes). Frames are read whatever their length.
const (
	journalPart    = 64 << 10
	maxJournalLine = journalPart + 64<<10
)

// The journal is a sequence of frames, one record each, one per line:
//
//	LLLLLLLL CCCCCCCC SSSSSSSSSSSSSSSS {json}\n
//
// L is the length of the JSON body, C the CRC-32C of the sequence number (8
// bytes, big-endian) followed by the body, S the sequence number; all three in
// fixed-width lower-case hex. The sequence numbers rise by one with every frame
// appended and never go down while the server runs: they name a position in
// the journal that a trim can keep everything after (committer.commit). A
// frame that is cut short, whose sum or end does not match, or whose number
// does not rise ends what is read: the rest is the tail of an append that never
// finished (or damage), and is cut off at startup.
const frameHead = 8 + 1 + 8 + 1 + 16 + 1

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

func frameSum(seq uint64, body []byte) uint32 {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], seq)
	return crc32.Update(crc32.Checksum(b[:], castagnoli), castagnoli, body)
}

func appendHex(b []byte, v uint64, width int) []byte {
	const digits = "0123456789abcdef"
	for i := width - 1; i >= 0; i-- {
		b = append(b, digits[(v>>(4*uint(i)))&0xf])
	}
	return b
}

// putFrame writes body as the frame numbered seq.
func putFrame(buf *bytes.Buffer, seq uint64, body []byte) {
	var h [frameHead]byte
	b := appendHex(h[:0], uint64(len(body)), 8)
	b = append(b, ' ')
	b = appendHex(b, uint64(frameSum(seq, body)), 8)
	b = append(b, ' ')
	b = appendHex(b, seq, 16)
	b = append(b, ' ')
	buf.Write(b)
	buf.Write(body)
	buf.WriteByte('\n')
}

// frame is one frame read back: its number, its body and where it is.
type frame struct {
	seq        uint64
	body       []byte
	start, end int
}

func parseHex(b []byte) (uint64, bool) {
	var v uint64
	for _, c := range b {
		switch {
		case '0' <= c && c <= '9':
			v = v<<4 | uint64(c-'0')
		case 'a' <= c && c <= 'f':
			v = v<<4 | uint64(c-'a'+10)
		default:
			return 0, false
		}
	}
	return v, true
}

// scanFrames reads the frames of data, in order, up to the first that is not
// whole and sound. end is where the good frames end; cut says why the bytes
// after it were not read: "" when there are none, "torn" for a frame cut short
// (an append a stop interrupted), "corrupt" for one that is all there but does
// not check out.
func scanFrames(data []byte) (frames []frame, end int, cut string) {
	var last uint64
	// A file system may leave the blocks of an append that a stop
	// interrupted as zeros: that is an append cut short, not damage.
	// A frame cut short is the last thing in the file: its body has no
	// line end (JSON has none), so whole lines after the cut are damage.
	corrupt := func(rest []byte) string {
		if len(bytes.Trim(rest, "\x00")) == 0 {
			return "torn"
		}
		return "corrupt"
	}
	short := func(rest []byte) string {
		if bytes.IndexByte(rest, '\n') >= 0 {
			return "corrupt"
		}
		return "torn"
	}
	for end < len(data) {
		rest := data[end:]
		if len(rest) < frameHead {
			return frames, end, short(rest)
		}
		n, ok1 := parseHex(rest[:8])
		sum, ok2 := parseHex(rest[9:17])
		seq, ok3 := parseHex(rest[18:34])
		if !ok1 || !ok2 || !ok3 || rest[8] != ' ' || rest[17] != ' ' || rest[34] != ' ' {
			return frames, end, corrupt(rest)
		}
		if uint64(len(rest)-frameHead) < n+1 {
			return frames, end, short(rest)
		}
		body := rest[frameHead : frameHead+int(n)]
		if rest[frameHead+int(n)] != '\n' || uint32(sum) != frameSum(seq, body) || seq <= last {
			return frames, end, "corrupt"
		}
		last = seq
		frames = append(frames, frame{seq: seq, body: body, start: end, end: end + frameHead + int(n) + 1})
		end += frameHead + int(n) + 1
	}
	return frames, end, ""
}

// encodeRec writes r as frames of the journal, numbered from *seq + 1 on: one,
// or for an "f" record with pages, its "fp" parts and then the record. It
// leaves *seq at the last number used, and r's first and seq at its frames.
func encodeRec(buf *bytes.Buffer, seq *uint64, r *jrec) {
	put := func(x jrec) {
		// Not escaped for HTML: page text is full of < and &, and the
		// content of a "p" record would grow sixfold for each.
		var enc bytes.Buffer
		e := json.NewEncoder(&enc)
		e.SetEscapeHTML(false)
		e.Encode(x)
		b := bytes.TrimSuffix(enc.Bytes(), []byte("\n"))
		*seq++
		if r.first == 0 || r.first > *seq {
			r.first = *seq
		}
		putFrame(buf, *seq, b)
	}
	r.first = 0
	if r.Op != "f" || len(r.Pages) == 0 {
		put(*r)
		r.seq = *seq
		return
	}
	part := jrec{Op: "fp", Path: r.Path, From: r.From, To: r.To, Rev: r.Rev}
	size := 0
	for _, p := range r.Pages {
		q, _ := json.Marshal(p)
		if size > 0 && size+len(q)+1 > journalPart {
			put(part)
			part.Pages, size = nil, 0
		}
		part.Pages = append(part.Pages, p)
		size += len(q) + 1
	}
	put(part)
	whole := *r
	whole.N, whole.Pages = len(r.Pages), nil
	put(whole)
	r.seq = *seq
}

// journalDamage is a frame of the journal whose body is not a record.
type journalDamage struct {
	Seq uint64
	Len int
	Why string
}

// journalRead is what readJournal found.
type journalRead struct {
	recs []jrec
	// damage are sound frames whose body is not a record: passed over, the
	// records after them still count.
	damage []journalDamage
	// end is where the good frames end, and cut why the rest was not read
	// (see scanFrames).
	end int
	cut string
	// seq is the number of the last good frame, 0 if there is none.
	seq uint64
}

// readJournal reads the records of a journal.
func readJournal(data []byte) journalRead {
	frames, end, cut := scanFrames(data)
	out := journalRead{end: end, cut: cut}
	type key struct{ path, from, to, rev string }
	type parts struct {
		first uint64
		pages []string
	}
	pending := map[key]*parts{}
	for _, f := range frames {
		out.seq = f.seq
		var r jrec
		switch err := json.Unmarshal(f.body, &r); {
		case err != nil:
			out.damage = append(out.damage, journalDamage{f.seq, len(f.body), err.Error()})
			continue
		case r.Op == "":
			out.damage = append(out.damage, journalDamage{f.seq, len(f.body), "no op"})
			continue
		}
		r.seq, r.first = f.seq, f.seq
		k := key{r.Path, r.From, r.To, r.Rev}
		switch r.Op {
		case "fp":
			p := pending[k]
			if p == nil {
				p = &parts{first: f.seq}
				pending[k] = p
			}
			p.pages = append(p.pages, r.Pages...)
			continue
		case "f":
			if r.N > 0 {
				if p := pending[k]; p != nil {
					r.Pages, r.first = append(p.pages, r.Pages...), p.first
				}
				if len(r.Pages) != r.N {
					out.damage = append(out.damage, journalDamage{f.seq, len(f.body), fmt.Sprintf("the rename names %d pages, %d were found", r.N, len(r.Pages))})
				}
			}
			r.N = 0
			delete(pending, k)
		}
		out.recs = append(out.recs, r)
	}
	out.recs = wholeTransactions(out.recs)
	return out
}

// wholeTransactions drops the records of a transaction that is not all
// there: its append was cut short, so none of its files was written yet (they
// are written once it is durable), and the closing of its "l" records is lost
// with it, so their links are planned again from untouched files.
func wholeTransactions(recs []jrec) []jrec {
	want, got := map[uint64]int{}, map[uint64]int{}
	for _, r := range recs {
		switch {
		case r.Op == "x":
			want[r.Tx] = r.N
		case r.Tx != 0 && r.Op != "a" && r.Op != "k":
			got[r.Tx]++
		}
	}
	whole := true
	for tx, n := range want {
		whole = whole && got[tx] == n
	}
	if whole {
		return recs
	}
	out := recs[:0:0]
	for _, r := range recs {
		if r.Tx == 0 || want[r.Tx] == got[r.Tx] {
			out = append(out, r)
		}
	}
	return out
}

func (r jrec) author() Author { return Author{Name: r.AuthorName, Email: r.AuthorEmail, Via: r.Via} }

// journal is an append-only file in the state directory. Every append is
// fsynced before the write it announces. After a successful commit it is
// trimmed to what is still pending (trim).
type journal struct {
	path string
	mu   sync.Mutex
	f    *os.File
	// size is the length of the good frames in the file, and seq the number
	// of the last one (0 when there is none). An append that fails is cut
	// back to size; broken says that cut failed too, and the next append
	// tries it again first.
	size   int64
	seq    uint64
	broken bool
	// blobs are the staged content files of the transactions in the
	// journal, by transaction.
	blobs map[uint64]string
	// at is where each frame of the file is, by number: start and end.
	at map[uint64][2]int64
	// guard, when set, says whether the folder of the journal can be written
	// in: it is not, through something that is not a folder (settingspath.go).
	guard func() error
	// write and sync, when set, stand in for the file's Write and Sync: a
	// test makes an append fail part way with them.
	write func(f *os.File, b []byte) (int, error)
	sync  func(f *os.File) error
	// replace, when set, stands in for the rename that puts a trimmed
	// journal in place: a test makes a trim fail with it.
	replace func(tmp, path string) error
}

// openJournal reads the records an earlier run left and opens the file for
// appending. A frame that is cut short or does not check out ends the
// journal: it and anything after it are logged and cut off, so that the next
// record appended is read back. Nothing is refused: a journal that cannot be
// read at all is an empty one.
func openJournal(path string, log *slog.Logger) (*journal, []jrec, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}
	rd := readJournal(data)
	for _, d := range rd.damage {
		log.Error("a record of the journal cannot be read; what it recorded may not be committed or finished",
			"journal", path, "seq", d.Seq, "bytes", d.Len, "problem", d.Why)
	}
	switch rd.cut {
	case "torn":
		log.Warn("the journal ends in a record cut short by a stop; it is dropped", "journal", path, "bytes", len(data)-rd.end)
	case "corrupt":
		log.Error("the journal is damaged from a point on; the rest is dropped, and what it recorded may not be committed or finished",
			"journal", path, "at", rd.end, "bytes", len(data)-rd.end)
	}
	if rd.cut != "" {
		if err := os.Truncate(path, int64(rd.end)); err != nil {
			return nil, nil, err
		}
	}
	j := &journal{path: path, size: int64(rd.end), seq: rd.seq, blobs: map[uint64]string{}, at: map[uint64][2]int64{}}
	j.index(data[:rd.end], 0)
	for i, r := range rd.recs {
		if r.Op == "p" && r.BlobLen > 0 {
			rd.recs[i].blob = j.blobPath(r.Tx)
			j.blobs[r.Tx] = rd.recs[i].blob
		}
	}
	// Staged content no record names: a crash after a trim, before the file
	// went, or during the append that was to name it.
	stale, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "tx-*.blob"))
	for _, f := range stale {
		named := false
		for _, b := range j.blobs {
			named = named || b == f
		}
		if !named {
			os.Remove(f)
		}
	}
	if j.f, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644); err != nil {
		return nil, nil, err
	}
	return j, rd.recs, nil
}

func (j *journal) blobPath(tx uint64) string {
	return filepath.Join(filepath.Dir(j.path), fmt.Sprintf("tx-%d.blob", tx))
}

// txInline is the most content a "p" record carries itself; more is staged in
// a file of the transaction's.
const txInline = 64 << 10

// txInlineTotal bounds the content one transaction carries in the journal.
const txInlineTotal = 1 << 20

// txFile is a file of a transaction: its path, the blob ids of its content
// before and after (Old "" for a file that is not there), and the new content.
//
// from, for a page that moves, is its old name (path is the new one, already
// taken as a second name of the file); with dir, the entry is a folder renamed
// from from to path, whose files are journaled one by one.
type txFile struct {
	path, old, rev string
	data           []byte
	from           string
	dir            bool
}

// appendTx journals a transaction, durably: an "x" record by by, a "p" record
// for each file, and the records in extra, in one append. It returns the
// transaction's number. Content over txInline bytes is written to the
// transaction's staged file first (synced, with its folder), so an append
// with such content costs two syncs more.
func (j *journal) appendTx(by Author, files []txFile, extra []jrec) (uint64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	tx := j.seq + 1 // the number of the "x" frame
	recs := make([]jrec, 0, len(files)+1+len(extra))
	// N counts the frames of the transaction after its "x": a transaction
	// that is not all there (an append cut short) is not replayed at all.
	recs = append(recs, jrec{Op: "x", Tx: tx, N: len(files) + len(extra), AuthorName: by.Name, AuthorEmail: by.Email, Via: by.Via})
	var staged bytes.Buffer
	inline := 0
	for _, f := range files {
		r := jrec{Op: "p", Tx: tx, Path: f.path, Old: f.old, Rev: f.rev, From: f.from, Dir: f.dir}
		switch {
		case f.dir:
		case len(f.data) > txInline || inline+len(f.data) > txInlineTotal:
			r.BlobAt, r.BlobLen = int64(staged.Len()), int64(len(f.data))
			staged.Write(f.data)
		case utf8.Valid(f.data):
			r.Text = string(f.data)
			inline += len(f.data)
		default:
			r.Bin = f.data
			inline += len(f.data)
		}
		recs = append(recs, r)
	}
	for _, r := range extra {
		r.Tx = tx
		recs = append(recs, r)
	}
	blob := ""
	if staged.Len() > 0 {
		blob = j.blobPath(tx)
		if err := writeSynced(blob, staged.Bytes()); err != nil {
			os.Remove(blob)
			return 0, err
		}
	}
	if err := j.appendLocked(recs, true); err != nil {
		if blob != "" {
			os.Remove(blob)
		}
		return 0, err
	}
	if blob != "" {
		j.blobs[tx] = blob
	}
	return tx, nil
}

// writeSynced writes data to the new file name, and syncs it and its folder.
func writeSynced(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// contentAt is the new content of the "p" record in the frame numbered seq,
// read back from the journal: for a commit of a file that was changed
// outside PetalBook after the server wrote it.
//
// Only that frame is read: the journal knows where each frame is (at).
func (j *journal) contentAt(seq uint64) ([]byte, error) {
	j.mu.Lock()
	loc, ok := j.at[seq]
	var body []byte
	var err error
	if ok {
		body = make([]byte, loc[1]-loc[0])
		var f *os.File
		if f, err = os.Open(j.path); err == nil {
			_, err = f.ReadAt(body, loc[0])
			f.Close()
		}
	}
	j.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("frame %d is not in the journal any more", seq)
	}
	if err != nil {
		return nil, err
	}
	frames, _, _ := scanFrames(body)
	if len(frames) != 1 || frames[0].seq != seq {
		return nil, fmt.Errorf("frame %d of the journal cannot be read", seq)
	}
	var r jrec
	if err := json.Unmarshal(frames[0].body, &r); err != nil || r.Op != "p" {
		return nil, fmt.Errorf("frame %d of the journal is not a file of a change", seq)
	}
	if r.BlobLen > 0 {
		r.blob = j.blobPath(r.Tx)
	}
	return r.content()
}

// index notes where the frames of data, which starts at offset base of the
// journal, are (at).
func (j *journal) index(data []byte, base int64) {
	frames, _, _ := scanFrames(data)
	for _, f := range frames {
		j.at[f.seq] = [2]int64{base + int64(f.start), base + int64(f.end)}
	}
}

// content is the new content of a "p" record.
func (r jrec) content() ([]byte, error) {
	if r.BlobLen == 0 {
		if r.Bin != nil {
			return r.Bin, nil
		}
		return []byte(r.Text), nil
	}
	if r.blob == "" {
		return nil, errors.New("the staged content of the record is not known")
	}
	f, err := os.Open(r.blob)
	if err != nil {
		return nil, fmt.Errorf("the staged content of %s cannot be read: %w", r.Path, err)
	}
	defer f.Close()
	b := make([]byte, r.BlobLen)
	if _, err := f.ReadAt(b, r.BlobAt); err != nil {
		return nil, fmt.Errorf("the staged content of %s cannot be read: %w", r.Path, err)
	}
	return b, nil
}

// lastSeq is the number of the last frame appended.
func (j *journal) lastSeq() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.seq
}

// append journals recs, durably, and sets their seq and first. If it fails,
// what it wrote is cut off again, so that the next append starts a frame of
// its own right after the last good one.
func (j *journal) append(recs []jrec) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.appendLocked(recs, true)
}

// appendNoSync is append without the sync: for a record that a crash may
// lose ("a"). The next synced append, or the system, makes it durable.
func (j *journal) appendNoSync(recs []jrec) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.appendLocked(recs, false)
}

func (j *journal) appendLocked(recs []jrec, synced bool) error {
	if j.broken {
		if err := j.f.Truncate(j.size); err != nil {
			return fmt.Errorf("the journal cannot be cut back after a failed append: %w", err)
		}
		j.broken = false
	}
	var buf bytes.Buffer
	seq := j.seq
	for i := range recs {
		encodeRec(&buf, &seq, &recs[i])
	}
	write, sync := (*os.File).Write, (*os.File).Sync
	if j.write != nil {
		write = j.write
	}
	if j.sync != nil {
		sync = j.sync
	}
	_, err := write(j.f, buf.Bytes())
	if err == nil && synced {
		err = sync(j.f)
	}
	if err != nil {
		for i := range recs {
			recs[i].seq, recs[i].first = 0, 0
		}
		if terr := j.f.Truncate(j.size); terr != nil {
			j.broken = true
		}
		return err
	}
	j.index(buf.Bytes(), j.size)
	j.size += int64(buf.Len())
	j.seq = seq
	return nil
}

// trim replaces the journal, atomically, by the frames after number upTo and
// those of the records in keep (records still open: they stay whatever their
// place), in their order, followed by a "t" record for each path in pending,
// with its author. A record of keep whose frames are not in the journal any
// more is journaled again, at the end.
// retainedTx is a file a transaction wrote whose commit failed: a trim
// journals it again as a transaction of its own, applied, with its content.
type retainedTx struct {
	by   Author
	file txFile
}

func (j *journal) trim(upTo uint64, keep []jrec, pending map[string]Author, retx ...retainedTx) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.guard != nil {
		if err := j.guard(); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(j.path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if int64(len(data)) > j.size {
		data = data[:j.size]
	}
	frames, _, _ := scanFrames(data)
	kept := func(seq uint64) bool {
		for _, r := range keep {
			if r.first <= seq && seq <= r.seq {
				return true
			}
		}
		return false
	}
	var buf bytes.Buffer
	found := map[uint64]bool{}
	for _, f := range frames {
		if f.seq > upTo || kept(f.seq) {
			buf.Write(data[f.start:f.end])
			found[f.seq] = true
		}
	}
	// A record journaled again gets its new numbers only once the new
	// journal is in place: after a failed trim the next append uses them.
	seq := j.seq
	again := map[int]jrec{}
	for i, r := range keep {
		if r.seq == 0 || !found[r.seq] {
			encodeRec(&buf, &seq, &r)
			again[i] = r
		}
	}
	for _, rt := range retx {
		tx := seq + 1
		f := rt.file
		recs := []jrec{
			{Op: "x", Tx: tx, N: 1, AuthorName: rt.by.Name, AuthorEmail: rt.by.Email, Via: rt.by.Via},
			{Op: "p", Tx: tx, Path: f.path, Old: f.old, Rev: f.rev},
			{Op: "a", Tx: tx},
		}
		// Inline whatever its size: it is rare, and the staged file of its
		// first transaction goes with this trim.
		if utf8.Valid(f.data) {
			recs[1].Text = string(f.data)
		} else {
			recs[1].Bin = f.data
		}
		for i := range recs {
			encodeRec(&buf, &seq, &recs[i])
		}
	}
	paths := make([]string, 0, len(pending))
	for p := range pending {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		by := pending[p]
		r := jrec{Op: "t", Path: p, AuthorName: by.Name, AuthorEmail: by.Email, Via: by.Via}
		encodeRec(&buf, &seq, &r)
	}
	tmp := j.path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	replace := os.Rename
	if j.replace != nil {
		replace = j.replace
	}
	if err := replace(tmp, j.path); err != nil {
		os.Remove(tmp)
		return err
	}
	for i, r := range again {
		keep[i].seq, keep[i].first = r.seq, r.first
	}
	// A transaction's frames are one append, never open: at or before upTo
	// they are all gone, and so is what was staged for them.
	for tx, b := range j.blobs {
		if tx <= upTo {
			os.Remove(b)
			delete(j.blobs, tx)
		}
	}
	j.f.Close()
	j.size, j.seq, j.broken = int64(buf.Len()), seq, false
	j.at = map[uint64][2]int64{}
	j.index(buf.Bytes(), 0)
	j.f, err = os.OpenFile(j.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	return err
}

func (j *journal) close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.f.Close()
}

// recoverPending turns the records of an earlier run into the paths the next
// commit must cover, each with the author of the last record that names it
// (as in the committer: the latest change to a path is the one that counts),
// and finishes a move the crash interrupted. Paths that no longer exist are
// fine: they become deletions. A record naming a path outside the space (the
// journal is a file on disk) is ignored.
func recoverPending(root string, recs []jrec) map[string]Author {
	return recoverPendingWith(nil, root, recs)
}

// recoverPendingWith is recoverPending; h restores a space.yml that cannot be
// used when the bindings of a folder are to be finished in it.
func recoverPendingWith(h *healer, root string, recs []jrec) map[string]Author {
	set, _, _ := recoverJournal(h, root, recs)
	return set
}

// recoverJournal is recoverPendingWith, and also gives the content that the
// paths a transaction wrote are to be committed with: what the server wrote,
// even where the file was changed outside PetalBook since (that change stays
// in the work tree, uncommitted). A later record of the path that is not a
// transaction's leaves it to the work tree again. giveUp are the transactions
// that never got to be applied and that the replay could not finish (a file
// changed outside meanwhile): their content never was on disk, so it is not
// committed, and the caller journals them as given up ("k").
func recoverJournal(h *healer, root string, recs []jrec) (set map[string]Author, revs map[string]gitstore.Blob, giveUp []uint64) {
	set = map[string]Author{}
	revs = map[string]gitstore.Blob{}
	gaveUp := map[uint64]bool{}
	defer func() {
		for p := range revs {
			if _, ok := set[p]; !ok {
				delete(revs, p)
			}
		}
	}()
	ok := func(p string) bool {
		return p != "" && fs.ValidPath(p) && !strings.HasPrefix(p, ".git/") && p != ".git"
	}
	var bindRecs, fieldRecs []jrec
	given := map[uint64]bool{}   // transactions given up ("k")
	applied := map[uint64]bool{} // and written ("a")
	authors := map[uint64]Author{}
	type pair struct{ from, to string }
	moved := map[pair]bool{}  // moves a transaction makes
	closed := map[pair]bool{} // folder renames that did not happen ("mdd")
	for _, r := range recs {
		switch r.Op {
		case "k":
			given[r.Tx] = true
		case "a":
			applied[r.Tx] = true
		case "x":
			authors[r.Tx] = r.author()
		case "mdd":
			closed[pair{r.From, r.To}] = true
		}
	}
	for _, r := range recs {
		if r.Op == "p" && r.From != "" && !given[r.Tx] {
			moved[pair{r.From, r.Path}] = true
		}
	}
	var log *slog.Logger
	if h != nil {
		log = h.log
	}
	for _, r := range recs {
		by := r.author()
		switch r.Op {
		case "x", "a", "k", "p", "mr", "md", "mdd":
		default:
			// Any other record of a path leaves it to the work tree.
			delete(revs, r.Path)
			delete(revs, r.From)
			delete(revs, r.To)
		}
		switch r.Op {
		case "mr":
			// A name taken for a move as a second name of the page: without
			// a transaction that makes the move, nobody was told it was
			// made, and the name goes again.
			if ok(r.From) && ok(r.To) && !moved[pair{r.From, r.To}] {
				takeBackName(root, r.From, r.To)
			}
			continue
		case "md":
			// A folder renamed for a move: the same, the folder goes back.
			if ok(r.From) && ok(r.To) && !moved[pair{r.From, r.To}] && !closed[pair{r.From, r.To}] {
				takeBackFolder(root, r.From, r.To)
			}
			continue
		case "p":
			if !ok(r.Path) || given[r.Tx] || (r.From != "" && !ok(r.From)) {
				continue
			}
			if r.Dir {
				// The folder of a move: its files are journaled one by one.
				finishFolderMove(root, r.From, r.Path)
				continue
			}
			// The file is the server's if the replay leaves it at the new
			// content, or, changed outside since, if the transaction was
			// applied. One that never was applied and whose file is at
			// neither content is given up: what it would have written never
			// was on disk (and a new file there may be another's that held
			// the name).
			switch {
			case replay(root, r, !applied[r.Tx], log) || applied[r.Tx]:
				r := r
				set[r.Path] = authors[r.Tx]
				revs[r.Path] = gitstore.Blob{ID: r.Rev, Data: r.content}
				if r.From != "" {
					// The old name of a moved page is committed as gone.
					set[r.From] = authors[r.Tx]
					revs[r.From] = gitstore.Blob{}
				}
			case !gaveUp[r.Tx]:
				gaveUp[r.Tx] = true
				giveUp = append(giveUp, r.Tx)
			}
			continue
		case "t", "d":
			if ok(r.Path) {
				set[r.Path] = by
			}
		case "w":
			if !ok(r.Path) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(r.Path)))
			if err == nil && page.Rev(data) == r.Rev {
				set[r.Path] = by
			}
		case "m":
			if !ok(r.From) || !ok(r.To) {
				continue
			}
			finishMove(root, r.From, r.To)
			set[r.From], set[r.To] = by, by
		case "bd", "ud":
			for i, o := range bindRecs {
				if o.Op+"d" == r.Op && o.From == r.From && o.To == r.To && o.Path == r.Path {
					bindRecs = append(bindRecs[:i:i], bindRecs[i+1:]...)
					break
				}
			}
		case "f":
			if ok(r.Path) {
				fieldRecs = append(fieldRecs, r)
			}
		case "fd":
			for i, o := range fieldRecs {
				if o.From == r.From && o.To == r.To && o.Path == r.Path && o.Rev == r.Rev {
					fieldRecs = append(fieldRecs[:i:i], fieldRecs[i+1:]...)
					break
				}
			}
		case "b", "u":
			// Looked at after the files are in their final places.
			if (r.Op == "b" && (!ok(r.From) || !ok(r.To))) || (r.Op == "u" && !ok(r.Path)) {
				continue
			}
			bindRecs = append(bindRecs, r)
		case "r":
			if !ok(r.From) || !ok(r.To) || !ok(r.Path) {
				continue
			}
			finishRespell(root, r.Path, r.To)
			if !r.Dir {
				set[r.From], set[r.To], set[r.Path] = by, by, by
			}
		}
	}
	for _, r := range fieldRecs {
		for _, p := range recoverFieldRename(h, root, r) {
			if ok(p) {
				set[p] = r.author()
				delete(revs, p)
			}
		}
	}
	if len(bindRecs) > 0 {
		by := bindRecs[len(bindRecs)-1].author()
		for _, p := range recoverBindings(h, root, bindRecs) {
			set[p] = by
			delete(revs, p)
		}
	}
	return set, revs, giveUp
}

// replay brings the file of a "p" record to its new content, if it is at the
// old one and write is set, and reports whether the file has the new content
// now. A file at any other content was changed outside PetalBook: it is left
// alone. Nothing is planned again, so replaying twice is replaying once.
//
// A transaction that was applied ("a") is not written again: its files were
// written, and whatever they hold now came after it (a later change, or one
// made outside, maybe back to the old bytes). The "a" goes to the journal
// unsynced, but the next synced append makes it durable, so only the last
// transaction before a crash can lack it although it was applied; its files
// are then at the new content, or untouched, and the replay is right either way.
//
// A record of a moved page (From set) has its new name already taken, as a
// second name of the old file: the new name gets the new content, and then
// the old name goes, if it is still that file or that content.
func replay(root string, r jrec, write bool, log *slog.Logger) bool {
	abs := filepath.Join(root, filepath.FromSlash(r.Path))
	cur := ""
	switch data, err := os.ReadFile(abs); {
	case err == nil:
		cur = page.Rev(data)
	case !errors.Is(err, fs.ErrNotExist):
		return false
	}
	from := ""
	if r.From != "" {
		from = filepath.Join(root, filepath.FromSlash(r.From))
		if cur == "" && revOf(from) == r.Old {
			cur = r.Old // the second name was lost: made again from the content
		}
	}
	switch cur {
	case r.Rev:
		if from != "" && write {
			dropOldName(from, abs, r.Old)
		}
		return true
	case r.Old:
		if !write {
			return false
		}
		data, err := r.content()
		if err == nil && page.Rev(data) != r.Rev {
			err = errors.New("the content does not match the record")
		}
		if err == nil {
			err = writeAtomic(abs, data)
		}
		if err == nil && from != "" {
			dropOldName(from, abs, r.Old)
		}
		if err != nil {
			if log != nil {
				log.Error("a change an earlier run journaled cannot be finished; the file stays as it was",
					"path", r.Path, "transaction", r.Tx, "staged", r.blob, "error", err)
			}
			return false
		}
		return true
	}
	return false
}

// revOf is the blob id of the file at abs, "" when there is none.
func revOf(abs string) string {
	data, err := os.ReadFile(abs)
	if err != nil {
		return ""
	}
	return page.Rev(data)
}

// dropOldName removes from, the old name of a page moved to to, if it is
// still a second name of that file or has the content the page had (old):
// anything else there is someone else's, and stays.
func dropOldName(from, to, old string) {
	if strings.EqualFold(from, to) {
		return // another spelling of the same name: never two files
	}
	fa, errA := os.Lstat(from)
	if errA != nil || !fa.Mode().IsRegular() {
		return
	}
	if fb, err := os.Lstat(to); err == nil && os.SameFile(fa, fb) || revOf(from) == old {
		os.Remove(from)
	}
}

// takeBackName undoes the taking of the name to for the page at from: to goes
// if it is a second name of that very file.
func takeBackName(root, from, to string) {
	if strings.EqualFold(from, to) {
		return
	}
	a, b := filepath.Join(root, filepath.FromSlash(from)), filepath.Join(root, filepath.FromSlash(to))
	fa, errA := os.Lstat(a)
	fb, errB := os.Lstat(b)
	if errA == nil && errB == nil && fa.Mode().IsRegular() && os.SameFile(fa, fb) {
		os.Remove(b)
	}
}

// takeBackFolder renames the folder to back to from, if it is at to and
// nothing is at from.
func takeBackFolder(root, from, to string) {
	a, b := filepath.Join(root, filepath.FromSlash(from)), filepath.Join(root, filepath.FromSlash(to))
	if _, err := os.Lstat(a); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	if fi, err := os.Lstat(b); err == nil && fi.IsDir() {
		os.Rename(b, a)
	}
}

// finishFolderMove renames the folder from to to, if it is still at from and
// nothing is at to: the rename the transaction of the move is about.
func finishFolderMove(root, from, to string) {
	a, b := filepath.Join(root, filepath.FromSlash(from)), filepath.Join(root, filepath.FromSlash(to))
	if _, err := os.Lstat(b); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	if fi, err := os.Lstat(a); err == nil && fi.IsDir() {
		os.MkdirAll(filepath.Dir(b), 0o755)
		os.Rename(a, b)
	}
}

// listed reports whether dir has an entry named exactly name.
func listed(dir, name string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Name() == name {
			return true
		}
	}
	return false
}

// finishRespell completes a rename through a third name that stopped
// between its two steps: what is at mid goes to to, if neither to nor (on a
// file system that folds names) the old spelling is there. A folder that
// stays at mid has its files journaled under mid (Space.rename), so they are
// committed there, not as deleted.
func finishRespell(root, mid, to string) {
	m, t := filepath.Join(root, filepath.FromSlash(mid)), filepath.Join(root, filepath.FromSlash(to))
	if _, err := os.Lstat(m); err != nil {
		return
	}
	if _, err := os.Lstat(t); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	os.Rename(m, t)
}

// finishMove completes a move that stopped between the link and the removal of
// the old name: if both names exist and are the same file (or the same page,
// byte for byte), the old name goes.
func finishMove(root, from, to string) {
	a, b := filepath.Join(root, filepath.FromSlash(from)), filepath.Join(root, filepath.FromSlash(to))
	fa, errA := os.Lstat(a)
	fb, errB := os.Lstat(b)
	if errA != nil || errB != nil || !fa.Mode().IsRegular() || !fb.Mode().IsRegular() {
		return
	}
	if os.SameFile(fa, fb) {
		// Two names of one file: the link was made and the old name is still
		// there. But on a file system that folds names, a rename to another
		// spelling of the file's own name answers to both names with one
		// entry, and removing "the old name" would remove the only one. Only
		// two entries in the listing are two names.
		if a != b && listed(filepath.Dir(a), filepath.Base(a)) && listed(filepath.Dir(b), filepath.Base(b)) {
			os.Remove(a)
		}
		return
	}
	da, errA := os.ReadFile(a)
	db, errB := os.ReadFile(b)
	if errA != nil || errB != nil || !bytes.Equal(da, db) {
		return
	}
	if id := page.Parse(da).Meta().ID(); id != "" && id == page.Parse(db).Meta().ID() {
		os.Remove(a)
	}
}
