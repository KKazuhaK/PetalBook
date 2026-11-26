package auth

import (
	"context"
	"errors"

	"petalbook.app/server/internal/mail"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

// More than one address per account (docs/api.md, "More than one email
// address"), behind the tenant's accounts.multipleEmails. Every address is a
// row of user_emails, unique on the server; the primary one is the UPN. Off,
// an account has its primary address only: the others are kept but neither
// shown nor used to sign in or reset, and nothing adds one.

// maxEmails is the most addresses one account has, the primary included.
const maxEmails = 10

// MultipleEmails says whether the tenant's accounts.multipleEmails is on. A
// failed read is off: an address that is not the primary then does not sign in.
func (s *Service) MultipleEmails(ctx context.Context, tenant string) bool {
	st := s.settings.Load()
	if st == nil {
		return false
	}
	e, err := st.Get(ctx, tenant, settings.AccountsMultipleEmails)
	if err != nil {
		return false
	}
	on, _ := e.Value.(bool)
	return on
}

// rowCounts says whether an address of an account counts for signing in,
// resetting and the domain requirement: the primary always, another one while
// the tenant's switch is on.
func (s *Service) rowCounts(ctx context.Context, tenant string, row store.EmailRow) bool {
	return row.Primary || s.MultipleEmails(ctx, tenant)
}

// userBySignInEmail is the account an address signs in or resets: the one that
// holds it verified (store.UserByVerifiedEmail), when the address counts for it
// (rowCounts). ErrNotFound otherwise.
func (s *Service) userBySignInEmail(ctx context.Context, email string) (*store.User, *store.EmailRow, error) {
	u, row, err := store.UserByVerifiedEmail(ctx, s.st.Q(), email)
	if err != nil {
		return nil, nil, err
	}
	if !s.rowCounts(ctx, u.TenantID, *row) {
		return nil, nil, store.ErrNotFound
	}
	return u, row, nil
}

// Emails lists an account's addresses as they are shown: all of them while the
// switch is on, else the primary alone. One query.
func (s *Service) Emails(ctx context.Context, u store.User) ([]store.EmailRow, error) {
	rows, err := store.UserEmails(ctx, s.st.Q(), u.ID)
	if err != nil {
		return nil, err
	}
	if s.MultipleEmails(ctx, u.TenantID) {
		return rows, nil
	}
	out := rows[:0]
	for _, r := range rows {
		if r.Primary {
			out = append(out, r)
		}
	}
	return out, nil
}

// PendingClaims lists the claims the account waits on: addresses it asked to
// add that another account holds unverified, which have no row until the link
// sent to them is followed. None while the switch is off.
func (s *Service) PendingClaims(ctx context.Context, u store.User) ([]store.EmailClaim, error) {
	if !s.MultipleEmails(ctx, u.TenantID) {
		return nil, nil
	}
	return store.PendingEmailClaims(ctx, s.st.Q(), u.ID, s.now())
}

// HasVerifiedEmailIn says whether the account holds, verified, an address in
// one of the domains (or below one): the condition of an organisation's
// domain requirement. Any address counts, the primary or another while the
// switch is on; an address only a tenant administrator marked does not
// (store.EmailRow.Holds). Nothing calls it yet: the organisation's domain
// requirement (milestone 3.6, the organisation part) is where it is meant to
// be called.
func (s *Service) HasVerifiedEmailIn(ctx context.Context, u store.User, domains []string) (bool, error) {
	if len(domains) == 0 {
		return false, nil
	}
	rows, err := s.Emails(ctx, u)
	if err != nil {
		return false, err
	}
	for _, r := range rows {
		if r.Holds() && domainAllowed(r.Email, domains) {
			return true, nil
		}
	}
	return false, nil
}

// ownEmail finds an address among the account's, compared by key: nil when it
// has none such.
func ownEmail(rows []store.EmailRow, email string) *store.EmailRow {
	key, ok := store.EmailKey(email)
	if !ok {
		return nil
	}
	for i := range rows {
		if k, _ := store.EmailKey(rows[i].Email); k == key {
			return &rows[i]
		}
	}
	return nil
}

func errNoSuchEmail() *Error { return errf(NotFound, "This account has no such email address.") }

// AddEmail adds an address to the person's account and sends it a link that
// proves it; the primary address is told. An address free on the server
// becomes an unverified row at once; one another account holds unverified (or
// that only a tenant administrator marked) waits as the link alone and moves
// when the link is followed (ConfirmEmail); one another account holds is 409
// email_taken. The answer is the same for the two first cases.
func (s *Service) AddEmail(ctx context.Context, u store.User, email, lang, ip string) error {
	if err := s.needMail(); err != nil {
		return err
	}
	e, err := s.cleanEmailFor(ctx, email)
	if err != nil {
		return err
	}
	if e == "" || !plainAddress(e) {
		return errf(Invalid, "That does not look like an email address.")
	}
	rows, err := store.UserEmails(ctx, s.st.Q(), u.ID)
	if err != nil {
		return err
	}
	if ownEmail(rows, e) != nil {
		return errf(Exists, "This address is already on your account.")
	}
	pending, err := store.CountEmailTokens(ctx, s.st.Q(), u.ID, store.TokenVerify, s.now())
	if err != nil {
		return err
	}
	if len(rows) >= maxEmails || len(rows)+pending >= 2*maxEmails {
		return coded(409, "email_limit", "An account can have at most 10 email addresses.")
	}
	claim := false
	switch row, err := store.EmailOwner(ctx, s.st.Q(), e); {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return err
	case row.Holds() || row.UserDeleted:
		return errEmailTaken()
	default:
		claim = true // held by another account unverified: proved, it moves
	}
	// The link goes to an address the person typed: held to the address's and
	// the caller's caps.
	if wait, ok := s.mailSlot(e, ip); !ok {
		return retryAfter(wait)
	}
	var tok string
	err = s.st.Tx(ctx, func(q store.Q) error {
		if !claim {
			if _, err := store.AddEmail(ctx, q, u.ID, e, false, "", false, s.now()); err != nil {
				return emailErr(err)
			}
		}
		var err error
		tok, err = s.newEmailToken(ctx, q, store.TokenVerify, u.ID, e, verifyTTL)
		return err
	})
	if err != nil {
		return err
	}
	s.Audit(AuditEvent{Kind: "email_added", Actor: u.ID, User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"email": e}})
	lang = mail.PickLang(lang, "")
	kind := mail.KindVerify
	if claim {
		kind = mail.KindEmailClaim
	}
	if err := s.sendMail(kind, lang, e, mail.Vars{Name: u.Name, Username: u.Username, Email: e, Link: s.link("/verify-email", tok), Hours: 24}); err != nil {
		return err
	}
	s.noteAddresses(ctx, mail.KindEmailAdded, lang, e, u.Email)
	return nil
}

// ResendEmailVerify sends one of the person's addresses a new link that proves
// it (the earlier link of that address stops working). The primary is
// RequestEmailVerify's; another address is 409 email_verified when the person
// already holds it.
func (s *Service) ResendEmailVerify(ctx context.Context, u store.User, email, lang, ip string) error {
	if err := s.needMail(); err != nil {
		return err
	}
	rows, err := store.UserEmails(ctx, s.st.Q(), u.ID)
	if err != nil {
		return err
	}
	row := ownEmail(rows, email)
	if row == nil {
		return errNoSuchEmail()
	}
	if row.Primary {
		return s.RequestEmailVerify(ctx, u, lang, ip)
	}
	if row.Holds() {
		return coded(409, "email_verified", "This address is already verified.")
	}
	if !s.mailable(ctx, row.Email) {
		return errNotASCII()
	}
	if wait, ok := s.mailSlot(row.Email, ip); !ok {
		return retryAfter(wait)
	}
	var tok string
	if err := s.st.Tx(ctx, func(q store.Q) (err error) {
		tok, err = s.newEmailToken(ctx, q, store.TokenVerify, u.ID, row.Email, verifyTTL)
		return err
	}); err != nil {
		return err
	}
	return s.sendMail(mail.KindVerify, mail.PickLang(lang, ""), row.Email, mail.Vars{Name: u.Name, Username: u.Username, Email: row.Email, Link: s.link("/verify-email", tok), Hours: 24})
}

// MakePrimaryEmail makes one of the person's verified addresses the primary
// one (the UPN, where notices go); the old primary stays as another address.
// Both are told. 409 email_unverified for an address the person does not hold
// (unverified, or only a tenant administrator marked it).
func (s *Service) MakePrimaryEmail(ctx context.Context, u store.User, email, lang, ip string) (*store.User, error) {
	rows, err := store.UserEmails(ctx, s.st.Q(), u.ID)
	if err != nil {
		return nil, err
	}
	row := ownEmail(rows, email)
	if row == nil {
		return nil, errNoSuchEmail()
	}
	if row.Primary {
		return &u, nil
	}
	switch err := store.MakePrimaryEmail(ctx, s.st.Q(), u.ID, row.Email); {
	case errors.Is(err, store.ErrEmailUnverified):
		return nil, coded(409, "email_unverified", "Only a verified address can be the primary one; confirm it first.")
	case errors.Is(err, store.ErrNotFound):
		return nil, errNoSuchEmail()
	case errors.Is(err, store.ErrPrimaryRace):
		return nil, errf(Conflict, "The primary address was being changed at the same moment; try again.")
	case err != nil:
		return nil, err
	}
	old := u.Email
	s.Audit(AuditEvent{Kind: "email_primary_changed", Actor: u.ID, User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"email": row.Email, "old": old}})
	s.dropUser(u.ID)
	u.Email, u.EmailVerified = row.Email, true
	s.cfg.Spaces.UserChanged(ctx, u)
	s.noteAddresses(ctx, mail.KindEmailPrimary, mail.PickLang(lang, ""), row.Email, row.Email, old)
	return &u, nil
}

// RemoveEmail removes one of the person's addresses that is not the primary
// one (409 primary_email for that: make another primary first), with the
// links sent to it. An address that is only a pending claim (no row yet) is
// withdrawn: its link stops working. The primary is told, and the address itself when it was
// verified.
func (s *Service) RemoveEmail(ctx context.Context, u store.User, email, lang, ip string) error {
	rows, err := store.UserEmails(ctx, s.st.Q(), u.ID)
	if err != nil {
		return err
	}
	row := ownEmail(rows, email)
	if row == nil {
		return s.withdrawClaim(ctx, u, email, ip)
	}
	err = s.st.Tx(ctx, func(q store.Q) error {
		if err := store.RemoveEmail(ctx, q, u.ID, row.Email); err != nil {
			return err
		}
		return store.DeleteEmailTokensFor(ctx, q, u.ID, row.Email)
	})
	switch {
	case errors.Is(err, store.ErrPrimaryEmail):
		return coded(409, "primary_email", "The primary address cannot be removed; make another address primary first.")
	case errors.Is(err, store.ErrNotFound):
		return errNoSuchEmail()
	case err != nil:
		return err
	}
	s.Audit(AuditEvent{Kind: "email_removed", Actor: u.ID, User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"email": row.Email}})
	to := []string{u.Email}
	if row.Holds() {
		to = append(to, row.Email)
	}
	s.noteAddresses(ctx, mail.KindEmailRemoved, mail.PickLang(lang, ""), row.Email, to...)
	return nil
}

// withdrawClaim drops the person's pending claim of an address (the link that
// would add it, see AddEmail): errNoSuchEmail when there is none. Nothing is
// told: no address changed.
func (s *Service) withdrawClaim(ctx context.Context, u store.User, email, ip string) error {
	key, ok := store.EmailKey(email)
	if !ok {
		return errNoSuchEmail()
	}
	claims, err := store.PendingEmailClaims(ctx, s.st.Q(), u.ID, s.now())
	if err != nil {
		return err
	}
	for _, c := range claims {
		if k, _ := store.EmailKey(c.Email); k != key {
			continue
		}
		if err := store.DeleteEmailTokensFor(ctx, s.st.Q(), u.ID, c.Email); err != nil {
			return err
		}
		s.Audit(AuditEvent{Kind: "email_claim_withdrawn", Actor: u.ID, User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"email": c.Email}})
		return nil
	}
	return errNoSuchEmail()
}

// noteAddresses sends a note with no link about an address to each of to that
// may be sent account mail. It is held to each address's cap, not the
// caller's: it goes only to the account's own addresses. A note that cannot
// go is left out.
func (s *Service) noteAddresses(ctx context.Context, kind, lang, about string, to ...string) {
	if !s.MailEnabled() {
		return
	}
	seen := map[string]bool{}
	for _, addr := range to {
		key, ok := store.EmailKey(addr)
		if !ok || seen[key] || !s.mailable(ctx, addr) {
			continue
		}
		seen[key] = true
		if _, ok := s.mailSlot(addr, ""); !ok {
			continue
		}
		if err := s.sendMail(kind, lang, addr, mail.Vars{Email: about}); err != nil {
			s.log.Warn("could not queue a message", "reason", err.Error())
		}
	}
}

// confirmOtherEmail is ConfirmEmail for an address that is not the account's
// primary one: the switch must be on; a row the account has is marked
// verified, and an address it claimed (another account held it unverified)
// is taken. Another account that proved it first is 409 email_taken.
func (s *Service) confirmOtherEmail(ctx context.Context, u store.User, t store.EmailToken, hash string) (*store.Released, error) {
	if !s.MultipleEmails(ctx, u.TenantID) {
		return nil, errBadLink()
	}
	var released *store.Released
	err := s.st.Tx(ctx, func(q store.Q) error {
		if ok, err := store.UseEmailToken(ctx, q, hash, s.now()); err != nil {
			return err
		} else if !ok {
			return errBadLink()
		}
		switch err := store.SetEmailRowVerified(ctx, q, u.ID, t.Email, true, ""); {
		case err == nil:
			return nil
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
		// A claim: the account must still have room for it.
		rows, err := store.UserEmails(ctx, q, u.ID)
		if err != nil {
			return err
		}
		if len(rows) >= maxEmails {
			return coded(409, "email_limit", "An account can have at most 10 email addresses.")
		}
		r, err := store.AddEmail(ctx, q, u.ID, t.Email, true, "", true, s.now())
		if err != nil {
			return emailErr(err)
		}
		released = r
		return nil
	})
	return released, err
}
