import { useState } from 'react'
import { greetingFor } from '@/lib/greeting'
import { loadWrote, localDay, noteVisit, rememberGreeting } from '@/lib/homePrefs'
import { th } from '@/i18n/home'

// The hello at the top. Everything it goes by is already in the browser (the clock, the account, the pages this
// browser saved); nothing is asked of the server. What it says stays the same all day, until something that matters
// (a page the person just wrote) changes, so coming back to the home page does not give a new sentence.
export function Greeting({ now, name, registered: registeredIso, visitKey, wroteKey, variety }: { now: Date; name?: string; registered?: string; visitKey: string; wroteKey: string; variety: boolean }) {
  const [text] = useState(() => {
    const today = localDay(now)
    const visit = noteVisit(visitKey, today)
    const wrote = loadWrote(wroteKey)
    const count = wrote.filter((d) => localDay(d) === today).length
    if (visit.greeting && visit.greeting.wrote === count && visit.greeting.variety === variety && !visit.first) return visit.greeting.text
    const out = greetingFor({ now, name, registered: registeredIso ? new Date(registeredIso) : null, firstToday: visit.first, absentDays: visit.absentDays, recentUpdated: wrote, variety }, th.greet)
    rememberGreeting(visitKey, today, { text: out, wrote: count, variety })
    return out
  })
  return (
    <h1 className="text-2xl font-semibold tracking-tight text-balance md:text-3xl" data-slot="greeting">
      {text}
    </h1>
  )
}
