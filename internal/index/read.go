package index

import (
	"context"
	"database/sql"
)

// Lock order. Every path takes what it needs in this order and never the other
// way round, which is what keeps the index free of deadlock:
//
//	read:          a connection of db, then gate (shared), then aux tokens
//	write:         wmu, then the connection of wdb
//	checkpointer:  wmu, then gate (alone), then the connection of wdb
//
// A read waits for a connection while it holds nothing, so all the reads that
// hold the gate have their connection and end. This matters because sync.RWMutex
// stops new shared holders as soon as the checkpointer waits for the gate: a
// read that held the gate while it waited for a connection could wait for ever
// behind reads that hold every connection and wait for the gate. A read never
// waits for an aux connection (it takes a free token or reads serially), never
// takes wmu, and never starts a second read while it holds one (the gate is not
// reentrant once a writer waits). A write never takes the gate or a connection
// of db or aux. Close waits for the checkpointer (ckptDone), which waits only
// for the reads under way and the write under way; nothing in the index calls
// out to code that may hold a lock of its own while it holds one of these,
// except Source in a write, and a Source must not call into the index.

// A listing with conditions and a count by values are several queries (see
// plan.go), some of them run at the same time on connections of their own.
// Each query on a connection of its own would read the index as it was when
// that query began, and a write committed in between would make them
// disagree: a page counted under one state and listed under another. So every
// query of one request is read from one snapshot:
//
//   - The request opens a read transaction on one connection (read) and pins
//     its snapshot with a first read; every query of the request runs in it.
//   - To read several ranges at the same time, it opens read transactions on
//     connections of the aux pool and pins them the same way. They hold the
//     same snapshot when no commit can have happened from before the first
//     pin to after the last: gen held the same even value all along (it is
//     odd while a write commits). When it did not, or no aux connection is
//     free, the ranges are read one after the other on the request's own
//     connection, which costs time and never correctness.
//
// SQLite can open a transaction on a given snapshot (sqlite3_snapshot_open),
// but the driver does not offer it; the sequence lock decides the same thing
// from outside, at the cost of reading serially when a commit falls between
// two pins, which takes microseconds.

// auxConns is the number of connections the reads of all requests share for
// reading ranges at the same time as their own.
const auxConns = 6

// querier is what a read's queries run on: a read transaction.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// read is the index as one snapshot shows it, for the queries of one request.
// It is used by one goroutine at a time; the reads that parallel hands out
// share the snapshot and are each used by one goroutine.
type read struct {
	*Index
	// db shadows Index.db: the queries of a read go to its transaction.
	db querier
	tx *sql.Tx
	// at is the generation the snapshot holds, when known: no commit was under
	// way while it was taken. Without it, nothing is read beside db.
	at    uint64
	known bool
}

// pin starts the snapshot of a read transaction: SQLite takes it at the first
// read of the database, not at BEGIN.
func pin(tx *sql.Tx) error {
	var n int
	return tx.QueryRow(`SELECT count(*) FROM meta`).Scan(&n)
}

// begin opens a read of the index as it is now. end must be called.
//
// The read holds the gate from before its snapshot is pinned to its end, so
// the checkpointer knows when no snapshot is held (index.go). It takes its
// connection first: waiting for one, it holds nothing back.
func (x *Index) begin() (*read, error) {
	tx, err := x.db.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	x.gate.RLock()
	before := x.gen.Load()
	if err := pin(tx); err != nil {
		tx.Rollback()
		x.gate.RUnlock()
		return nil, err
	}
	after := x.gen.Load()
	return &read{Index: x, db: tx, tx: tx, at: before, known: before == after && before%2 == 0}, nil
}

// end closes the read.
func (r *read) end() {
	r.tx.Rollback()
	r.gate.RUnlock()
}

// connQuerier runs the queries of a read that is one query on a connection
// of its own, outside a transaction: the statement holds its snapshot.
type connQuerier struct{ c *sql.Conn }

func (q connQuerier) Query(query string, args ...any) (*sql.Rows, error) {
	return q.c.QueryContext(context.Background(), query, args...)
}

func (q connQuerier) QueryRow(query string, args ...any) *sql.Row {
	return q.c.QueryRowContext(context.Background(), query, args...)
}

// one runs f, a read that is one query, on a connection of db under the gate,
// in the lock order above: the connection first. It costs no transaction and
// no pin, which a read of one statement does not need; f must close its rows
// before it returns.
func (x *Index) one(f func(q querier) error) error {
	c, err := x.db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer c.Close()
	x.gate.RLock()
	defer x.gate.RUnlock()
	return f(connQuerier{c})
}

// parallel is up to n reads that hold the snapshot of r, r itself first, for
// queries to run at the same time; done must be called when they are no longer
// used. It may be r alone.
func (r *read) parallel(n int) (reads []*read, done func()) {
	reads = []*read{r}
	var txs []*sql.Tx
	done = func() {
		for _, tx := range txs {
			tx.Rollback()
			r.auxFree <- struct{}{}
		}
		txs = nil
	}
	if n <= 1 {
		return reads, done
	}
	if !r.known {
		r.serial.Add(1)
		return reads, done
	}
	for len(reads) < n {
		select {
		case <-r.auxFree:
		default:
			n = 0 // none free: go on with those there are
			continue
		}
		tx, err := r.aux.BeginTx(context.Background(), nil)
		if err != nil {
			r.auxFree <- struct{}{}
			break
		}
		txs = append(txs, tx)
		if pin(tx) != nil {
			break
		}
		reads = append(reads, &read{Index: r.Index, db: tx, tx: tx, at: r.at, known: true})
	}
	// Every snapshot was taken while gen held r.at: they are all the one of r.
	if r.Index.gen.Load() != r.at || len(reads) != len(txs)+1 || len(reads) == 1 {
		done()
		r.serial.Add(1)
		return []*read{r}, func() {}
	}
	r.shared.Add(1)
	return reads, done
}
