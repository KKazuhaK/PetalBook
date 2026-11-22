// Keyboard shortcuts as shown in tooltips. Keys are written the way Tiptap
// writes them ("Mod-Shift-s"); Mod is Command on a Mac and Ctrl elsewhere.

export const isMac = (): boolean => typeof navigator !== 'undefined' && /Mac|iPhone|iPad|iPod/.test(navigator.platform || navigator.userAgent)

export function shortcutText(keys: string, mac = isMac()): string {
  const parts = keys.split('-').map((k) => {
    if (k === 'Mod') return mac ? '⌘' : 'Ctrl'
    if (k === 'Shift') return mac ? '⇧' : 'Shift'
    if (k === 'Alt') return mac ? '⌥' : 'Alt'
    return k.length === 1 ? k.toUpperCase() : k
  })
  return mac ? parts.join('') : parts.join('+')
}

export const withShortcut = (label: string, keys: string, mac = isMac()) => `${label} (${shortcutText(keys, mac)})`
