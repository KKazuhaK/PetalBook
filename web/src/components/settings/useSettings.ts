import { useCallback } from 'react'
import { useLocation, useNavigate } from 'react-router'
import { settingsPath, type SectionId } from './sections'

// Carried in the location state of the settings address: where the person was,
// so closing returns there (and a deep link, which has none, closes to home).
export interface SettingsState {
  from?: string
}

// Opens the settings dialog on a section (the first one there is, without one).
// Moving between sections replaces the entry, so a single "back" or Close
// leaves settings whatever was visited inside.
export function useOpenSettings() {
  const navigate = useNavigate()
  const location = useLocation()
  return useCallback(
    (section?: SectionId) => {
      const inSettings = location.pathname.startsWith('/settings')
      const state: SettingsState = inSettings
        ? ((location.state as SettingsState | null) ?? {})
        : { from: location.pathname + location.search + location.hash }
      navigate(section ? settingsPath(section) : '/settings', { state, replace: inSettings })
    },
    [navigate, location],
  )
}
