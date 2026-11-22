import { Languages } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { isLang, lang, languages, setLanguage, t } from '@/i18n'

// Each language under its own name, under a heading. Choosing one saves it and
// reloads the page. The heading sits inside the group: the menu library gives
// a label to the group it is in, and fails without one.
export function LanguageRadioItems() {
  return (
    <DropdownMenuRadioGroup
      value={lang}
      onValueChange={(v) => {
        if (isLang(v) && v !== lang) setLanguage(v)
      }}
    >
      <DropdownMenuLabel>{t.language.label}</DropdownMenuLabel>
      {languages.map((l) => (
        <DropdownMenuRadioItem key={l.code} value={l.code} lang={l.code}>
          {l.name}
        </DropdownMenuRadioItem>
      ))}
    </DropdownMenuRadioGroup>
  )
}

// A button for the sidebar footer, next to the theme toggle. Single-user mode
// has no account menu, so this is where the language is changed there.
export function LanguageToggle() {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button variant="ghost" size="icon" aria-label={t.language.label} />}>
        <Languages className="size-4" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-36">
        <LanguageRadioItems />
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
