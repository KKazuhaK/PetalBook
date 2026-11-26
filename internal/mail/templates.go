package mail

import (
	"fmt"
	"html"
	"strings"

	"petalbook.app/server/internal/langtag"
)

// The kinds of message the server sends.
const (
	KindVerify      = "verify"
	KindEmailClaim  = "email_claim" // verify, for an address another account holds unverified
	KindRegister    = "register"
	KindRegisterDup = "register_exists"
	// KindRegisterOrg confirms a registration that makes an organisation;
	// KindRegisterOrgRefused says that none was made (a cap was reached).
	KindRegisterOrg        = "register_org"
	KindRegisterOrgRefused = "register_org_refused"
	KindReset              = "reset"
	KindResetSSO           = "reset_sso"
	KindEmailChanged       = "email_changed"
	// Notes with no link about the addresses of an account with several
	// (accounts.multipleEmails): one was added, removed, or made primary.
	KindEmailAdded   = "email_added"
	KindEmailRemoved = "email_removed"
	KindEmailPrimary = "email_primary"
	KindInvite       = "invite"
	KindTest         = "test"
)

// Vars are what a template may use.
type Vars struct {
	Site     string // the host of the public address
	Name     string // the account's display name, beside its username
	Username string
	Org      string // the name of the organisation a registration makes
	Email    string
	Link     string
	Links    []Link // reset: one per account sharing the address
	Provider string // "sign in with {provider}"
	Hours    int
}

// Link is a link and the account it is for.
type Link struct{ Username, URL string }

// Langs are the languages the messages exist in.
var Langs = []string{"en", "zh-Hans"}

// PickLang chooses the language of a message: the body's lang, else the
// Accept-Language header, else English.
func PickLang(lang, acceptLanguage string) string {
	if c, ok := langtag.Canonical(lang); ok {
		if m, ok := langtag.Match(c, Langs); ok {
			return m
		}
	}
	for _, part := range strings.Split(acceptLanguage, ",") {
		tag := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if c, ok := langtag.Canonical(tag); ok {
			if m, ok := langtag.Match(c, Langs); ok {
				return m
			}
		}
	}
	return "en"
}

type body struct {
	subject string
	paras   []string
	link    string // the first link, as a line of its own
	links   []Link
	foot    string
}

// Render builds a message of the kind in the language.
func Render(kind, lang string, v Vars) Message {
	b := templates(kind, lang, v)
	var text, ht strings.Builder
	for _, p := range b.paras {
		text.WriteString(p + "\n\n")
		ht.WriteString("<p>" + html.EscapeString(p) + "</p>\n")
	}
	if b.link != "" {
		text.WriteString(b.link + "\n\n")
		ht.WriteString(`<p><a href="` + html.EscapeString(b.link) + `">` + html.EscapeString(b.link) + "</a></p>\n")
	}
	for _, l := range b.links {
		text.WriteString(l.Username + ": " + l.URL + "\n\n")
		ht.WriteString("<p>" + html.EscapeString(l.Username) + `: <a href="` + html.EscapeString(l.URL) + `">` + html.EscapeString(l.URL) + "</a></p>\n")
	}
	if b.foot != "" {
		text.WriteString(b.foot + "\n")
		ht.WriteString(`<p style="color:#666">` + html.EscapeString(b.foot) + "</p>\n")
	}
	return Message{Subject: b.subject, Text: text.String(), HTML: "<!doctype html><html><body>\n" + ht.String() + "</body></html>"}
}

func templates(kind, lang string, v Vars) body {
	if lang == "zh-Hans" {
		return zh(kind, v)
	}
	return en(kind, v)
}

// account names the account a verification link is for: "Name (username)".
func account(v Vars) string {
	switch {
	case v.Name != "" && v.Name != v.Username && v.Username != "":
		return fmt.Sprintf("%s (%s)", v.Name, v.Username)
	case v.Username != "":
		return v.Username
	}
	return v.Name
}

func en(kind string, v Vars) body {
	ignore := "If you did not ask for this, you can ignore this message; nothing was changed."
	switch kind {
	case KindVerify:
		para := fmt.Sprintf("Follow this link to confirm that %s is your address on %s. It works once and for %d hours.", v.Email, v.Site, v.Hours)
		if a := account(v); a != "" {
			para = fmt.Sprintf("Follow this link to confirm that %s is your address on %s, for the account %s. It works once and for %d hours.", v.Email, v.Site, a, v.Hours)
		}
		return body{subject: "Confirm your email address", paras: []string{para}, link: v.Link, foot: ignore}
	case KindEmailClaim:
		return body{subject: "Confirm adding this address to an account", paras: []string{fmt.Sprintf("Someone asked to add %s to the account %s on %s. Follow this link to confirm that it is yours: it is then added to that account, and if another account has it without having confirmed it, it is moved away from that account. It works once and for %d hours.", v.Email, account(v), v.Site, v.Hours)}, link: v.Link, foot: ignore}
	case KindRegister:
		return body{subject: "Confirm your registration", paras: []string{fmt.Sprintf("Follow this link to finish making your account %q on %s. It works once and for %d hours.", v.Username, v.Site, v.Hours)}, link: v.Link, foot: ignore}
	case KindRegisterOrg:
		return body{subject: "Confirm your registration", paras: []string{fmt.Sprintf("Follow this link to finish making your account %q and your organization %q on %s. You will be its administrator. It works once and for %d hours.", v.Username, v.Org, v.Site, v.Hours)}, link: v.Link, foot: ignore}
	case KindRegisterOrgRefused:
		return body{subject: "Your organization was not made", paras: []string{fmt.Sprintf("Someone asked to register this address on %s and to make the organization %q, but no organization can be made right now, so nothing was made. You can try again later.", v.Site, v.Org)}, foot: ignore}
	case KindRegisterDup:
		return body{subject: "You already have an account", paras: []string{fmt.Sprintf("Someone tried to register this address on %s, but it already has an account. If it was you, you can reset the password with this link (it works once and for %d hour).", v.Site, v.Hours)}, link: v.Link, foot: ignore}
	case KindReset:
		return body{subject: "Reset your password", paras: []string{fmt.Sprintf("Follow a link to choose a new password on %s. Each works once and for %d hour; asking again makes earlier links stop working.", v.Site, v.Hours)}, link: v.Link, links: v.Links, foot: ignore}
	case KindResetSSO:
		return body{subject: "Sign in with " + v.Provider, paras: []string{fmt.Sprintf("Someone asked to reset the password of your account on %s. Your account signs in with %s, so there is no password to reset: sign in with %s instead.", v.Site, v.Provider, v.Provider)}, foot: ignore}
	case KindInvite:
		return body{subject: "You are invited to " + v.Site, paras: []string{fmt.Sprintf("You were invited to make an account on %s. Follow this link to choose your username and password. It works once and for %d days.", v.Site, v.Hours/24)}, link: v.Link, foot: "If you were not expecting this, you can ignore this message; no account is made until you follow the link."}
	case KindEmailChanged:
		return body{subject: "The email address of your account was changed", paras: []string{fmt.Sprintf("The email address of your account on %s was changed. If this was not you, tell an administrator.", v.Site)}}
	case KindEmailAdded:
		return body{subject: "An email address was added to your account", paras: []string{fmt.Sprintf("%s was added to your account on %s. It counts once the link sent to it is followed. If this was not you, remove it and change your password.", v.Email, v.Site)}}
	case KindEmailRemoved:
		return body{subject: "An email address was removed from your account", paras: []string{fmt.Sprintf("%s was removed from your account on %s. If this was not you, tell an administrator.", v.Email, v.Site)}}
	case KindEmailPrimary:
		return body{subject: "The primary email address of your account was changed", paras: []string{fmt.Sprintf("%s is now the primary email address of your account on %s: you sign in with it, and notices go to it. If this was not you, tell an administrator.", v.Email, v.Site)}}
	default:
		return body{subject: "A test message", paras: []string{fmt.Sprintf("This is a test message from %s. Mail is working.", v.Site)}}
	}
}

func zh(kind string, v Vars) body {
	ignore := "如果不是你本人要的，忽略这封信即可，什么都没有改动。"
	switch kind {
	case KindVerify:
		para := fmt.Sprintf("点开下面的链接，确认 %s 是你在 %s 上的邮箱。链接只能用一次，%d 小时内有效。", v.Email, v.Site, v.Hours)
		if a := account(v); a != "" {
			para = fmt.Sprintf("点开下面的链接，确认 %s 是你在 %s 上的邮箱，对应账号 %s。链接只能用一次，%d 小时内有效。", v.Email, v.Site, a, v.Hours)
		}
		return body{subject: "确认你的邮箱地址", paras: []string{para}, link: v.Link, foot: ignore}
	case KindEmailClaim:
		return body{subject: "确认把这个邮箱加到账号", paras: []string{fmt.Sprintf("有人要把 %s 加到 %s 上的账号 %s。点开下面的链接确认它是你的：它会加到该账号；如果另一个账号持有它但没有确认过，它会从那个账号移走。链接只能用一次，%d 小时内有效。", v.Email, v.Site, account(v), v.Hours)}, link: v.Link, foot: ignore}
	case KindRegister:
		return body{subject: "确认你的注册", paras: []string{fmt.Sprintf("点开下面的链接，完成在 %s 上创建账号「%s」。链接只能用一次，%d 小时内有效。", v.Site, v.Username, v.Hours)}, link: v.Link, foot: ignore}
	case KindRegisterOrg:
		return body{subject: "确认你的注册", paras: []string{fmt.Sprintf("点开下面的链接，完成在 %s 上创建账号「%s」和组织「%s」，你将是这个组织的管理员。链接只能用一次，%d 小时内有效。", v.Site, v.Username, v.Org, v.Hours)}, link: v.Link, foot: ignore}
	case KindRegisterOrgRefused:
		return body{subject: "你的组织没有创建", paras: []string{fmt.Sprintf("有人想用这个邮箱在 %s 上注册并创建组织「%s」，但现在不能再创建组织，所以什么都没有创建。可以稍后再试。", v.Site, v.Org)}, foot: ignore}
	case KindRegisterDup:
		return body{subject: "你已经有账号了", paras: []string{fmt.Sprintf("有人想用这个邮箱在 %s 上注册，但它已经有账号了。如果是你，可以用下面的链接重置密码（只能用一次，%d 小时内有效）。", v.Site, v.Hours)}, link: v.Link, foot: ignore}
	case KindReset:
		return body{subject: "重置你的密码", paras: []string{fmt.Sprintf("点开链接，在 %s 上设置新密码。每个链接只能用一次，%d 小时内有效；再要一次，之前的链接就失效。", v.Site, v.Hours)}, link: v.Link, links: v.Links, foot: ignore}
	case KindResetSSO:
		return body{subject: "请用 " + v.Provider + " 登录", paras: []string{fmt.Sprintf("有人要重置你在 %s 上的账号密码。你的账号通过 %s 登录，没有可以重置的密码，请直接用 %s 登录。", v.Site, v.Provider, v.Provider)}, foot: ignore}
	case KindInvite:
		return body{subject: "邀请你加入 " + v.Site, paras: []string{fmt.Sprintf("你被邀请在 %s 上创建账号。点开下面的链接，设置用户名和密码。链接只能用一次，%d 天内有效。", v.Site, v.Hours/24)}, link: v.Link, foot: "如果你没有预期这封邀请，忽略即可；点开链接之前不会创建任何账号。"}
	case KindEmailChanged:
		return body{subject: "你账号的邮箱地址被改了", paras: []string{fmt.Sprintf("你在 %s 上的账号的邮箱地址被改了。如果不是你本人，请告诉管理员。", v.Site)}}
	case KindEmailAdded:
		return body{subject: "你的账号添加了一个邮箱", paras: []string{fmt.Sprintf("%s 被添加到你在 %s 上的账号。点开发到它的链接后它才生效。如果不是你本人，请删掉它并修改密码。", v.Email, v.Site)}}
	case KindEmailRemoved:
		return body{subject: "你的账号删掉了一个邮箱", paras: []string{fmt.Sprintf("%s 已从你在 %s 上的账号删除。如果不是你本人，请告诉管理员。", v.Email, v.Site)}}
	case KindEmailPrimary:
		return body{subject: "你账号的主邮箱改了", paras: []string{fmt.Sprintf("%s 现在是你在 %s 上的账号的主邮箱：登录用它，通知也发到它。如果不是你本人，请告诉管理员。", v.Email, v.Site)}}
	default:
		return body{subject: "测试邮件", paras: []string{fmt.Sprintf("这是来自 %s 的测试邮件，发信正常。", v.Site)}}
	}
}
