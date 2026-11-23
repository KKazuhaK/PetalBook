package index

import (
	"context"
	"database/sql"
)

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

// view runs f on a read of the index as it is now: the reads that are one
// query go through the gate like the others.
func (x *Index) view(f func(r *read) error) error {
	r, err := x.begin()
	if err != nil {
		return err
	}
	defer r.end()
	return f(r)
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
