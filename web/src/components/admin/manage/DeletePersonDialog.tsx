import { useState } from 'react'
import { Link } from 'react-router'
import { ApiError } from '@/api/client'
import { useDeleteUser, useUserDeletion } from '@/api/manage'
import type { RecycleItem, User } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
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
import { Textarea } from '@/components/ui/textarea'
import { formatBytes } from '@/lib/bytes'
import { absoluteTime } from '@/lib/time'
import { adminPath } from '@/lib/urls'
import { tg } from '@/i18n/manage'
import { manageError, UserPicker, useIsSite } from './shared'

// Deleting one person: what happens to each of their spaces, who inherits, what stops working; then their username
// typed, and where they went. The person goes to the recycle bin, never away.
export function DeletePersonDialog({ id, username, onClose }: { id: string; username: string; onClose: () => void }) {
  const site = useIsSite()
  const [heir, setHeir] = useState<User | null>(null)
  const plan = useUserDeletion(id, heir?.id ?? '')
  const del = useDeleteUser()
  const [typed, setTyped] = useState('')
  const [reason, setReason] = useState('')
  const [error, setError] = useState('')
  const [done, setDone] = useState<RecycleItem | null>(null)
  const p = plan.data
  const needs = p?.needsHeir ?? []
  const blockers = p?.blockers ?? []
  const ok =
    typed.trim().toLowerCase() === username.toLowerCase() && blockers.length === 0 && !(needs.length > 0 && !heir)

  const confirm = () => {
    if (!ok || del.isPending) return
    setError('')
    del.mutate(
      {
        id,
        ...(heir ? { heir: heir.id } : {}),
        ...(reason.trim() ? { reason: reason.trim() } : {}),
      },
      {
        onSuccess: setDone,
        onError: (e) => {
          if (e instanceof ApiError && e.code === 'heir_required') void plan.refetch()
          setError(manageError(e))
        },
      },
    )
  }
  // A personal space is the person's own: a tenant administrator is told that it goes, not what it is called or weighs.
  const spaceName = (s: { name: string; personal?: boolean }) =>
    s.personal && !site ? tg.del.user.personalHidden : s.name

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-auto sm:max-w-xl" data-slot="delete-person">
        {done ? (
          <div className="grid gap-4">
            <DialogHeader>
              <DialogTitle>{tg.del.user.deleted(username)}</DialogTitle>
              <DialogDescription>
                {tg.del.user.deletedHelp(absoluteTime(done.purgeAt), done.with.length)}
              </DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button type="button" variant="outline" render={<Link to={adminPath('recycle')} onClick={onClose} />}>
                {tg.del.space.goBin}
              </Button>
              <Button type="button" onClick={onClose}>
                {tg.bulk.close}
              </Button>
            </DialogFooter>
          </div>
        ) : (
          <form
            className="grid gap-4"
            onSubmit={(e) => {
              e.preventDefault()
              confirm()
            }}
          >
            <DialogHeader>
              <DialogTitle>{tg.del.user.title(username)}</DialogTitle>
              <DialogDescription>{p ? tg.del.daysLine(p.recycleDays) : tg.del.daysUnknown}</DialogDescription>
            </DialogHeader>
            <p className="text-sm text-muted-foreground">{tg.del.user.intro}</p>
            {plan.isPending ? (
              <PageFallback />
            ) : plan.isError && !p ? (
              <ErrorState
                message={manageError(plan.error, tg.del.user.loadFailed)}
                onRetry={() => void plan.refetch()}
              />
            ) : (
              p && (
                <div className="grid gap-3 text-sm" data-slot="user-plan">
                  <section className="grid gap-1" data-slot="plan-recycle">
                    <h4 className="font-medium">{tg.del.user.recycle}</h4>
                    {p.recycle.length === 0 ? (
                      <p className="text-muted-foreground">{tg.del.user.recycleNone}</p>
                    ) : (
                      <ul className="grid gap-0.5">
                        {p.recycle.map((r) => (
                          <li key={r.space.id}>
                            {spaceName(r.space)}
                            {(site || !r.space.personal) && (
                              <span className="text-muted-foreground"> · {formatBytes(r.bytes)}</span>
                            )}
                            {r.space.personal && site && (
                              <span className="text-muted-foreground"> · {tg.del.user.personal}</span>
                            )}
                          </li>
                        ))}
                      </ul>
                    )}
                  </section>
                  {p.transfer.length > 0 && (
                    <section className="grid gap-1" data-slot="plan-transfer">
                      <h4 className="font-medium">{tg.del.user.transfer}</h4>
                      <ul className="grid gap-1">
                        {p.transfer.map((t) => (
                          <li key={t.space.id}>
                            {t.space.name} →{' '}
                            {tg.del.user.transferTo(
                              t.to.username,
                              (tg.del.user.whys as Record<string, string>)[t.why] ?? t.why,
                            )}
                            {t.credential === 'user' && (
                              <p className="text-xs text-muted-foreground">{tg.del.user.credential}</p>
                            )}
                          </li>
                        ))}
                      </ul>
                    </section>
                  )}
                  {p.leave.length > 0 && (
                    <section className="grid gap-1" data-slot="plan-leave">
                      <h4 className="font-medium">{tg.del.user.leave}</h4>
                      <ul className="grid gap-0.5">
                        {p.leave.map((l) => (
                          <li key={l.space.id}>
                            {l.space.name}{' '}
                            <span className="text-muted-foreground">
                              · {tg.del.user.leaveRole((tg.del.user.roles as Record<string, string>)[l.role] ?? l.role)}
                            </span>
                          </li>
                        ))}
                      </ul>
                    </section>
                  )}
                  <section className="grid gap-0.5" data-slot="plan-revoke">
                    <h4 className="font-medium">{tg.del.user.revoke}</h4>
                    <p>
                      {tg.del.user.revokeLine(
                        p.revoke.tokens,
                        p.revoke.grants,
                        p.revoke.passkeys,
                        p.revoke.identities,
                        p.revoke.sessions,
                      )}
                    </p>
                    {p.revoke.github && <p className="text-xs text-muted-foreground">{tg.del.user.github}</p>}
                    {p.groups > 0 && <p className="text-xs text-muted-foreground">{tg.del.user.groups(p.groups)}</p>}
                  </section>
                  {(needs.length > 0 || heir) && (
                    <section className="grid gap-2" data-slot="plan-heir">
                      {needs.length > 0 && (
                        <p role="alert" className="text-destructive">
                          {tg.del.user.needsHeir}：{needs.map((n) => n.name).join('、')}
                        </p>
                      )}
                      <UserPicker label={tg.del.user.heir} value={heir} onChange={setHeir} exclude={[id]} />
                      <p className="text-xs text-muted-foreground">{tg.del.user.heirHelp}</p>
                    </section>
                  )}
                  {blockers.length > 0 && (
                    <ul role="alert" className="grid gap-1 text-destructive" data-slot="blockers">
                      {blockers.map((b) => (
                        <li key={b.reason}>
                          {(tg.del.user.blockers as Record<string, string>)[b.reason] ?? b.message ?? b.reason}
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              )
            )}
            <div className="grid gap-1.5">
              <Label htmlFor="delete-person-reason">{tg.del.user.reason}</Label>
              <Textarea
                id="delete-person-reason"
                rows={2}
                maxLength={500}
                value={reason}
                onChange={(e) => setReason(e.target.value)}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="delete-person-confirm">{tg.del.user.type}</Label>
              <code className="rounded-md bg-muted px-2 py-1 text-sm break-all select-all">{username}</code>
              <Input
                id="delete-person-confirm"
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                autoComplete="off"
                spellCheck={false}
              />
            </div>
            <FormMessage>{error}</FormMessage>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={onClose}>
                {tg.cancel}
              </Button>
              <Button type="submit" variant="destructive" disabled={!ok || del.isPending || plan.isPending}>
                {del.isPending && <Spinner />}
                {tg.del.user.confirm}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
