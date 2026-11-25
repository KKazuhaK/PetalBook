import { useState } from 'react'
import { Link } from 'react-router'
import { RefreshCw } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { useQueryClient } from '@tanstack/react-query'
import { spaceApi } from '@/api/client'
import { dayOf, keys, useDaily, useSpaceId, useSpacePermissions, useUrls } from '@/api/hooks'
import type { DailyAnswer, DailyQuote } from '@/api/types'
import { useOpenSettings } from '@/components/settings/useSettings'
import { Button } from '@/components/ui/button'
import { touchHit } from '@/lib/touch'
import { Skeleton } from '@/components/ui/skeleton'
import { lang as uiLang, t } from '@/i18n'
import { canonicalTag } from '@/lib/langTags'
import { th } from '@/i18n/home'
import { shortDate } from '@/lib/time'

// The interface language as the server wants it: a canonical language tag (zh-CN, en), what the server matches to the
// languages that are set up (zh-CN finds zh-Hans, en-GB finds en).
const lang = canonicalTag(uiLang) ?? 'en'

// "《Title》｜Subtitle" for a page that has them, else what the page is called.
export function pageHead(p: { title?: string; subtitle?: string; label: string }): string {
  if (!p.title) return p.label
  return `《${p.title}》${p.subtitle ? `｜${p.subtitle}` : ''}`
}

// Who said it and where it is from, under every text of the day whatever it came from (a page of the space, the
// administrators' lines, an external interface): "—— 作者《出处》" in Chinese, "— Author, Source" in English, only the
// parts there are, as plain text. There is never a link to somewhere else here: what an interface sends as one is
// not drawn, so that the home page does not send people off the site. Nothing at all when there is none of them.
export function Attribution({ author, source }: { author?: string; source?: string }) {
  const text =
    uiLang === 'en' ? [author, source].filter(Boolean).join(', ') : `${author ?? ''}${source ? `《${source}》` : ''}`
  if (!text) return null
  return (
    <p className="text-sm text-muted-foreground" data-slot="daily-attribution">
      {`${uiLang === 'en' ? '—' : '——'} ${text}`}
    </p>
  )
}

// The text of the day under the greeting. A page of the space (its text, opening the page when clicked), or a line
// the administrators gave, with where it is from. Nothing is drawn when there is nothing to show, and the only
// request is the one for the text; "another" asks for the next one and only changes this browser.
export function DailyBlock({ now }: { now: Date }) {
  const { date, tz } = dayOf(now)
  const [skip, setSkip] = useState(0)
  const query = useDaily(date, tz, skip, lang)
  const { manage } = useSpacePermissions()
  const urls = useUrls()
  const openSettings = useOpenSettings()
  const data: DailyAnswer | undefined = query.data

  // Another text is read first and only shown when it came: asked for too often, or with the interface down, the
  // one on screen stays and the person is told.
  const qc = useQueryClient()
  const space = useSpaceId()
  const [asking, setAsking] = useState(false)
  // The server will not skip further than this: the button stays off, with a note.
  const [skipLimit, setSkipLimit] = useState(false)
  const another = async () => {
    if (asking) return
    setAsking(true)
    try {
      await qc.fetchQuery({
        queryKey: keys.daily(space, date, skip + 1, lang),
        queryFn: ({ signal }) => spaceApi(space).daily({ date, skip: skip + 1, lang, tz }, signal),
        staleTime: 5 * 60_000,
      })
      setSkip((s) => s + 1)
    } catch (e) {
      if (e instanceof ApiError && e.reason?.code === 'skip_too_large') setSkipLimit(true)
      else toast.error(e instanceof ApiError && e.code === 'rate_limited' ? th.daily.tooOften : th.daily.anotherFailed)
    } finally {
      setAsking(false)
    }
  }

  const shown = !!data && (data.page !== null || data.quote !== undefined)
  if (query.isPending) return <Skeleton className="h-20 w-full max-w-xl" />
  if (!data) return null
  if (!shown) {
    // A source that was set up but meets no page says so, to the people who can change it.
    if (data.reason === 'no_match' && manage)
      return (
        <p className="text-sm text-muted-foreground" data-slot="daily-empty">
          {th.daily.noMatch}{' '}
          <button
            type="button"
            onClick={() => openSettings('home')}
            className="text-brand underline underline-offset-4"
          >
            {th.daily.noMatchSettings}
          </button>
        </p>
      )
    return null
  }
  const more = !(data.page && data.count === 1)
  const waiting = asking

  return (
    <figure className="grid max-w-xl gap-2" data-slot="daily" aria-label={th.daily.label}>
      {data.page ? (
        <Link
          to={urls.page(data.page.id)}
          title={th.daily.open}
          className="group block rounded-lg outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        >
          {data.page.body.trim() !== '' ? (
            <blockquote className="line-clamp-6 text-base leading-relaxed whitespace-pre-line text-foreground/90">
              {data.page.body.trim()}
              {data.page.bodyTruncated ? th.daily.cut : ''}
            </blockquote>
          ) : (
            <p className="text-base text-foreground/90">{pageHead(data.page)}</p>
          )}
        </Link>
      ) : (
        data.quote && <Quote quote={data.quote} />
      )}
      {data.page && (
        <Attribution
          author={data.page.author}
          source={
            data.page.title
              ? `${data.page.title}${data.page.subtitle ? `｜${data.page.subtitle}` : ''}`
              : t.fmt.list(data.page.source)
          }
        />
      )}
      <figcaption className="flex items-center gap-2 text-xs text-muted-foreground">
        {more && (
          <Button
            variant="ghost"
            size="xs"
            className={touchHit}
            onClick={() => void another()}
            disabled={waiting || skipLimit}
            data-slot="daily-another"
          >
            <RefreshCw className={waiting ? 'animate-spin' : ''} />
            {waiting ? th.daily.anotherBusy : th.daily.another}
          </Button>
        )}
        {more && skipLimit && <span data-slot="daily-skip-limit">{th.daily.skipLimit}</span>}
      </figcaption>
    </figure>
  )
}

function Quote({ quote }: { quote: DailyQuote }) {
  const when = quote.fetched ? new Date(quote.fetched) : null
  return (
    <>
      <blockquote className="text-base leading-relaxed whitespace-pre-line text-foreground/90">{quote.text}</blockquote>
      <Attribution author={quote.author} source={quote.source} />
      {quote.stale && (
        <p className="text-xs text-amber-700 dark:text-amber-300" data-slot="daily-stale">
          {th.daily.stale(when && !Number.isNaN(when.getTime()) ? shortDate(when) : '')}
        </p>
      )}
    </>
  )
}
