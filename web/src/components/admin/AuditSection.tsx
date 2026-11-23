import { useState, type FormEvent } from 'react'
import { useAuditLog } from '@/api/admin'
import type { AuditItem } from '@/api/types'
import { ErrorState } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n'
import { tsec } from '@/i18n/security'

const ta = tsec.audit
const KINDS = ['login', 'login_failed', 'rate_limited', 'lockout', 'passkey_clone', 'identity_linked', 'identity_unlinked', 'passkey_added', 'passkey_removed', 'security_changed', 'provider_created', 'provider_changed', 'provider_deleted', 'user_changed', 'dropped']

const show = (v: unknown): string => (typeof v === 'string' ? v : JSON.stringify(v))

// A row's detail as short `key=value` pairs; a nested object (what a change was from and to) is flattened one level.
export function detailText(detail: Record<string, unknown>): string {
  const parts: string[] = []
  for (const [k, v] of Object.entries(detail ?? {})) {
    if (v !== null && typeof v === 'object' && !Array.isArray(v)) for (const [k2, v2] of Object.entries(v as Record<string, unknown>)) parts.push(`${k}.${k2}=${show(v2)}`)
    else parts.push(`${k}=${show(v)}`)
  }
  return parts.join(' ')
}

// The audit log: sign-ins and the changes made to the security settings, newest first, filtered by kind and person.
export function AuditSection() {
  const [kind, setKind] = useState('')
  const [user, setUser] = useState('')
  const [userDraft, setUserDraft] = useState('')
  const log = useAuditLog(kind, user)
  const rows = log.data?.pages.flatMap((p) => p.items) ?? []
  const apply = (e: FormEvent) => {
    e.preventDefault()
    setUser(userDraft.trim())
  }
  return (
    <SettingsSection title={ta.title} description={ta.help} id="security-audit">
      <form className="grid grid-cols-1 gap-3 sm:flex sm:flex-wrap sm:items-end" onSubmit={apply}>
        <div className="grid min-w-0 gap-1.5 sm:w-56">
          <Label htmlFor="audit-kind">{ta.kind}</Label>
          <NativeSelect id="audit-kind" value={kind} onChange={(e) => setKind(e.target.value)}>
            <option value="">{ta.allKinds}</option>
            {KINDS.map((k) => (
              <option key={k} value={k}>
                {ta.kinds[k] ?? k}
              </option>
            ))}
          </NativeSelect>
        </div>
        <div className="grid min-w-0 gap-1.5 sm:w-56">
          <Label htmlFor="audit-user">{ta.user}</Label>
          <Input id="audit-user" value={userDraft} placeholder={ta.userPlaceholder} onChange={(e) => setUserDraft(e.target.value)} onBlur={() => setUser(userDraft.trim())} autoComplete="off" spellCheck={false} />
        </div>
        <Button type="button" variant="outline" onClick={() => void log.refetch()}>
          {ta.refresh}
        </Button>
      </form>
      {log.isPending ? (
        <div aria-busy="true" className="h-16 animate-pulse rounded-lg bg-muted" />
      ) : log.isError ? (
        <ErrorState message={t.fmt.labelled(ta.loadFailed, log.error.message)} onRetry={() => void log.refetch()} />
      ) : rows.length === 0 ? (
        <p className="text-sm text-muted-foreground">{ta.empty}</p>
      ) : (
        <ul className="divide-y rounded-lg border" data-slot="audit-rows">
          {rows.map((r) => (
            <Row key={r.id} r={r} />
          ))}
        </ul>
      )}
      {log.hasNextPage && (
        <div>
          <Button type="button" variant="outline" disabled={log.isFetchingNextPage} onClick={() => void log.fetchNextPage()}>
            {log.isFetchingNextPage ? ta.loading : ta.loadMore}
          </Button>
        </div>
      )}
    </SettingsSection>
  )
}

function Row({ r }: { r: AuditItem }) {
  const who = [r.username, r.user && r.user !== r.username ? r.user : '', r.actor ? ta.actor(r.actor) : r.detail?.by === 'cli' ? ta.cli : ''].filter(Boolean).join(' · ')
  return (
    <li className="grid min-w-0 grid-cols-1 gap-1 px-3 py-2.5 text-sm" data-audit={r.id}>
      <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
        <Badge variant="secondary">{ta.kinds[r.kind] ?? r.kind}</Badge>
        <time dateTime={r.at} className="text-xs text-muted-foreground">
          {absoluteTime(r.at)}
        </time>
        {r.ip && <span className="text-xs break-all text-muted-foreground">{r.ip}</span>}
      </div>
      {who && <div className="text-xs break-all">{who}</div>}
      {Object.keys(r.detail ?? {}).length > 0 && <div className="font-mono text-xs break-all text-muted-foreground">{detailText(r.detail)}</div>}
    </li>
  )
}
