import type { ReactNode } from 'react'
import { LanguageToggle } from '@/components/layout/LanguageMenu'
import { ThemeToggle } from '@/components/layout/ThemeToggle'
import { useDocumentTitle } from '@/lib/useDocumentTitle'
import { t } from '@/i18n'

// The frame of the pages that come before the app: sign in, first-run setup,
// invite. One centred column that works at phone width.
export function AuthLayout({ title, intro, children }: { title: string; intro?: string; children: ReactNode }) {
  useDocumentTitle(`${title} · ${t.appName}`)
  return (
    <div className="flex min-h-svh flex-col bg-background">
      <header className="flex items-center justify-between px-4 py-3">
        <div className="flex items-center gap-2">
          <img src="/favicon.svg" alt="" className="size-6 rounded-md" />
          <span className="text-sm font-semibold">{t.appName}</span>
        </div>
        <div className="flex items-center">
          {/* 40px: these pages are used on phones and tablets. */}
          <LanguageToggle className="size-10" />
          <ThemeToggle className="size-10" />
        </div>
      </header>
      <main className="mx-auto flex w-full max-w-[400px] flex-1 flex-col justify-center gap-5 px-4 pt-4 pb-16">
        <div className="grid gap-1.5">
          <h1 className="text-xl font-semibold">{title}</h1>
          {intro && <p className="text-sm text-muted-foreground">{intro}</p>}
        </div>
        {children}
      </main>
    </div>
  )
}

// "— or —" between the password form and the single sign-on button.
export function OrDivider() {
  return (
    <div className="flex items-center gap-3 text-xs text-muted-foreground" aria-hidden="true">
      <span className="h-px flex-1 bg-border" />
      {t.auth.or}
      <span className="h-px flex-1 bg-border" />
    </div>
  )
}
