package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/idna"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Addresses (3.6). An address belongs to at most one account on the whole
// server: user_emails holds every address of every account, verified or not,
// with a unique index on its key. The primary address is the account's UPN and
// is copied to users.email, users.email_key, users.email_verified and
// users.email_verified_by so that the lists and the search need no join; only
// the functions of this file write that copy (SetPrimaryEmail and the ones it
// shares its transaction with), and the purge clears both.
//
// An account has one address today. The shape already allows several (one
// primary, any number of others, each verified or not): the lookups that sign
// in or reset by address read a verified row of user_emails, never users.email.

// ErrEmailTaken: another account holds the address (verified or not, and also
// a deleted account that was not purged yet).
var ErrEmailTaken = errors.New("the address belongs to another account")

// ErrPrimaryEmail: a primary address cannot be removed on its own; another
// must be made primary first (or the primary set to "").
var ErrPrimaryEmail = errors.New("the primary address cannot be removed")

// EmailKey is the form addresses are compared in, and what user_emails.email_key
// holds:
//
//   - the domain in its ASCII form: lower case, and a domain that is not ASCII
//     (or has an xn-- label) converted by IDNA (UTS #46, the lookup profile) to
//     punycode, so 例子.公司 and xn--fsqu00a.xn--55qx5d are one domain;
//   - the local part with A to Z in lower case when it is ASCII; otherwise
//     NFC, case folded (Unicode full folding), and NFC again.
//
// ok is false for something that is not an address (no @, an empty part,
// invalid UTF-8, a domain IDNA refuses). Whether an address outside ASCII may
// be set at all is the site's setting users.emailNonAscii, decided by the
// caller; the key is the same either way, so turning the setting off and on
// never lets two accounts share an address.
func EmailKey(email string) (key string, ok bool) {
	at := strings.LastIndexByte(email, '@')
	if at < 1 || at == len(email)-1 || !utf8.ValidString(email) {
		return "", false
	}
	local, domain := email[:at], email[at+1:]
	if isASCII(local) {
		local = asciiLower(local)
	} else {
		local = norm.NFC.String(cases.Fold().String(norm.NFC.String(local)))
	}
	d, ok := domainKey(domain)
	if !ok || local == "" {
		return "", false
	}
	return local + "@" + d, true
}

// EmailExactKey is EmailKey without the Unicode case folding of the local part
// (only A to Z are made lower case; the rest is NFC). Two addresses that are
// one in EmailKey may be two mailboxes for the mail server (U+017F, the long
// s, folds to "s"), so proving one never takes the other from its holder
// (takeFromOther compares this form): the two stay one address for
// uniqueness, and the later one is refused.
func EmailExactKey(email string) (string, bool) {
	at := strings.LastIndexByte(email, '@')
	if at < 1 || at == len(email)-1 || !utf8.ValidString(email) {
		return "", false
	}
	d, ok := domainKey(email[at+1:])
	if !ok {
		return "", false
	}
	return asciiLower(norm.NFC.String(email[:at])) + "@" + d, true
}

// domainKey is the domain's part of the key. The root's dot at the end
// (example.com.) is left out, so that it is not a second spelling of the
// domain; a domain with an empty label, as typed or once its punycode is
// decoded (xn--), has no key.
func domainKey(domain string) (string, bool) {
	lower := strings.TrimSuffix(asciiLower(domain), ".")
	if !labelsOK(lower) {
		return "", false
	}
	if isASCII(lower) && !strings.Contains(lower, "xn--") {
		return lower, true
	}
	// A punycode label is decoded first, so that a form that is not canonical
	// is not a second spelling of the domain.
	u, err := idna.Lookup.ToUnicode(lower)
	if err != nil || !labelsOK(u) {
		return "", false
	}
	a, err := idna.Lookup.ToASCII(u)
	if err != nil || !labelsOK(a) {
		return "", false
	}
	return asciiLower(a), true
}

// labelsOK says whether a domain has no empty label (and is not empty).
func labelsOK(domain string) bool {
	if domain == "" {
		return false
	}
	for l := range strings.SplitSeq(domain, ".") {
		if l == "" {
			return false
		}
	}
	return true
}

// EmailIsASCII says whether an address is ASCII only.
func EmailIsASCII(email string) bool { return isASCII(email) }

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// storedEmailKey is what an email_key column holds for an address: its
// EmailKey, or "" for an empty address and for one that has none (which no
// lookup finds).
func storedEmailKey(email string) string {
	key, _ := EmailKey(email)
	return key
}

// InsertUser adds a user, and its address (if any) as its primary one, in one
// transaction. A taken username or id is a unique violation (see UniqueOn); a
// taken address is ErrEmailTaken.
func InsertUser(ctx context.Context, q Q, u User) error {
	if u.TenantID == "" {
		u.TenantID = DefaultTenantID
	}
	return atomically(ctx, q, func(q Q) error {
		// The row starts without its address; SetPrimaryEmail writes it and
		// its copy.
		_, err := q.Exec(ctx, `INSERT INTO users(`+userCols+`, name_key, email_key) VALUES (?, ?, ?, '', ?, ?, ?, 0, ?, ?, ?, ?, ?, '')`,
			u.ID, u.Username, u.Name, u.Role, b2i(u.Disabled), ms(u.Created), b2i(u.PasskeyRequired), u.TenantID, b2i(u.SiteAdmin), b2i(u.RemoteExempt),
			FoldName(u.Name))
		if err != nil {
			return err
		}
		if u.Email != "" {
			if _, err := SetPrimaryEmail(ctx, q, u.ID, u.Email, u.EmailVerified, "", PrimaryOpts{Now: u.Created}); err != nil {
				return err
			}
		}
		return usageOfNewUser(ctx, q, u.ID, u.TenantID, u.Created)
	})
}

// UpdateProfile sets the name, and the primary address and whether it is
// verified (SetPrimaryEmail when either changed: who vouched for it is kept
// while the address stays, and cleared with a new one).
func UpdateProfile(ctx context.Context, q Q, id, name, email string, verified bool) error {
	return atomically(ctx, q, func(q Q) error {
		res, err := q.Exec(ctx, `UPDATE users SET name = ?, name_key = ? WHERE id = ?`, name, FoldName(name), id)
		if err := oneRow(res, err); err != nil {
			return err
		}
		var cur, by string
		var curVerified int64
		if err := q.QueryRow(ctx, `SELECT email, email_verified, email_verified_by FROM users WHERE id = ?`, id).Scan(&cur, &curVerified, &by); err != nil {
			return err
		}
		if cur == email && (curVerified != 0) == verified {
			return nil
		}
		if cur != email {
			by = ""
		}
		_, err = SetPrimaryEmail(ctx, q, id, email, verified, by, PrimaryOpts{})
		return err
	})
}

// VerifiedByTenantAdmin is the email_verified_by of an address only a tenant
// administrator vouched for.
const VerifiedByTenantAdmin = "tenant_admin"

// SetEmail sets a user's primary address, whether it is verified, and who
// vouched for it (SetPrimaryEmail; another account's address is ErrEmailTaken).
func SetEmail(ctx context.Context, q Q, id, email string, verified bool, by string) error {
	_, err := SetPrimaryEmail(ctx, q, id, email, verified, by, PrimaryOpts{})
	return err
}

// EmailVerifiedBy is who vouched for a user's verified primary address.
func EmailVerifiedBy(ctx context.Context, q Q, id string) (string, error) {
	var by string
	err := q.QueryRow(ctx, `SELECT email_verified_by FROM users WHERE id = ?`, id).Scan(&by)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return by, err
}

// EmailRow is one address of an account.
type EmailRow struct {
	UserID  string
	Email   string
	Primary bool
	// Verified, and who vouched for it: "" the person (or a site
	// administrator), VerifiedByTenantAdmin, or VerifiedByProvider(slug).
	Verified   bool
	VerifiedBy string
	Created    time.Time
	// UserDeleted: the account is in the recycle bin (it keeps the address
	// until it is purged).
	UserDeleted bool
}

// Holds says whether the account holds the address against everyone on the
// server: it is verified by the person, a trusted sign-in or a site
// administrator. A tenant administrator's mark alone does not count: they
// could give a person of their tenant anyone's address, verified, and keep its
// owner out of it everywhere. Such an address is held as an unverified one is:
// whoever proves it takes it (PrimaryOpts.TakeUnverified).
func (r EmailRow) Holds() bool { return r.Verified && r.VerifiedBy != VerifiedByTenantAdmin }

// VerifiedByProvider is the verified_by of an address a trusted sign-in
// vouched for: "provider:" and the provider's slug.
func VerifiedByProvider(slug string) string { return "provider:" + slug }

const emailRowCols = `e.user_id, e.email, e.is_primary, e.verified, e.verified_by, e.created, CASE WHEN u.deleted_at IS NULL THEN 0 ELSE 1 END`

func scanEmailRow(sc interface{ Scan(...any) error }) (*EmailRow, error) {
	var r EmailRow
	var primary, verified, created, deleted int64
	if err := sc.Scan(&r.UserID, &r.Email, &primary, &verified, &r.VerifiedBy, &created, &deleted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.Primary, r.Verified, r.Created, r.UserDeleted = primary != 0, verified != 0, fromMS(created), deleted != 0
	return &r, nil
}

// EmailOwner returns the row that holds an address (in EmailKey form), whoever
// holds it: verified or not, primary or not, a deleted account too. ErrNotFound
// when nobody does, or when email has no key.
func EmailOwner(ctx context.Context, q Q, email string) (*EmailRow, error) {
	key, ok := EmailKey(email)
	if !ok {
		return nil, ErrNotFound
	}
	// email_key <> '' is the partial index's condition, written out so that
	// SQLite (which cannot infer it from "= ?") uses it.
	return scanEmailRow(q.QueryRow(ctx, `SELECT `+emailRowCols+` FROM user_emails e JOIN users u ON u.id = e.user_id WHERE e.email_key = ? AND e.email_key <> ''`, key))
}

// EmailsHeld says which of the keys (EmailKey form) an account holds, under
// the rule of EmailOwner (verified or not, primary or not, a deleted account
// too): one statement per 200 keys on the unique index of the key.
func EmailsHeld(ctx context.Context, q Q, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	for i := 0; i < len(keys); i += 200 {
		chunk := keys[i:min(i+200, len(keys))]
		args := make([]any, len(chunk))
		for j, k := range chunk {
			args[j] = k
		}
		got, err := strCol(ctx, q, `SELECT e.email_key FROM user_emails e JOIN users u ON u.id = e.user_id WHERE e.email_key <> '' AND e.email_key IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for _, k := range got {
			out[k] = true
		}
	}
	return out, nil
}

// UserByVerifiedEmail is the account that signs in or resets by an address:
// the one that holds it verified (EmailRow.Holds; any of its addresses,
// primary or not), and is not deleted. row is the address that matched, as
// stored: account mail goes to it, never to what was typed. ErrNotFound when
// there is none (an address held unverified, or marked by a tenant
// administrator alone, finds nobody). It is one lookup by the unique key.
func UserByVerifiedEmail(ctx context.Context, q Q, email string) (*User, *EmailRow, error) {
	row, err := EmailOwner(ctx, q, email)
	if err != nil {
		return nil, nil, err
	}
	if !row.Holds() || row.UserDeleted {
		return nil, nil, ErrNotFound
	}
	u, err := GetUser(ctx, q, AllTenants(), row.UserID)
	if err != nil {
		return nil, nil, err
	}
	return u, row, nil
}

// UserEmails lists an account's addresses, the primary first.
func UserEmails(ctx context.Context, q Q, userID string) ([]EmailRow, error) {
	rows, err := q.Query(ctx, `SELECT `+emailRowCols+` FROM user_emails e JOIN users u ON u.id = e.user_id WHERE e.user_id = ? ORDER BY e.is_primary DESC, e.created, e.email`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EmailRow
	for rows.Next() {
		r, err := scanEmailRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// PrimaryOpts says how SetPrimaryEmail treats what is there.
type PrimaryOpts struct {
	// KeepOld keeps the old primary address as another address of the
	// account; otherwise it is removed (an account has one address today).
	KeepOld bool
	// TakeUnverified moves the address from another account that holds it
	// unverified (and is not deleted): the caller has just proved it (a
	// confirmed registration, a trusted sign-in), so it applies only when the
	// address is set verified. A holder that holds it verified (EmailRow.Holds;
	// a tenant administrator's mark alone does not) is never displaced.
	TakeUnverified bool
	// Now is when the row is made (time.Now() when zero).
	Now time.Time
}

// Released is an address SetPrimaryEmail took from another account.
type Released struct {
	UserID, Email string
}

// SetPrimaryEmail makes email the account's primary address, verified or not
// and vouched for by by, and copies it to users: one transaction (q's, or a
// new one). "" removes the primary address. An address the account already
// has (as another address) becomes the primary one, keeping its row. Another
// account holding it is ErrEmailTaken, unless opts.TakeUnverified applies; then
// released says who lost it.
func SetPrimaryEmail(ctx context.Context, q Q, userID, email string, verified bool, by string, opts PrimaryOpts) (released *Released, err error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	key := storedEmailKey(email)
	if email == "" {
		verified, by = false, ""
	}
	if !verified {
		by = ""
	}
	err = guarded(ctx, q, func(q Q) error {
		released = nil
		if key != "" {
			r, err := takeFromOther(ctx, q, userID, email, opts.TakeUnverified && verified)
			if err != nil {
				return err
			}
			released = r
		}
		// The old primary: kept as another address, or removed.
		if opts.KeepOld {
			if _, err := q.Exec(ctx, `UPDATE user_emails SET is_primary = 0 WHERE user_id = ? AND is_primary = 1`, userID); err != nil {
				return err
			}
		} else if _, err := q.Exec(ctx, `DELETE FROM user_emails WHERE user_id = ? AND is_primary = 1`, userID); err != nil {
			return err
		}
		if email != "" {
			// The same address under another spelling, or as another address of
			// the account, is replaced by this row.
			if key != "" {
				if _, err := q.Exec(ctx, `DELETE FROM user_emails WHERE user_id = ? AND email_key = ? AND email_key <> ''`, userID, key); err != nil {
					return err
				}
			}
			if _, err := q.Exec(ctx, `DELETE FROM user_emails WHERE user_id = ? AND email = ?`, userID, email); err != nil {
				return err
			}
			if testHookBeforeEmailInsert != nil {
				testHookBeforeEmailInsert(q)
			}
			if _, err := q.Exec(ctx, `INSERT INTO user_emails(user_id, email, email_key, is_primary, verified, verified_by, created) VALUES (?, ?, ?, 1, ?, ?, ?)`,
				userID, email, key, b2i(verified), by, ms(opts.Now)); err != nil {
				return emailErr(err)
			}
		}
		res, err := q.Exec(ctx, `UPDATE users SET email = ?, email_key = ?, email_verified = ?, email_verified_by = ? WHERE id = ?`, email, key, b2i(verified), by, userID)
		return oneRow(res, err)
	})
	if err != nil {
		return nil, err
	}
	return released, nil
}

// testHookBeforeEmailInsert is called with SetPrimaryEmail's transaction
// between its check of who holds the address and its insert (tests only).
var testHookBeforeEmailInsert func(Q)

// takeFromOther checks who else holds the address: nobody, or someone it may
// be taken from (then it is, and the copy on their users row is cleared).
func takeFromOther(ctx context.Context, q Q, userID, email string, take bool) (*Released, error) {
	cur, err := EmailOwner(ctx, q, email)
	if errors.Is(err, ErrNotFound) || err == nil && cur.UserID == userID {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !take || cur.Holds() || cur.UserDeleted {
		return nil, ErrEmailTaken
	}
	// Only the same mailbox: an address that is the holder's only once case
	// folded was not proved to be theirs.
	if a, ok := EmailExactKey(email); !ok {
		return nil, ErrEmailTaken
	} else if b, ok := EmailExactKey(cur.Email); !ok || a != b {
		return nil, ErrEmailTaken
	}
	if _, err := q.Exec(ctx, `DELETE FROM user_emails WHERE user_id = ? AND email = ?`, cur.UserID, cur.Email); err != nil {
		return nil, err
	}
	if cur.Primary {
		if _, err := q.Exec(ctx, `UPDATE users SET email = '', email_key = '', email_verified = 0, email_verified_by = '' WHERE id = ?`, cur.UserID); err != nil {
			return nil, err
		}
	}
	return &Released{UserID: cur.UserID, Email: cur.Email}, nil
}

// AddEmail adds an address to an account that is not its primary one (for
// accounts with several addresses). Another account holding it is
// ErrEmailTaken (or it is taken, as SetPrimaryEmail's TakeUnverified).
func AddEmail(ctx context.Context, q Q, userID, email string, verified bool, by string, takeUnverified bool, now time.Time) (released *Released, err error) {
	key, ok := EmailKey(email)
	if !ok {
		return nil, errors.New("not an address")
	}
	if !verified {
		by = ""
	}
	err = guarded(ctx, q, func(q Q) error {
		r, err := takeFromOther(ctx, q, userID, email, takeUnverified && verified)
		if err != nil {
			return err
		}
		released = r
		if testHookBeforeEmailInsert != nil {
			testHookBeforeEmailInsert(q)
		}
		_, err = q.Exec(ctx, `INSERT INTO user_emails(user_id, email, email_key, is_primary, verified, verified_by, created) VALUES (?, ?, ?, 0, ?, ?, ?)`,
			userID, email, key, b2i(verified), by, ms(now))
		return emailErr(err)
	})
	if err != nil {
		return nil, err
	}
	return released, nil
}

// SetEmailRowVerified marks one address of an account verified or not, and who
// vouched for it; for the primary address the copy on users follows in the
// same transaction. ErrNotFound when the account does not have the address.
func SetEmailRowVerified(ctx context.Context, q Q, userID, email string, verified bool, by string) error {
	if !verified {
		by = ""
	}
	return atomically(ctx, q, func(q Q) error {
		var primary int64
		err := q.QueryRow(ctx, `SELECT is_primary FROM user_emails WHERE user_id = ? AND email = ?`, userID, email).Scan(&primary)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `UPDATE user_emails SET verified = ?, verified_by = ? WHERE user_id = ? AND email = ?`, b2i(verified), by, userID, email); err != nil {
			return err
		}
		if primary == 0 {
			return nil
		}
		res, err := q.Exec(ctx, `UPDATE users SET email_verified = ?, email_verified_by = ? WHERE id = ?`, b2i(verified), by, userID)
		return oneRow(res, err)
	})
}

// RemoveEmail removes an address of an account that is not its primary one
// (ErrPrimaryEmail for that; ErrNotFound when it has no such address).
func RemoveEmail(ctx context.Context, q Q, userID, email string) error {
	return atomically(ctx, q, func(q Q) error {
		var primary int64
		err := q.QueryRow(ctx, `SELECT is_primary FROM user_emails WHERE user_id = ? AND email = ?`, userID, email).Scan(&primary)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if primary != 0 {
			return ErrPrimaryEmail
		}
		_, err = q.Exec(ctx, `DELETE FROM user_emails WHERE user_id = ? AND email = ?`, userID, email)
		return err
	})
}

// emailErr turns the unique index on the key into ErrEmailTaken (two writers
// that both saw the address free).
func emailErr(err error) error {
	if on, ok := UniqueOn(err); ok && (strings.Contains(on, "email_key") || strings.Contains(on, "user_emails_key")) {
		return ErrEmailTaken
	}
	return err
}
