package domains

// publicDomains are the domains of mail services anyone may have an address
// at: no organisation may claim them, whatever its DNS says (a tenant that
// could would take every such address's sign-in). The list is the large
// services worldwide and in mainland China; it is not complete, and a domain
// missing from it still needs its DNS record to be claimed.
var publicDomains = map[string]bool{}

func init() {
	for _, d := range []string{
		// Google, Microsoft, Apple, Yahoo, AOL
		"gmail.com", "googlemail.com",
		"outlook.com", "outlook.cn", "outlook.jp", "outlook.de", "outlook.fr", "outlook.es", "outlook.it",
		"hotmail.com", "hotmail.co.uk", "hotmail.fr", "hotmail.de", "hotmail.it", "hotmail.es", "hotmail.co.jp",
		"live.com", "live.cn", "live.co.uk", "live.fr", "live.de", "live.jp", "msn.com", "passport.com",
		"icloud.com", "me.com", "mac.com",
		"yahoo.com", "yahoo.co.uk", "yahoo.co.jp", "yahoo.fr", "yahoo.de", "yahoo.es", "yahoo.it", "yahoo.ca",
		"yahoo.com.au", "yahoo.com.br", "yahoo.com.tw", "yahoo.com.hk", "yahoo.co.in", "ymail.com", "rocketmail.com",
		"aol.com", "aim.com", "verizon.net",
		// Mainland China
		"qq.com", "vip.qq.com", "foxmail.com", "163.com", "126.com", "yeah.net", "vip.163.com", "vip.126.com", "188.com",
		"sina.com", "sina.cn", "vip.sina.com", "sohu.com", "aliyun.com", "139.com", "189.cn", "wo.cn", "tom.com", "21cn.com",
		// Elsewhere in Asia
		"naver.com", "daum.net", "hanmail.net", "kakao.com", "nate.com", "rediffmail.com", "docomo.ne.jp", "ezweb.ne.jp",
		"softbank.ne.jp", "i.softbank.jp",
		// Europe and Russia
		"gmx.com", "gmx.net", "gmx.de", "gmx.at", "gmx.ch", "web.de", "t-online.de", "freenet.de", "mail.de",
		"yandex.ru", "yandex.com", "ya.ru", "mail.ru", "inbox.ru", "list.ru", "bk.ru", "rambler.ru",
		"orange.fr", "wanadoo.fr", "free.fr", "laposte.net", "sfr.fr", "libero.it", "virgilio.it", "tiscali.it",
		"btinternet.com", "sky.com", "virginmedia.com", "seznam.cz", "wp.pl", "o2.pl", "onet.pl", "interia.pl",
		// Privacy and paid services
		"proton.me", "protonmail.com", "protonmail.ch", "pm.me", "tutanota.com", "tutanota.de", "tuta.io", "tuta.com",
		"fastmail.com", "fastmail.fm", "hey.com", "zoho.com", "zohomail.com", "mail.com", "email.com", "inbox.com",
		"hushmail.com", "posteo.de", "mailbox.org", "runbox.com",
		// Internet providers in America
		"comcast.net", "att.net", "sbcglobal.net", "bellsouth.net", "cox.net", "charter.net", "earthlink.net", "optonline.net",
	} {
		publicDomains[d] = true
	}
}

// Public says whether a domain (a key) is a public mail service's.
func Public(key string) bool { return publicDomains[key] }
