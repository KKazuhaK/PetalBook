package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// A linked account is a person's account at a provider (linked_accounts): the
// sign-in links of consolidation 5a (signin = 1, no tokens). One person has at
// most one per provider, and one provider's subject belongs to one person.
// An account is found by the provider's own id and the subject, never by the
// issuer string an administrator typed: a second organisation can copy an
// issuer, and must not reach another's accounts.

// Link is a linked account as sign-in uses it.
type Link struct {
	ID, ProviderID, UserID, Subject string
	Login, Email                    string
	// EmailTrusted: the provider's address passed the trust rules at the last sign-in.
	EmailTrusted bool
	SignIn       bool
	Label        string
	Created      time.Time
	LastUsed     time.Time
}

// Errors of InsertLink: the subject is already some other person's link at the
// provider, or the person already has a link at the provider.
var (
	ErrLinkSubjectTaken  = errors.New("store: that account is linked to another person")
	ErrLinkProviderTaken = errors.New("store: the person already has an account linked at that provider")
)

const linkCols = `id, provider_id, user_id, subject, login, email, email_trusted, signin, label, created, last_used`

func scanLink(sc interface{ Scan(...any) error }) (*Link, error) {
	var l Link
	var tr, si, cr, lu int64
	if err := sc.Scan(&l.ID, &l.ProviderID, &l.UserID, &l.Subject, &l.Login, &l.Email, &tr, &si, &l.Label, &cr, &lu); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	l.EmailTrusted, l.SignIn, l.Created = tr != 0, si != 0, fromMS(cr)
	if lu != 0 {
		l.LastUsed = fromMS(lu)
	}
	return &l, nil
}

// InsertLink adds a link. A subject that is already some person's at the
// provider is ErrLinkSubjectTaken, a second link of the same person at the
// provider ErrLinkProviderTaken: the unique indexes decide, so two sign-ins at
// once cannot both win.
func InsertLink(ctx context.Context, q Q, l Link) error {
	_, err := q.Exec(ctx, `INSERT INTO linked_accounts(`+linkCols+`, updated) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, l.ProviderID, l.UserID, l.Subject, l.Login, l.Email, b2i(l.EmailTrusted), b2i(l.SignIn), l.Label, ms(l.Created), zeroMS(l.LastUsed), ms(l.Created))
	if on, ok := UniqueOn(err); ok {
		switch {
		case strings.Contains(on, "subject"):
			return ErrLinkSubjectTaken
		case strings.Contains(on, "user_id"):
			return ErrLinkProviderTaken
		}
	}
	return err
}

// FindLink looks a link up by provider id and subject: the one query of a
// sign-in.
func FindLink(ctx context.Context, q Q, providerID, subject string) (*Link, error) {
	return scanLink(q.QueryRow(ctx, `SELECT `+linkCols+` FROM linked_accounts WHERE provider_id = ? AND subject = ?`, providerID, subject))
}

// FindUserLink looks a person's link at a provider up.
func FindUserLink(ctx context.Context, q Q, providerID, userID string) (*Link, error) {
	return scanLink(q.QueryRow(ctx, `SELECT `+linkCols+` FROM linked_accounts WHERE provider_id = ? AND user_id = ?`, providerID, userID))
}

// ListLinks returns a person's sign-in links, oldest first.
func ListLinks(ctx context.Context, q Q, userID string) ([]Link, error) {
	rows, err := q.Query(ctx, `SELECT `+linkCols+` FROM linked_accounts WHERE user_id = ? AND signin = 1 ORDER BY created, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// DeleteLink removes one of the person's links; ErrNotFound when it is not
// theirs or does not exist.
func DeleteLink(ctx context.Context, q Q, id, userID string) error {
	res, err := q.Exec(ctx, `DELETE FROM linked_accounts WHERE id = ? AND user_id = ? AND signin = 1`, id, userID)
	return oneRow(res, err)
}

// TouchLink records a sign-in through a link: the time, and the address the
// provider gave (and whether it passed the trust rules). It writes only when
// something changed or the last use is over a day old, so an ordinary sign-in
// costs no write.
func TouchLink(ctx context.Context, q Q, l *Link, email string, trusted bool, label string, now time.Time) error {
	if l.Email == email && l.EmailTrusted == trusted && l.Label == label && now.Sub(l.LastUsed) < 24*time.Hour {
		return nil
	}
	_, err := q.Exec(ctx, `UPDATE linked_accounts SET email = ?, email_trusted = ?, label = ?, last_used = ?, updated = ? WHERE id = ?`,
		email, b2i(trusted), label, ms(now), ms(now), l.ID)
	return err
}

// UserLink is a link with what its repositories part says: a link is a sign-in,
// a repository connection, or both (the GitHub row's).
type UserLink struct {
	Link
	// Repos: the link holds tokens, or held them and lost them (State "expired").
	Repos bool
	// State is "ok", or "expired": the person authorises again.
	State          string
	RefreshExpires time.Time
	Admin          bool
}

const userLinkCols = linkCols + `, (access_enc <> '' OR state <> 'ok'), state, refresh_expires, is_admin`

func scanUserLink(sc interface{ Scan(...any) error }) (*UserLink, error) {
	var l UserLink
	var tr, si, cr, lu, repos, re, admin int64
	if err := sc.Scan(&l.ID, &l.ProviderID, &l.UserID, &l.Subject, &l.Login, &l.Email, &tr, &si, &l.Label, &cr, &lu, &repos, &l.State, &re, &admin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	l.EmailTrusted, l.SignIn, l.Created, l.Repos, l.Admin = tr != 0, si != 0, fromMS(cr), repos != 0, admin != 0
	if lu != 0 {
		l.LastUsed = fromMS(lu)
	}
	if re > 0 {
		l.RefreshExpires = fromMS(re)
	}
	return &l, nil
}

// ListUserLinks returns every link a person has, whatever it is for, oldest first.
func ListUserLinks(ctx context.Context, q Q, userID string) ([]UserLink, error) {
	rows, err := q.Query(ctx, `SELECT `+userLinkCols+` FROM linked_accounts WHERE user_id = ? ORDER BY created, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserLink
	for rows.Next() {
		l, err := scanUserLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// GetUserLink returns one of the person's links by its id, or ErrNotFound.
func GetUserLink(ctx context.Context, q Q, id, userID string) (*UserLink, error) {
	return scanUserLink(q.QueryRow(ctx, `SELECT `+userLinkCols+` FROM linked_accounts WHERE id = ? AND user_id = ?`, id, userID))
}

// EnableLinkSignin lets a link that was made to connect repositories sign the
// person in too, with the address the provider gave when they proved the
// account. ErrNotFound when it is not the person's.
func EnableLinkSignin(ctx context.Context, q Q, id, userID, email string, trusted bool, label string, now time.Time) error {
	res, err := q.Exec(ctx, `UPDATE linked_accounts SET signin = 1, email = ?, email_trusted = ?, label = ?, last_used = ?, updated = ? WHERE id = ? AND user_id = ?`,
		email, b2i(trusted), label, ms(now), ms(now), id, userID)
	return oneRow(res, err)
}
