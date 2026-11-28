# Owner decisions of 2026-11-27

## 18. The registration mode (open, invite, closed)

**Decision (final).** `features.registration`, a boolean that governed only
password self-registration, is replaced by a three-way setting,
`registration.mode`: `open`, `invite` or `closed`.

- **Two separate scopes.**
  - The site's value governs **personal accounts only**: people who belong to no
    organisation, that is the default tenant. With tenancy off, everything is the
    default tenant.
  - Each organisation (a tenant that is not the default) has **its own value**, set
    by its administrators. It is **not bounded by the site's value**: it is not
    tighten-only. It still follows the settings machinery, so a site administrator
    may set a rule for one tenant, as for any setting. An organisation's mode
    governs every way an account is made into it: its own SSO, the site's social
    sign-in that lands in it, invites into it, and its domain joins.
  - An organisation stays bounded by `quota.people` (set per tenant by the site
    administrator). Every way into a tenant is held to it, in one place
    (`CreateUser`), refused as `409 people_limit` (`?sso_error=people_limit`).
    Before this decision the quota only warned.
- **What each mode means.**
  - `open`: any way may make an account. Password self-sign-up still needs mail and
    the existing `registration.domains` and `registration.perHour` rules.
  - `invite`: only someone holding a valid invite link makes an account, by
    password or by any external sign-in. For an organisation, a domain's join mode
    (`auto` or `request`) is its administrators' own explicit opt-in and keeps
    working under `invite`. The refusal is `403 invite_required`.
  - `closed`: nothing makes an account. Invite links stop working too, so accepting
    one answers `403 registration_closed`. Domain joins are off (no automatic joins,
    no new requests); requests already pending stay approvable, because approval is
    an administrator's act. First-administrator setup is exempt. Direct creation of
    accounts by an administrator comes in the next step; for now closed simply
    creates nothing.
- **Defaults.** Invites worked while registration was closed, so the old behaviour
  is `invite`: both the site default and the organisation default are `invite`.
- **The per-provider `autoCreate` switch is gone**, with the column, the views and
  inputs, and the web form. So is **GitHub's organisation allowlist**
  (`auto_create_needs_org`, `organizations` as a gate); GitHub organisations are
  read only for the groups claim, and `read:org` is asked only when groups are on.
  Whether an external sign-in makes an account is now decided only by the mode of
  the tenant the account would land in:
  1. the join tenant, when the address is verified and on a joining domain (a site
     social provider only);
  2. else the provider's own tenant (a tenant's own provider: that tenant; a site
     provider: its jit `tenant`);
  3. else the default tenant, that is the site's value.
  Existing accounts still match by link (`provider_id`, `subject`) or by a trusted
  verified email, exactly as before.
- **A tenant that wants its SSO to make accounts** sets `open` together with
  `signin.ssoOnly`: password sign-up is then refused (`sso_required`) while the SSO
  creates. `sso_only_with_registration` no longer blocks this; it stays only for
  `features.orgCreation`, which needs a password sign-up.
- **Organisation creation is unrelated to the mode.** `features.orgCreation`
  (default off) stays as it is; a self-made organisation is an ordinary tenant.
- **Error codes.** New: `invite_required` (mode `invite`, no invitation) and
  `people_limit`. `registration_closed` is kept for `closed`. Both mode codes are
  `403` on the REST registration endpoints and `?sso_error=` values on external
  sign-in. `no_account` now means only that setup is not done or the tenant is
  disabled. `auto_create_needs_org` is removed.

**Where the deciding tenant was a choice of the implementation**

- A verified address on a joining domain whose organisation is `closed`: the join is
  off, so the sign-in falls to the provider's own tenant (the default tenant for a
  social provider), whose mode then decides. The password path does the same with
  the tenant the page named.
- An organisation's effective mode is its own value, else the site administrator's
  rule for it, else `invite`: the site's own value is for the default tenant only
  and never flows into organisations (the setting has a `TenantDefault`).
- Quota checks run after the account is counted, inside its transaction (the
  usage row is locked by the count), so parallel accounts cannot pass the cap
  together on PostgreSQL. A person restored from the recycle bin is held to it too.
- On the code-hosting provider, `groups.allow` and the group map are read from the
  person's organisations (`read:org` when either is set): that is how access is cut
  off when someone leaves the company's organisation, now that the organisation
  allowlist is gone.
- The default tenant's value is always the site's: a tenant-layer value for the
  default tenant is not read.
- A join request waiting while its organisation turns `closed` is not cancelled
  (only registrations still unconfirmed are); its approval is an administrator's act.
