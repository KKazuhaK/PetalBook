import { Monitor, Moon, Sun } from 'lucide-react'
import { useTheme, type Theme } from '@/providers/theme'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { t } from '@/i18n'

export function ThemeToggle({ className }: { className?: string }) {
  const { theme = 'system', setTheme } = useTheme()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={<Button variant="ghost" size="icon" className={className} aria-label={t.theme.toggle} />}
      >
        <Sun className="size-4 dark:hidden" />
        <Moon className="hidden size-4 dark:block" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-36">
        <DropdownMenuRadioGroup value={theme} onValueChange={(v) => setTheme(String(v) as Theme)}>
          <DropdownMenuRadioItem value="light">
            <Sun /> {t.theme.light}
          </DropdownMenuRadioItem>
          <DropdownMenuRadioItem value="dark">
            <Moon /> {t.theme.dark}
          </DropdownMenuRadioItem>
          <DropdownMenuRadioItem value="system">
            <Monitor /> {t.theme.system}
          </DropdownMenuRadioItem>
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
