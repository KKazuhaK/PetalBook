import { useState } from 'react'
import { Monitor, Moon, Sun } from 'lucide-react'
import { Switch } from '@/components/ui/switch'
import { NativeSelect } from '@/components/ui/native-select'
import { getEditMode, setEditMode, EDIT_MODES, type DefaultEditMode } from '@/lib/editPrefs'
import { palettes, swatchOf, type Palette } from '@/lib/palettes'
import { getUploadOriginal, setUploadOriginal } from '@/lib/uploadPrefs'
import { useTheme, type Theme } from '@/providers/theme'
import { isLang, lang, languages, setLanguage, t } from '@/i18n'
import { SettingsRow, SettingsRows, SettingsSection } from './SettingsParts'

// Per-browser preferences: how the app looks, the language, and two choices
// the editor reads. Nothing here goes to the server.

const MODES: { value: Theme; label: string; Icon: typeof Sun }[] = [
  { value: 'light', label: t.theme.light, Icon: Sun },
  { value: 'dark', label: t.theme.dark, Icon: Moon },
  { value: 'system', label: t.theme.system, Icon: Monitor },
]

// A row of buttons of which exactly one is chosen (light, dark, system).
function ModeControl() {
  const { theme, setTheme } = useTheme()
  return (
    <div role="radiogroup" aria-label={t.prefs.mode} className="inline-flex rounded-lg border p-0.5">
      {MODES.map(({ value, label, Icon }) => (
        <button
          key={value}
          type="button"
          role="radio"
          aria-checked={theme === value}
          onClick={() => setTheme(value)}
          className={`inline-flex h-7 items-center gap-1.5 rounded-md px-2.5 text-sm outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 ${theme === value ? 'bg-muted font-medium text-foreground' : 'text-muted-foreground hover:text-foreground'}`}
        >
          <Icon className="size-3.5" />
          {label}
        </button>
      ))}
    </div>
  )
}

const paletteName = (p: Palette) => (p.id === 'default' ? t.prefs.paletteDefault : p.name)

// Each palette as a small card that shows its own colours in the mode that is
// showing, so the choice can be made by eye.
function PaletteControl() {
  const { palette, setPalette, dark } = useTheme()
  return (
    <div role="radiogroup" aria-label={t.prefs.palette} className="grid grid-cols-3 gap-2 max-sm:grid-cols-2 sm:w-[21rem]">
      {palettes.map((p) => {
        const [bg, fg, accent] = swatchOf(p, dark ? 'dark' : 'light')
        const on = palette === p.id
        return (
          <button
            key={p.id}
            type="button"
            role="radio"
            aria-checked={on}
            aria-label={paletteName(p)}
            data-palette-option={p.id}
            onClick={() => setPalette(p.id)}
            className={`group flex flex-col gap-1.5 rounded-lg border p-1.5 text-left outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 ${on ? 'border-ring ring-2 ring-ring/40' : 'hover:border-ring/60'}`}
          >
            <span aria-hidden="true" className="flex h-9 items-center gap-1.5 rounded-md px-2 ring-1 ring-black/10" style={{ background: bg }}>
              <span className="text-[0.8rem] leading-none font-semibold" style={{ color: fg }}>
                Aa
              </span>
              <span className="ml-auto size-3 rounded-full" style={{ background: accent }} />
              <span className="size-3 rounded-full opacity-60" style={{ background: fg }} />
            </span>
            <span className="truncate px-0.5 text-xs">{paletteName(p)}</span>
          </button>
        )
      })}
    </div>
  )
}

export function PreferencesPanel() {
  // The two editor preferences live in storage the editor reads; the state here
  // only mirrors it for display.
  const [original, setOriginal] = useState(getUploadOriginal)
  const [mode, setMode] = useState<DefaultEditMode>(getEditMode)
  return (
    <div className="space-y-8">
      <SettingsSection title={t.prefs.appearance}>
        <SettingsRows>
          <SettingsRow title={t.prefs.mode} help={t.prefs.modeHelp}>
            <ModeControl />
          </SettingsRow>
          <SettingsRow title={t.prefs.palette} help={t.prefs.paletteHelp}>
            <PaletteControl />
          </SettingsRow>
        </SettingsRows>
      </SettingsSection>

      <SettingsSection title={t.prefs.languageTitle}>
        <SettingsRows>
          <SettingsRow title={t.prefs.language} help={t.prefs.languageHelp} controlId="pref-language">
            <div className="w-44 max-sm:w-full">
              <NativeSelect
                id="pref-language"
                value={lang}
                onChange={(e) => {
                  if (isLang(e.target.value) && e.target.value !== lang) setLanguage(e.target.value)
                }}
              >
                {languages.map((l) => (
                  <option key={l.code} value={l.code} lang={l.code}>
                    {l.name}
                  </option>
                ))}
              </NativeSelect>
            </div>
          </SettingsRow>
        </SettingsRows>
      </SettingsSection>

      <SettingsSection title={t.prefs.editorTitle}>
        <SettingsRows>
          <SettingsRow title={t.prefs.uploadOriginal} help={t.prefs.uploadOriginalHelp} controlId="pref-original">
            <Switch
              id="pref-original"
              checked={original}
              onCheckedChange={(on) => {
                setOriginal(on)
                setUploadOriginal(on)
              }}
            />
          </SettingsRow>
          <SettingsRow title={t.prefs.editMode} help={t.prefs.editModeHelp} controlId="pref-edit-mode">
            <div className="w-44 max-sm:w-full">
              <NativeSelect
                id="pref-edit-mode"
                value={mode}
                onChange={(e) => {
                  const next = e.target.value as DefaultEditMode
                  setMode(next)
                  setEditMode(next)
                }}
              >
                {EDIT_MODES.map((m) => (
                  <option key={m} value={m}>
                    {t.modes[m === 'rich' ? 'rich' : m === 'source' ? 'source' : 'split']}
                  </option>
                ))}
              </NativeSelect>
            </div>
          </SettingsRow>
        </SettingsRows>
      </SettingsSection>
    </div>
  )
}
