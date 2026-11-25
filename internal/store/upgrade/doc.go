// Package upgrade is where the code that upgrades a state database from one
// release of PetalBook to the next will live. It is empty on purpose.
//
// The rule (docs/design/database.md, "5.3 Database baseline (2026-11-24)"):
//
//   - Until the first release there is no upgrade code at all. The schema is
//     one baseline, internal/store/schema.sql, edited in place; a database made
//     from another baseline is refused at start and is made again.
//   - From the first release, each step from one release to the next is one
//     file here, kept apart from the baseline: the baseline always describes a
//     new database of the current release, and the steps only bring an older
//     database to it.
//   - Only upgrades from the previous major version are supported. When a new
//     major version starts, its baseline already contains everything, and the
//     steps of the major before can simply be deleted.
package upgrade
