import { KeyRound, LogIn } from 'lucide-react'

// The icons an administrator can choose for a provider: `preset:<name>`. They are drawn here, never loaded
// from an address, so the sign-in page asks no other site for anything. Each is a simple mark in the
// provider's colour, not its logo.
export const ICON_PRESETS = ['google', 'microsoft', 'github', 'gitlab', 'okta', 'keycloak', 'authentik', 'key'] as const
export type IconPreset = (typeof ICON_PRESETS)[number]

const MARKS: Record<Exclude<IconPreset, 'key'>, { letter: string; color: string }> = {
  google: { letter: 'G', color: '#4285f4' },
  microsoft: { letter: 'M', color: '#00a4ef' },
  github: { letter: 'GH', color: '#24292f' },
  gitlab: { letter: 'GL', color: '#fc6d26' },
  okta: { letter: 'O', color: '#007dc1' },
  keycloak: { letter: 'K', color: '#4d4d4d' },
  authentik: { letter: 'A', color: '#fd4b2d' },
}

export const presetOf = (icon: string): IconPreset | '' => {
  const name = icon.startsWith('preset:') ? icon.slice(7) : ''
  return (ICON_PRESETS as readonly string[]).includes(name) ? (name as IconPreset) : ''
}

export function ProviderIcon({ icon, className = 'size-4' }: { icon: string; className?: string }) {
  const preset = presetOf(icon)
  if (preset === 'key') return <KeyRound aria-hidden="true" className={className} />
  if (!preset) return <LogIn aria-hidden="true" className={className} />
  const mark = MARKS[preset]
  return (
    <svg aria-hidden="true" viewBox="0 0 16 16" className={className} data-icon={preset}>
      <rect width="16" height="16" rx="4" fill={mark.color} />
      <text x="8" y="11.2" textAnchor="middle" fontSize={mark.letter.length > 1 ? 7 : 9} fontWeight="700" fill="#fff" fontFamily="system-ui, sans-serif">
        {mark.letter}
      </text>
    </svg>
  )
}
