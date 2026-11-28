import { useState, type ReactNode } from 'react'
import { Fingerprint, KeyRound, MoreHorizontal, Pencil, Trash2 } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '@/api/client'
import { keys, useAuthProviders } from '@/api/hooks'
import type { AccountInfo, AccountMethod, Passkey, PublicProvider } from '@/api/types'
import { ProviderIcon } from '@/components/auth/ProviderIcon'
import { FormMessage } from '@/components/auth/Field'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { absoluteTime } from '@/lib/time'
import { toast } from 'sonner'
import { t } from '@/i18n'
import { ts } from '@/i18n/signin'
import { AddPasskey, usePasskeyActions } from './PasskeysSection'
import { SettingsSection } from './SettingsParts'

// One row's action: the same in the table (an icon in a box) and in the card's menu (an icon and its words).
interface Action {
  id: string
  label: string
  // What a screen reader hears for the table's icon button: the action and the method.
  aria: string
  icon: ReactNode
  onClick: () => void
  disabled?: boolean
  destructive?: boolean
}

// What one row says of a method, however it is laid out.
interface Row {
  key: string
  type: AccountMethod['type']
  // Passkeys only: the id the passkey tests and the page address the row by.
  passkey?: Passkey
  title: string
  // The identity's label, or what the password is.
  detail: string
  icon: ReactNode
  badge?: string
  badgeHelp?: string
  added: string
  addedLabel: (date: string) => string
  lastUsed: string | null
  // Why the row cannot be removed, when it cannot.
  note?: string
  actions: Action[]
}

// The account's one list of sign-in methods (docs/api.md, "GET /api/account"): the password, each linked sign-in
// and each passkey, with when each was added and last used and what can be done with it. From 560px it is a table;
// under that each method is a card with its actions in a menu. Below it, how to add one: link a provider, add a
// passkey, set a password.
export function SignInMethods({ account, onPassword }: { account: AccountInfo; onPassword: () => void }) {
  const qc = useQueryClient()
  const providers = useAuthProviders()
  const passkeyActions = usePasskeyActions(account)
  const [removing, setRemoving] = useState<AccountMethod | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const methods = account.methods ?? []
  const passkeys = new Map((account.passkeys ?? []).map((p) => [p.id, p]))
  const publicProviders = providers.data?.providers ?? []
  // A button for each enabled provider the person has no linked sign-in at: one per provider.
  const linked = new Set(methods.filter((m) => m.type === 'identity').map((m) => m.providerSlug))
  const linkable = publicProviders.filter((p) => !linked.has(p.slug))

  const link = async (p: PublicProvider) => {
    setError('')
    try {
      const { url } = await api.startLink(p.slug, '/settings')
      window.location.assign(url)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t.auth.failed)
    }
  }

  const unlink = async () => {
    if (!removing?.id) return
    setBusy(true)
    try {
      await api.removeLink(removing.id)
      void qc.invalidateQueries({ queryKey: keys.account })
      void qc.invalidateQueries({ queryKey: keys.accountLinks })
      setRemoving(null)
    } catch (err) {
      setRemoving(null)
      // 409: it is the only way in.
      if (err instanceof ApiError && err.code === 'last_method') setError(t.settings.onlyWay)
      else toast.error(err instanceof ApiError ? err.message : t.auth.failed)
      void qc.invalidateQueries({ queryKey: keys.account })
    }
    setBusy(false)
  }

  const rows: Row[] = methods.map((m) => {
    if (m.type === 'password') {
      return {
        key: 'password',
        type: m.type,
        title: ts.methods.password,
        detail: ts.methods.passwordHelp,
        icon: <KeyRound aria-hidden="true" className="size-4" />,
        added: m.added,
        addedLabel: ts.methods.changed,
        lastUsed: m.lastUsed,
        actions: [
          {
            id: 'password',
            label: t.settings.changePassword,
            aria: t.settings.changePassword,
            icon: <KeyRound aria-hidden="true" />,
            onClick: onPassword,
          },
        ],
      }
    }
    if (m.type === 'identity') {
      const icon = publicProviders.find((p) => p.slug === m.providerSlug)?.icon ?? ''
      const name = m.provider || ts.methods.identity
      return {
        key: `identity:${m.id}`,
        type: m.type,
        title: name,
        detail: m.label ?? '',
        icon: <ProviderIcon icon={icon} />,
        added: m.added,
        addedLabel: ts.methods.added,
        lastUsed: m.lastUsed,
        note: m.removable ? undefined : ts.methods.onlyWay,
        actions: [
          {
            id: 'remove',
            label: t.settings.remove,
            aria: t.fmt.labelled(t.settings.remove, m.label || name),
            icon: <Trash2 aria-hidden="true" />,
            onClick: () => {
              setError('')
              setRemoving(m)
            },
            disabled: !m.removable,
            destructive: true,
          },
        ],
      }
    }
    const name = m.name ?? ts.methods.passkey
    const passkey = m.id ? passkeys.get(m.id) : undefined
    return {
      key: `passkey:${m.id}`,
      type: m.type,
      passkey,
      title: name,
      detail: ts.methods.passkey,
      icon: <Fingerprint aria-hidden="true" className="size-4" />,
      badge: m.synced ? ts.passkeys.synced : undefined,
      badgeHelp: ts.passkeys.syncedHelp,
      added: m.added,
      addedLabel: ts.methods.added,
      lastUsed: m.lastUsed,
      note: m.removable ? undefined : ts.methods.onlyWay,
      actions: passkey
        ? [
            {
              id: 'rename',
              label: ts.passkeys.rename,
              aria: t.fmt.labelled(ts.passkeys.rename, name),
              icon: <Pencil aria-hidden="true" />,
              onClick: () => passkeyActions.rename(passkey),
            },
            {
              id: 'delete',
              label: ts.passkeys.remove,
              aria: t.fmt.labelled(ts.passkeys.remove, name),
              icon: <Trash2 aria-hidden="true" />,
              onClick: () => passkeyActions.remove(passkey),
              disabled: !m.removable,
              destructive: true,
            },
          ]
        : [],
    }
  })

  const lastUsed = (r: Row) => (r.lastUsed ? absoluteTime(r.lastUsed) : ts.methods.neverUsed)

  return (
    <SettingsSection title={ts.methods.title} id="methods-heading" description={ts.methods.help}>
      {account.user.passkeyRequired && <FormMessage tone="info">{ts.passkeys.required}</FormMessage>}
      {/* The layout follows the width of this box, not the window: the settings dialog is narrower than the window. In a box under 560px each method is a card of its own, its actions in a menu. */}
      <div className="@container/methods min-w-0">
        <ul
          className="divide-y rounded-lg border @min-[560px]/methods:hidden"
          aria-label={ts.methods.title}
          data-slot="methods-cards"
        >
          {rows.map((r) => (
            <li key={r.key} className="flex items-start gap-2 px-3 py-2.5" data-method={r.key}>
              <span className="mt-0.5 shrink-0 text-muted-foreground">{r.icon}</span>
              <div className="min-w-0 flex-1">
                <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
                  <span className="text-sm font-medium [overflow-wrap:anywhere]">{r.title}</span>
                  {r.badge && (
                    <Badge variant="secondary" title={r.badgeHelp}>
                      {r.badge}
                    </Badge>
                  )}
                </div>
                {r.detail && <div className="text-xs [overflow-wrap:anywhere] text-muted-foreground">{r.detail}</div>}
                <div className="text-xs text-muted-foreground">{r.addedLabel(absoluteTime(r.added))}</div>
                <div className="text-xs text-muted-foreground">
                  {r.lastUsed ? ts.methods.lastUsed(absoluteTime(r.lastUsed)) : ts.methods.neverUsed}
                </div>
                {r.note && <div className="mt-1 text-xs text-muted-foreground">{r.note}</div>}
              </div>
              {r.actions.length > 0 && <CardMenu row={r} />}
            </li>
          ))}
        </ul>
        <div className="hidden @min-[560px]/methods:block" data-slot="methods-table">
          <table className="w-full table-fixed border-collapse overflow-hidden rounded-lg border text-sm">
            <caption className="sr-only">{ts.methods.title}</caption>
            <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th scope="col" className="px-3 py-2 font-medium">
                  {ts.methods.columns.method}
                </th>
                <th scope="col" className="w-36 px-3 py-2 font-medium">
                  {ts.methods.columns.added}
                </th>
                <th scope="col" className="w-36 px-3 py-2 font-medium">
                  {ts.methods.columns.lastUsed}
                </th>
                <th scope="col" className="w-24 px-3 py-2 text-right font-medium">
                  <span className="sr-only">{ts.methods.columns.actions}</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {rows.map((r) => (
                <tr key={r.key} data-method={r.key} data-passkey={r.passkey?.id}>
                  <td className="px-3 py-2.5 align-top">
                    <div className="flex min-w-0 items-start gap-2">
                      <span className="mt-0.5 shrink-0 text-muted-foreground">{r.icon}</span>
                      <div className="min-w-0">
                        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
                          <span className="font-medium [overflow-wrap:anywhere]">{r.title}</span>
                          {r.badge && (
                            <Badge variant="secondary" title={r.badgeHelp}>
                              {r.badge}
                            </Badge>
                          )}
                        </div>
                        {r.detail && (
                          <div className="text-xs [overflow-wrap:anywhere] text-muted-foreground">{r.detail}</div>
                        )}
                        {r.note && <div className="mt-1 text-xs text-muted-foreground">{r.note}</div>}
                      </div>
                    </div>
                  </td>
                  <td className="px-3 py-2.5 align-top text-xs text-muted-foreground">{absoluteTime(r.added)}</td>
                  <td className="px-3 py-2.5 align-top text-xs text-muted-foreground">{lastUsed(r)}</td>
                  <td className="px-3 py-2.5 align-top">
                    <div className="flex justify-end gap-1">
                      {r.actions.map((a) => (
                        <Button
                          key={a.id}
                          type="button"
                          variant="outline"
                          size="icon-sm"
                          aria-label={a.aria}
                          title={a.label}
                          disabled={a.disabled}
                          onClick={a.onClick}
                        >
                          {a.icon}
                        </Button>
                      ))}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
      <FormMessage>{error || passkeyActions.error}</FormMessage>
      <div className="grid gap-3 border-t pt-3" data-slot="methods-add">
        <h4 className="text-sm font-medium">{ts.methods.addTitle}</h4>
        {!account.hasPassword && (
          <div className="grid gap-1">
            <p className="text-xs text-muted-foreground">{t.settings.setHelp}</p>
            <Button type="button" variant="outline" className="self-start" onClick={onPassword}>
              <KeyRound aria-hidden="true" />
              {t.settings.setPassword}
            </Button>
          </div>
        )}
        {linkable.length > 0 && (
          <div className="grid grid-cols-1 gap-2 sm:max-w-sm">
            {linkable.map((p) => (
              <Button
                key={`${p.kind}/${p.slug}`}
                type="button"
                variant="outline"
                className="w-full min-w-0 justify-start gap-2"
                onClick={() => void link(p)}
              >
                <ProviderIcon icon={p.icon} />
                <span className="min-w-0 truncate">{ts.identities.link(p.name)}</span>
              </Button>
            ))}
          </div>
        )}
        <AddPasskey account={account} />
      </div>
      {passkeyActions.dialogs}
      <AlertDialog open={removing !== null} onOpenChange={(open) => !open && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t.settings.removeTitle}</AlertDialogTitle>
            <AlertDialogDescription>
              {removing ? t.settings.removeHelp(removing.label || removing.provider || '') : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t.settings.cancel}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" disabled={busy} onClick={() => void unlink()}>
              {t.settings.removeConfirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsSection>
  )
}

// A card's actions: one menu, a method's actions as its items.
function CardMenu({ row }: { row: Row }) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button
            type="button"
            variant="outline"
            size="icon-sm"
            aria-label={ts.methods.actionsFor(row.title)}
            className="shrink-0"
          />
        }
      >
        <MoreHorizontal aria-hidden="true" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-auto min-w-40">
        {row.actions.map((a) => (
          <DropdownMenuItem
            key={a.id}
            disabled={a.disabled}
            variant={a.destructive ? 'destructive' : undefined}
            aria-label={a.aria}
            onClick={a.onClick}
          >
            {a.icon}
            {a.label}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
