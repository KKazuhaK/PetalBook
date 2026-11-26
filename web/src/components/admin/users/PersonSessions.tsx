import { useState } from 'react'
import { LogOut } from 'lucide-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { adminApi } from '@/api/admin'
import { keys } from '@/api/hooks'
import type { PersonSession, User } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { SettingsSection } from '@/components/settings/SettingsParts'
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
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n'
import { tc } from '@/i18n/console'

// The browser and system a user agent names, in a few words: "Firefox · Linux". Anything it cannot read is shown as it
// is (cut), because the administrator may know better than the parser.
export function deviceOf(ua: string): string {
  if (!ua) return ''
  const browser = /Edg\//.test(ua)
    ? 'Edge'
    : /OPR\/|Opera/.test(ua)
      ? 'Opera'
      : /Firefox\//.test(ua)
        ? 'Firefox'
        : /Chrome\//.test(ua)
          ? 'Chrome'
          : /Safari\//.test(ua)
            ? 'Safari'
            : /curl\//.test(ua)
              ? 'curl'
              : ''
  const system = /Windows/.test(ua)
    ? 'Windows'
    : /iPhone|iPad/.test(ua)
      ? 'iOS'
      : /Android/.test(ua)
        ? 'Android'
        : /Mac OS X|Macintosh/.test(ua)
          ? 'macOS'
          : /Linux|X11/.test(ua)
            ? 'Linux'
            : ''
  if (!browser && !system) return ua.length > 60 ? ua.slice(0, 60) + '…' : ua
  return [browser, system].filter(Boolean).join(' · ')
}

export function methodText(method: string): string {
  const m = tc.person.sessions.methods
  if (method.startsWith('sso:')) return tc.person.sessions.sso(method.slice(4))
  return method === 'password' ||
    method === 'passkey' ||
    method === 'invite' ||
    method === 'registration' ||
    method === 'setup'
    ? m[method]
    : m.unknown
}

// A site administrator's look at where a person is signed in: end one device or all of them.
export function PersonSessions({ u }: { u: User }) {
  const qc = useQueryClient()
  const key = [...keys.users, 'sessions', u.id]
  const list = useQuery({ queryKey: key, queryFn: ({ signal }) => adminApi.userSessions(u.id, signal) })
  const [ending, setEnding] = useState<PersonSession | 'all' | null>(null)
  const [error, setError] = useState('')
  const end = useMutation({
    mutationFn: async (what: PersonSession | 'all') =>
      what === 'all' ? adminApi.endUserSessions(u.id) : adminApi.endUserSession(u.id, what.id),
    onSuccess: (r) => {
      toast.success(r && 'ended' in r ? tc.person.sessions.endedAll(r.ended) : tc.person.sessions.ended)
      void qc.invalidateQueries({ queryKey: key })
    },
    onError: (e) => setError(e instanceof ApiError ? e.message : tc.person.sessions.failed),
    onSettled: () => setEnding(null),
  })
  const sessions = list.data?.sessions ?? []
  const nameOf = (s: PersonSession) => deviceOf(s.userAgent) || tc.person.sessions.unknownDevice
  return (
    <SettingsSection title={tc.person.sessions.title} id="person-sessions" description={tc.person.sessions.help}>
      {list.isPending ? (
        <div aria-busy="true" className="h-16 animate-pulse rounded-lg bg-muted" />
      ) : list.isError ? (
        <FormMessage>{t.fmt.labelled(tc.person.sessions.failed, list.error.message)}</FormMessage>
      ) : sessions.length === 0 ? (
        <p className="text-sm text-muted-foreground">{tc.person.sessions.none}</p>
      ) : (
        <>
          <ul className="divide-y rounded-lg border" data-slot="sessions">
            {sessions.map((s) => (
              <li
                key={s.id}
                className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-2 px-3 py-2.5 transition-colors hover:bg-muted/40"
                data-session={s.id}
              >
                <div className="min-w-0 flex-1 basis-56">
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-sm font-medium">{nameOf(s)}</span>
                    {s.current && <Badge>{tc.person.sessions.thisDevice}</Badge>}
                    {s.held && <Badge variant="outline">{tc.person.sessions.held}</Badge>}
                  </div>
                  <div className="text-xs break-words text-muted-foreground">
                    {methodText(s.method)} · {tc.person.sessions.started} {absoluteTime(s.created)} ·{' '}
                    {tc.person.sessions.lastUsed} {absoluteTime(s.lastUsed)}
                  </div>
                  {s.userAgent && (
                    <div className="truncate text-xs text-muted-foreground/80" title={s.userAgent}>
                      {s.userAgent}
                    </div>
                  )}
                </div>
                <Button
                  type="button"
                  variant="outline"
                  className="h-10"
                  aria-label={tc.person.sessions.endFor(nameOf(s))}
                  onClick={() => setEnding(s)}
                >
                  <LogOut aria-hidden="true" />
                  {tc.person.sessions.end}
                </Button>
              </li>
            ))}
          </ul>
          <div>
            <Button type="button" variant="destructive" className="h-10" onClick={() => setEnding('all')}>
              <LogOut aria-hidden="true" />
              {tc.person.sessions.endAll}
            </Button>
          </div>
        </>
      )}
      <FormMessage>{error}</FormMessage>
      <AlertDialog open={ending !== null} onOpenChange={(open) => !open && setEnding(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {ending === 'all' ? tc.person.sessions.endAllTitle : tc.person.sessions.end}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {ending === 'all'
                ? sessions.some((s) => s.current)
                  ? tc.person.sessions.endAllOwnHelp(sessions.length)
                  : tc.person.sessions.endAllHelp(sessions.length, u.name)
                : ending
                  ? `${tc.person.sessions.endFor(nameOf(ending))}${ending.current ? `. ${tc.person.sessions.endThisHelp}` : ''}`
                  : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t.settings.cancel}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                setError('')
                if (ending) end.mutate(ending)
              }}
            >
              {ending === 'all' ? tc.person.sessions.endAll : tc.person.sessions.end}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsSection>
  )
}
