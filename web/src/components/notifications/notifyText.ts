import type { NotificationItem } from '@/api/types'
import { settingsPath } from '@/components/settings/sections'
import { spaceUrls } from '@/lib/urls'
import { tn } from '@/i18n/notify'

// The sentence of a notification and where it leads, from the kind and the facts the server stored. A kind this client
// does not know (a newer server) is a plain notice that leads nowhere.

export interface Described {
  title: string
  body: string
  // Where clicking goes ('' for nowhere), and the space that settings address then (the one the notice is about).
  to: string
  space?: string
}

const str = (v: unknown, fallback = ''): string => (typeof v === 'string' ? v : fallback)
const num = (v: unknown): number => (typeof v === 'number' && Number.isFinite(v) ? v : 0)

// The kinds of problem in the order the check page lists them.
const PROBLEM_ORDER = [
  'no_id',
  'duplicate_id',
  'bad_header',
  'type_mismatch',
  'not_in_type',
  'broken_link',
  'missing_asset',
  'duplicate_title',
] as const

export function describeNotification(n: NotificationItem): Described {
  const p = n.payload
  const p2 = tn.items
  switch (n.kind) {
    case 'problems': {
      const counts = (p.counts ?? {}) as Record<string, unknown>
      const what = PROBLEM_ORDER.filter((k) => num(counts[k]) > 0).map((k) => p2.problems.kinds[k](num(counts[k])))
      return {
        title: p2.problems.title(str(p.space), num(p.total)),
        body: p2.problems.body(what.join(p2.problems.join)),
        to: spaceUrls(n.ref).check(),
      }
    }
    case 'invite': {
      const space = str(p.space)
      const by = str(p.by)
      const roles = p2.invite.roles as Record<string, string>
      return {
        title: by ? p2.invite.title(by, space) : p2.invite.titleNoName(space),
        body: p2.invite.body(roles[str(p.role)] ?? str(p.role)),
        to: spaceUrls(n.ref).home(),
      }
    }
    case 'sync': {
      const space = str(p.space)
      if (p.event === 'ok')
        return { title: p2.sync.okTitle(space), body: p2.sync.okBody, to: settingsPath('sync'), space: n.ref }
      const bodies = p2.sync.failedBody as Record<string, string>
      const key = str(p.state) === 'rewritten' ? 'rewritten' : str(p.kind)
      return {
        title: p2.sync.failedTitle(space),
        body: bodies[key] ?? bodies.fallback,
        to: settingsPath('sync'),
        space: n.ref,
      }
    }
    // Someone proved an address the person held unverified (3.6).
    case 'email_released':
      return { title: p2.emailReleased.title(str(p.email)), body: p2.emailReleased.body, to: settingsPath('profile') }
    // An administrator took over or transferred a space the person owned.
    case 'space_taken_over':
    case 'space_transferred': {
      const title = n.kind === 'space_taken_over' ? p2.takeover.takenTitle : p2.takeover.transferredTitle
      return {
        title: title(str(p.by), str(p.space)),
        body: p2.takeover.body(str(p.reason)),
        to: spaceUrls(n.ref).home(),
      }
    }
    // Someone else changed the person's name, username or address: the fields, never the values.
    case 'account_changed': {
      const names = p2.accountChanged.fields as Record<string, string>
      const fields = (Array.isArray(p.fields) ? p.fields : []).map((f) => names[str(f)] ?? str(f)).filter(Boolean)
      return {
        title: p2.accountChanged.title(str(p.by)),
        body: p2.accountChanged.body(fields.join(p2.accountChanged.join)),
        to: settingsPath('profile'),
      }
    }
    // An administrator gave the person a temporary password.
    case 'password_set':
      return { title: p2.passwordSet.title(str(p.by)), body: p2.passwordSet.body, to: settingsPath('profile') }
    case 'mention':
    case 'todo':
      return { title: p2[n.kind].title, body: p2[n.kind].body, to: n.ref ? spaceUrls(n.ref).home() : '' }
    default:
      return { title: p2.unknown, body: '', to: '' }
  }
}
