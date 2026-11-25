package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/settings"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// The daily text's settings on the layered mechanism: daily.own and daily.api
// are the 2.3 switches of what a space may do, and daily.default is the
// default of /api/admin/daily. The site's value of each stays in the daily
// text's own record (so /api/admin/daily and /api/admin/settings are one
// thing); the tenant layer, and the locks, are the new mechanism's.

func (s *server) bindDailySettings() {
	flag := func(pick func(*dailyAllow) *bool) settings.SiteBinding {
		return settings.SiteBinding{
			Get: func(ctx context.Context, q store.Q) (any, error) {
				d, err := s.loadSiteQ(ctx, q)
				if err != nil {
					return nil, err // d is empty then (a context that ended, say)
				}
				return *pick(d.Allow), nil
			},
			Set: func(ctx context.Context, q store.Q, v any) error {
				return s.editSiteQ(ctx, q, func(_ store.Q, d *siteDaily) error {
					*pick(d.Allow) = v.(bool)
					return nil
				})
			},
		}
	}
	s.settings.Bind(settings.DailyOwn, flag(func(a *dailyAllow) *bool { return &a.Own }))
	s.settings.Bind(settings.DailyAPI, flag(func(a *dailyAllow) *bool { return &a.API }))
	s.settings.Bind(settings.DailyDefault, settings.SiteBinding{
		Get: func(ctx context.Context, q store.Q) (any, error) {
			d, err := s.loadSiteQ(ctx, q)
			if err != nil || d.Default == nil {
				return nil, err
			}
			return toGeneric(d.Default)
		},
		Set: func(ctx context.Context, q store.Q, v any) error {
			def, err := fromGeneric(v)
			if err != nil {
				return err
			}
			return s.editSiteQ(ctx, q, func(_ store.Q, d *siteDaily) error {
				if def != nil && def.Kind == "api" {
					if err := checkDefaultAPIs(def, d); err != nil {
						return err
					}
				}
				d.Default = def
				return nil
			})
		},
		Check: func(ctx context.Context, tenant string, v any) (any, error) {
			def, err := fromGeneric(v)
			if err != nil || def == nil {
				return nil, err
			}
			site, err := s.loadSite(ctx)
			if err != nil {
				return nil, err
			}
			if tenant != "" {
				// A tenant chooses a space of its own or one of the interfaces; the
				// pasted lines are the site's.
				if def.Kind != "space" && def.Kind != "api" && def.Kind != "sources" {
					return nil, &settings.Error{Code: settings.CodeInvalid, Msg: "A tenant's default is a space of the tenant, an interface, or sources of those."}
				}
				if def.Kind == "sources" {
					// The pasted lines are the site's; a space has to be one of the tenant's.
					for _, list := range def.Sources {
						for _, src := range list {
							if src.Kind == "list" {
								return nil, &settings.Error{Code: settings.CodeInvalid, Msg: "The lines are the site's: a tenant's sources are interfaces and spaces of the tenant."}
							}
							if src.Kind == "space" {
								if _, err := store.GetSpace(ctx, s.opt.Store.Q(), store.InTenant(tenant), src.Space); err != nil {
									if err == store.ErrNotFound {
										return nil, &settings.Error{Code: settings.CodeInvalid, Msg: "There is no such space in this tenant."}
									}
									return nil, err
								}
							}
						}
					}
				}
				if def.Kind == "space" {
					if !page.ValidID(def.Space) {
						return nil, &settings.Error{Code: settings.CodeInvalid, Msg: "A default that is a space needs the space and a source (folder, tag or type)."}
					}
					if _, err := store.GetSpace(ctx, s.opt.Store.Q(), store.InTenant(tenant), def.Space); err != nil {
						if err == store.ErrNotFound {
							return nil, &settings.Error{Code: settings.CodeInvalid, Msg: "There is no such space in this tenant."}
						}
						return nil, err
					}
				}
			}
			if err := s.checkSiteDefault(ctx, def, &site); err != nil {
				return nil, asSettingError(err)
			}
			return toGeneric(def)
		},
	})
}

// asSettingError turns the daily text's refusals into the settings package's.
func asSettingError(err error) error {
	var inv *space.InvalidError
	if errors.As(err, &inv) {
		return &settings.Error{Code: settings.CodeInvalid, Msg: inv.Msg}
	}
	return err
}

func toGeneric(d *siteDefault) (any, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	var v any
	return v, json.Unmarshal(raw, &v)
}

func fromGeneric(v any) (*siteDefault, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var d siteDefault
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, &settings.Error{Code: settings.CodeInvalid, Msg: fmt.Sprint("default: ", err)}
	}
	return &d, nil
}

// dailyAllowFor is what a person's tenant lets spaces do with the daily text:
// 2.3's switches after the tenant layer (the site's own value when the tenant
// follows it, which is always so with one tenant).
func (s *server) dailyAllowFor(ctx context.Context, tenantID string) (dailyAllow, error) {
	own, err := s.settings.Get(ctx, tenantID, settings.DailyOwn)
	if err != nil {
		return dailyAllow{}, err
	}
	api, err := s.settings.Get(ctx, tenantID, settings.DailyAPI)
	if err != nil {
		return dailyAllow{}, err
	}
	o, _ := own.Value.(bool)
	a, _ := api.Value.(bool)
	return dailyAllow{Own: o, API: a}, nil
}

// dailyDefaultFor is the default of the daily text for a tenant: the tenant's
// own when it has one in effect, else the site's. scope names whose it is
// ("tenant:<id>" or "site"), for what is chosen in turn among several
// interfaces.
func (s *server) dailyDefaultFor(ctx context.Context, tenantID string, site *siteDefault) (*siteDefault, string, error) {
	e, err := s.settings.Get(ctx, tenantID, settings.DailyDefault)
	if err != nil {
		return nil, "", err
	}
	if e.Tenant != nil && e.Tenant.InEffect && e.Tenant.Value != nil {
		def, err := fromGeneric(e.Tenant.Value)
		return def, "tenant:" + tenantID, err
	}
	return site, "site", nil
}
