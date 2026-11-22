import { ImportSection } from './SyncPanel'

// Importing has a page of its own (folder choice, progress, the report); this
// section is the way to it, as it was on the old space settings page.
export function ImportPanel({ space }: { space: string }) {
  return (
    <div className="space-y-8">
      <ImportSection space={space} />
    </div>
  )
}
