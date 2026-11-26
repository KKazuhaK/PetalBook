// The text of the day and where it comes from: a space's own source (a folder, a tag or a type), and in accounts mode
// the interfaces the space chose. The day's text is the page at (day + skip) among the pages of the source; which page
// that is on a given day is the server's to decide, so a test that needs a particular answer says so with `fx.reply`.

import type { DailyAnswer, DailyPrefs, DailySettings, DailySource } from '../../types.ts'
import { Refusal, type Ctx, type Route } from '../kit.ts'
import { listOf, pathOf, spaceOf, summary, typeOf } from './files.ts'

const sourceOf = (settings: DailySettings | null | undefined): DailySource | null => {
  if (!settings) return null
  const { folder, tag, type } = settings
  return folder !== undefined || tag !== undefined || type !== undefined ? settings : null
}

const dayOf = (date: string) => Number(date.replace(/\D/g, '')) || 0

function answer(c: Ctx): DailyAnswer {
  const s = spaceOf(c)
  const source = sourceOf(c.data.area.daily.settings[s.info.id])
  if (!source) return { page: null, reason: 'no_source' }
  const pages = s.pages
    .filter((p) => {
      if (source.folder !== undefined && p.folder !== source.folder && !p.folder.startsWith(`${source.folder}/`))
        return false
      if (source.tag !== undefined && !listOf(p.meta.tags).includes(source.tag)) return false
      if (source.type !== undefined && typeOf(s, p) !== source.type) return false
      return true
    })
    .sort((a, b) => (pathOf(a) < pathOf(b) ? -1 : 1))
  if (pages.length === 0) return { page: null, reason: 'no_match' }
  const skip = Number(c.query.get('skip')) || 0
  const p = pages[(dayOf(c.query.get('date') ?? '') + skip) % pages.length]
  return { page: { ...summary(s, p), body: p.body }, count: pages.length, origin: 'space' }
}

function prefsOf(c: Ctx): DailyPrefs {
  const { daily } = c.data.area
  const mine = daily.prefs[spaceOf(c).info.id] ?? { inherit: true, apis: [] }
  // An interface that no longer exists is left out of the answer.
  const apis = mine.apis.filter((id) => daily.site.unavailable[id] !== 'gone')
  return {
    ...mine,
    apis,
    available: daily.site.available,
    languages: daily.site.languages,
    default: daily.site.default,
    options: mine.options ?? {},
    allowed: daily.site.allowed ?? { own: true, api: true },
    ...(Object.keys(daily.site.unavailable).length ? { unavailable: daily.site.unavailable } : {}),
  }
}

export const dailyRoutes: Route[] = [
  ['GET', '/api/spaces/:space/daily', answer],
  [
    'GET',
    '/api/spaces/:space/daily/settings',
    (c) => ({ daily: c.data.area.daily.settings[spaceOf(c).info.id] ?? null, inEffect: true }),
  ],
  [
    'PUT',
    '/api/spaces/:space/daily/settings',
    (c) => {
      c.data.area.daily.settings[spaceOf(c).info.id] = c.body.daily
      return { daily: c.body.daily ?? null, inEffect: true }
    },
  ],
  [
    'GET',
    '/api/spaces/:space/daily/prefs',
    (c) => {
      // Only accounts mode has them.
      if (c.data.auth.mode !== 'accounts') throw new Refusal(404, 'not_found', 'no such endpoint')
      return prefsOf(c)
    },
  ],
  [
    'PUT',
    '/api/spaces/:space/daily/prefs',
    (c) => {
      const { inherit, apis, options, sources } = c.body
      c.data.area.daily.prefs[spaceOf(c).info.id] = {
        inherit,
        apis,
        ...(options ? { options } : {}),
        ...(sources ? { sources } : {}),
      }
      return prefsOf(c)
    },
  ],
]
