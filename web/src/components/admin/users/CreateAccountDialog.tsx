import { useRef, useState, type FormEvent } from 'react'
import { Copy, UserPlus } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { ApiError } from '@/api/client'
import { adminApi, useTenants } from '@/api/admin'
import { keys } from '@/api/hooks'
import type { CreatedAccount, Role } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { Spinner } from '@/components/common'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { useInScope } from '@/lib/scopedClient'
import { tc } from '@/i18n/console'

const LINK_DAYS = 7

// An administrator makes an account by hand (docs/api.md, "Creating an account"), which is the way in when the
// registration mode is closed. The administrator never learns a password: the answer carries a one-time link that sets it,
// shown here once. Across the site the tenant is chosen; inside a tenant's scope the request carries the scope.
export function CreateAccountDialog({ across, onClose }: { across: boolean; onClose: () => void }) {
  const qc = useQueryClient()
  const inScope = useInScope()
  const tenants = useTenants(across, true)
  const [username, setUsername] = useState('')
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<Role>('member')
  const [tenant, setTenant] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [made, setMade] = useState<CreatedAccount | null>(null)
  const list = tenants.data ?? []
  // The default tenant is the first, and is where the person lands until another is picked.
  const chosen = tenant || list.find((x) => x.default)?.id || ''

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy || username.trim() === '') return
    setBusy(true)
    setError('')
    try {
      const res = await inScope(() =>
        adminApi.createAccount({
          username: username.trim(),
          name: name.trim() || undefined,
          email: email.trim() || undefined,
          role,
          tenant: across && chosen ? chosen : undefined,
        }),
      )
      setMade(res)
      void qc.invalidateQueries({ queryKey: keys.users })
    } catch (err) {
      setError(err instanceof ApiError ? err.message : tc.users.createDialog.failed)
    } finally {
      setBusy(false)
    }
  }

  const d = tc.users.createDialog
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent sheet className="sm:max-w-md" data-slot="create-account-dialog">
        {made ? (
          <Made made={made} onClose={onClose} />
        ) : (
          <form onSubmit={(e) => void submit(e)} className="grid min-w-0 gap-4" noValidate>
            <DialogHeader>
              <DialogTitle>{d.title}</DialogTitle>
              <DialogDescription>{d.help}</DialogDescription>
            </DialogHeader>
            <Field
              label={d.username}
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              maxLength={32}
              requiredMark
            />
            <Field label={d.name} value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" />
            <Field
              label={d.email}
              type="email"
              inputMode="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              autoComplete="off"
            />
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div className="grid gap-1.5">
                <Label htmlFor="create-account-role">{d.role}</Label>
                <NativeSelect id="create-account-role" value={role} onChange={(e) => setRole(e.target.value as Role)}>
                  <option value="member">{tc.users.roleMember}</option>
                  <option value="admin">{tc.users.roleAdmin}</option>
                </NativeSelect>
              </div>
              {across && (
                <div className="grid gap-1.5">
                  <Label htmlFor="create-account-tenant">{d.tenant}</Label>
                  <NativeSelect
                    id="create-account-tenant"
                    value={chosen}
                    onChange={(e) => setTenant(e.target.value)}
                    disabled={tenants.isPending}
                  >
                    {list.map((x) => (
                      <option key={x.id} value={x.id}>
                        {x.default ? d.tenantDefault(x.name) : x.name}
                      </option>
                    ))}
                  </NativeSelect>
                </div>
              )}
            </div>
            <FormMessage>{error}</FormMessage>
            <DialogFooter>
              <Button type="button" variant="outline" className="h-10 sm:h-9" onClick={onClose}>
                {d.cancel}
              </Button>
              <Button type="submit" className="h-10 sm:h-9" disabled={busy || username.trim() === ''}>
                {busy ? <Spinner /> : <UserPlus aria-hidden="true" />}
                {busy ? d.creating : d.submit}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

// What came of it: the link to hand over (shown only now) and whether it was mailed too.
function Made({ made, onClose }: { made: CreatedAccount; onClose: () => void }) {
  const d = tc.users.createDialog
  const input = useRef<HTMLInputElement>(null)
  const [copied, setCopied] = useState(false)
  const [failed, setFailed] = useState(false)
  const copy = async () => {
    setFailed(false)
    try {
      await navigator.clipboard.writeText(made.link ?? '')
      setCopied(true)
    } catch {
      // No clipboard permission: select it so Ctrl+C works.
      input.current?.select()
      setFailed(true)
    }
  }
  const mail = made.mail === 'sent' ? d.mailSent(made.user.email) : made.mail === 'limited' ? d.mailLimited : d.mailNone
  return (
    <div className="grid min-w-0 gap-4">
      <DialogHeader>
        <DialogTitle>{d.title}</DialogTitle>
        <DialogDescription>{d.created(made.user.name || made.user.username)}</DialogDescription>
      </DialogHeader>
      {made.link ? (
        <div
          className="grid gap-2 rounded-lg border border-brand/40 bg-brand/5 p-3"
          role="group"
          aria-labelledby="created-link-title"
        >
          <h3 id="created-link-title" className="text-sm font-medium">
            {d.linkTitle}
          </h3>
          <p className="text-xs text-muted-foreground">{d.linkHelp(LINK_DAYS)}</p>
          <div className="flex flex-wrap gap-2">
            <Input
              ref={input}
              className="min-w-0 flex-1 basis-48 font-mono text-xs"
              readOnly
              value={made.link}
              aria-label={d.linkTitle}
              onFocus={(e) => e.currentTarget.select()}
            />
            <Button type="button" onClick={() => void copy()}>
              <Copy aria-hidden="true" />
              {copied ? d.copied : d.copy}
            </Button>
          </div>
          <span role="status" className="text-xs text-destructive">
            {failed ? d.copyFailed : ''}
          </span>
          <p className="text-xs text-muted-foreground" data-slot="created-mail">
            {mail}
          </p>
        </div>
      ) : (
        <p className="text-sm text-muted-foreground" data-slot="created-sso">
          {d.sso}
        </p>
      )}
      <DialogFooter>
        <Button type="button" className="h-10 sm:h-9" onClick={onClose}>
          {d.done}
        </Button>
      </DialogFooter>
    </div>
  )
}
