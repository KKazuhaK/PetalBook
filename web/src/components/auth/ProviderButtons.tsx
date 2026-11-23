import { providerStartUrl } from '@/api/client'
import type { PublicProvider } from '@/api/types'
import { buttonVariants } from '@/components/ui/button'
import { Button } from '@/components/ui/button'
import { ts } from '@/i18n/signin'
import { ProviderIcon } from './ProviderIcon'

const WIDE = 'w-full min-w-0 justify-start gap-2.5 px-3 h-9'

// The sign-in page's buttons: each is a plain navigation to the provider's `start` (docs/api.md,
// "Starting"), so they work without scripts and another site cannot start one for the person.
export function ProviderLinks({ providers, ret }: { providers: PublicProvider[]; ret: string }) {
  return (
    <div className="grid grid-cols-1 gap-2">
      {providers.map((p) => (
        <a key={`${p.kind}/${p.slug}`} href={providerStartUrl(p.kind, p.slug, ret)} className={`${buttonVariants({ variant: 'outline' })} ${WIDE}`}>
          <ProviderIcon icon={p.icon} />
          <span className="min-w-0 truncate">{ts.login.withProvider(p.name)}</span>
        </a>
      ))}
    </div>
  )
}

// The same buttons where a token or a session has to be sent (setup, invite, link): they ask for the address first.
export function ProviderActions({ providers, label, onPick, disabled }: { providers: PublicProvider[]; label: (name: string) => string; onPick: (p: PublicProvider) => void; disabled?: boolean }) {
  return (
    <div className="grid grid-cols-1 gap-2">
      {providers.map((p) => (
        <Button key={`${p.kind}/${p.slug}`} type="button" variant="outline" className={WIDE} disabled={disabled} onClick={() => onPick(p)}>
          <ProviderIcon icon={p.icon} />
          <span className="min-w-0 truncate">{label(p.name)}</span>
        </Button>
      ))}
    </div>
  )
}
