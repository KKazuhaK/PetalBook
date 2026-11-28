package settings

import "encoding/json"

// CaptchaTenantContexts are the captcha forms a tenant may ask a captcha of:
// the ones whose request is scoped to a tenant before the form is answered.
// The invitation (the invite is looked up only after the captcha) and the
// first-run setup belong to the site alone.
var CaptchaTenantContexts = []string{"login", "forgot", "register"}

type captchaCtx struct {
	On       bool
	When     string
	Failures int
}

// captchaCtxs decodes a captcha.contexts document; a missing field is the
// default (off, after failures, 3).
func captchaCtxs(v any) map[string]captchaCtx {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var in map[string]*struct {
		On       *bool    `json:"on"`
		When     *string  `json:"when"`
		Failures *float64 `json:"failures"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	out := make(map[string]captchaCtx, len(in))
	for k, x := range in {
		c := captchaCtx{When: "after_failures", Failures: 3}
		if x != nil {
			if x.On != nil {
				c.On = *x.On
			}
			if x.When != nil {
				c.When = *x.When
			}
			if x.Failures != nil {
				c.Failures = int(*x.Failures)
			}
		}
		out[k] = c
	}
	return out
}

// captchaLooser says whether the captcha document v is looser than above, form
// by form: a form that is on above is looser off; when it is on in both, the
// one that asks always is stricter than the one that asks after failures, and
// of two that ask after failures the one with fewer failures is. A form that
// is off above leaves v free; a form v leaves out is the default (off).
func captchaLooser(v, above any) bool {
	a, b := captchaCtxs(above), captchaCtxs(v)
	for _, name := range CaptchaTenantContexts {
		ac, ok := a[name]
		if !ok || !ac.On {
			continue
		}
		bc, ok := b[name]
		if !ok {
			bc = captchaCtx{When: "after_failures", Failures: 3}
		}
		switch {
		case !bc.On:
			return true
		case ac.When == "always":
			if bc.When != "always" {
				return true
			}
		case bc.When != "always" && bc.Failures > ac.Failures:
			return true
		}
	}
	return false
}
