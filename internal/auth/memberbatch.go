package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/mail"
	"petalbook.app/server/internal/ratelimit"
	"petalbook.app/server/internal/store"
)

// Inviting many people at once (docs/api.md, "Adding people in batches"): the
// batch checks what a single invite checks once, makes the invites of 200
// rows in one transaction, and mails them afterwards, within a cap.

// MailBulkPerHour is how many invitations one administrator's batches may
// send in an hour; the rows past it get a link to hand over instead.
const MailBulkPerHour = 300

const inviteDays = 7

// InviteSpec is a new person a batch invites.
type InviteSpec struct {
	Email  string
	Name   string
	Role   string
	Groups []string // ids of local groups of the tenant
}

// The codes of a spec InviteMany refuses that a batch reports as their own
// status: an address an account holds, and one with an open invite already.
const (
	CodeEmailTaken     = "email_taken"
	CodeAlreadyInvited = "already_invited"
)

// How an invitation reached (or did not reach) its person.
const (
	MailSent    = "sent"    // queued on the server's mail
	MailNone    = "none"    // no mail is configured: the link is for the administrator to hand over
	MailLimited = "limited" // the cap of an address or of the administrator is spent: same
)

// InviteOutcome is one made invite. Token is shown once; Link is the address
// to hand over (always given: a mailed invitation can still be copied).
type InviteOutcome struct {
	ID, Token, Link string
	Mail            string
	// Sender is whose mail sent it: "site", or "" when none was sent. A tenant's
	// own sender (the integrations.smtp capability) takes this place once a
	// tenant can configure one; until then the site's is the only one.
	Sender string
}

// MailSender is whose mail an invitation of the tenant goes by: "site" when
// the server's mail is enabled, "" when no mail is configured anywhere.
func (s *Service) MailSender(tenantID string) string {
	if s.MailEnabled() {
		return "site"
	}
	return ""
}

// joinInvitedGroups puts a person just made into the groups their invite
// named; a group that is gone meanwhile, or of another tenant, is skipped.
func (s *Service) joinInvitedGroups(ctx context.Context, q store.Q, u store.User, groups []string) error {
	for _, g := range groups {
		if err := store.AddLocalMembers(ctx, q, g, []string{u.ID}, s.now()); err != nil {
			return err
		}
	}
	return nil
}

// InviteMany makes an invite for each spec in the tenant, as CreateInvite does
// for one (the same checks of the caller and the tenant, done once). The result
// has an entry per spec, in order. A spec that cannot be made has its error
// (the rest are made); dry only checks and makes nothing.
func (s *Service) InviteMany(ctx context.Context, actor store.User, access authz.Subject, tenantID string, specs []InviteSpec, dry bool) ([]InviteOutcome, []error, error) {
	if !access.Can(authz.TenantInvitesManage, tenantID) {
		return nil, nil, errf(NotFound, "No such tenant.")
	}
	if t, err := store.GetTenant(ctx, s.st.Q(), tenantID); errors.Is(err, store.ErrNotFound) {
		return nil, nil, errf(NotFound, "No such tenant.")
	} else if err != nil {
		return nil, nil, err
	} else if t.Disabled {
		return nil, nil, errf(Conflict, "That tenant is disabled.")
	}
	out := make([]InviteOutcome, len(specs))
	errs := make([]error, len(specs))
	now := s.now().UTC()
	var made []int
	invs := make([]store.Invite, len(specs))
	for i, sp := range specs {
		role := sp.Role
		if role == "" {
			role = "member"
		}
		if err := checkRole(role); err != nil {
			errs[i] = err
			continue
		}
		// The rules of CleanInviteFill; its look for an account holding the
		// address is made for all rows at once below.
		f, err := s.cleanInviteFillFor(ctx, InviteFill{Name: sp.Name, Email: sp.Email})
		if err == nil && f.Email == "" {
			err = errf(Invalid, "An invitation needs an email address.")
		}
		if err != nil {
			errs[i] = err
			continue
		}
		token := randomToken()
		invs[i] = store.Invite{ID: s.newID(), TokenHash: hashToken(token), Role: role, CreatedBy: actor.ID, Created: now, TenantID: tenantID,
			Expires: now.Add(inviteDays * 24 * time.Hour), Name: f.Name, Email: f.Email, Groups: sp.Groups}
		out[i] = InviteOutcome{ID: invs[i].ID, Token: token, Link: s.inviteLink(token)}
		made = append(made, i)
	}
	// An address any account holds (a disabled or deleted one's too) is 409
	// email_taken, as CleanInviteFill says: the invite would make an account it
	// cannot have, and its mail would go to someone else's address. One with an
	// open invite of the tenant is not invited (nor mailed) again. One lookup
	// per 200 addresses for each.
	mailKeys := make([]string, 0, len(made))
	for _, i := range made {
		if k, ok := store.EmailKey(invs[i].Email); ok {
			mailKeys = append(mailKeys, k)
		}
	}
	held, err := store.EmailsHeld(ctx, s.st.Q(), mailKeys)
	if err != nil {
		return nil, nil, err
	}
	open, err := store.OpenInviteEmails(ctx, s.st.Q(), store.InTenant(tenantID), mailKeys, now)
	if err != nil {
		return nil, nil, err
	}
	if len(held) > 0 || len(open) > 0 {
		kept := made[:0]
		for _, i := range made {
			k, _ := store.EmailKey(invs[i].Email)
			switch {
			case held[k]:
				errs[i], out[i] = errEmailTaken(), InviteOutcome{}
			case open[k]:
				errs[i], out[i] = coded(409, CodeAlreadyInvited, "This address has an open invitation already."), InviteOutcome{}
			default:
				kept = append(kept, i)
			}
		}
		made = kept
	}
	if dry {
		return out, errs, nil
	}
	for start := 0; start < len(made); start += 200 {
		chunk := made[start:min(start+200, len(made))]
		err := s.st.Tx(ctx, func(q store.Q) error {
			for _, i := range chunk {
				if err := store.InsertInvite(ctx, q, invs[i]); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	sender := s.MailSender(tenantID)
	for _, i := range made {
		out[i].Mail = MailNone
		if sender == "" {
			continue
		}
		key, ok := store.EmailKey(invs[i].Email)
		if !ok {
			key = invs[i].Email
		}
		if _, ok := s.mailCaps.TakeAll(ratelimit.Cost{Key: "to:" + key, Limit: mailCapPerAddress}, ratelimit.Cost{Key: "bulk:" + actor.ID, Limit: MailBulkPerHour}); !ok {
			out[i].Mail = MailLimited
			continue
		}
		m := mail.Render(mail.KindInvite, mail.PickLang("", ""), mail.Vars{Site: s.siteName(), Email: invs[i].Email, Link: out[i].Link, Hours: inviteDays * 24})
		m.To = invs[i].Email
		if err := s.mail.Enqueue(m); err != nil {
			out[i].Mail = MailNone
			if errors.Is(err, mail.ErrQueueFull) {
				out[i].Mail = MailLimited
			}
			continue
		}
		out[i].Mail, out[i].Sender = MailSent, sender
	}
	return out, errs, nil
}

// inviteLink is the address that accepts an invite.
func (s *Service) inviteLink(token string) string {
	return strings.TrimRight(s.cfg.PublicURL, "/") + "/invite#" + token
}
