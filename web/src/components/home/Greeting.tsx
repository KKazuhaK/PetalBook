import { useMemo } from 'react'
import { greetingFor } from '@/lib/greeting'
import { th } from '@/i18n/home'

// The hello at the top. Everything it goes by is already in the page (the clock, the account, the pages just
// written); nothing is asked of the server, and what it says does not change within the day unless something
// that matters (a page just written) does.
export function Greeting({ now, name, registered: registeredIso, firstToday, absentDays, recentUpdated, variety }: { now: Date; name?: string; registered?: string; firstToday: boolean; absentDays: number; recentUpdated: Date[]; variety: boolean }) {
  const text = useMemo(() => greetingFor({ now, name, registered: registeredIso ? new Date(registeredIso) : null, firstToday, absentDays, recentUpdated, variety }, th.greet), [now, name, registeredIso, firstToday, absentDays, recentUpdated, variety])
  return (
    <h1 className="text-2xl font-semibold tracking-tight text-balance md:text-3xl" data-slot="greeting">
      {text}
    </h1>
  )
}
