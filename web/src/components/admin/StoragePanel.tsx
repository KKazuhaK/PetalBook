import type { ReactNode } from 'react'
import { ConnectionsSection } from './ConnectionsSection'
import { StorageRules } from './StorageRules'

// The administrators' page "存储与同步" (Storage & sync). It is a list of parts, top to bottom, so the parts
// other milestones bring slot in without reshaping the page: milestone 3.1's rules for where spaces are stored
// (a remote required, the grace period, the limit of spaces kept only here, the hosts allowed, the cache) are
// at the top, and 3.5 puts its budget and pressure after them. The connections, one card per platform, are the
// bottom part.
const PARTS: readonly { id: string; node: ReactNode }[] = [
  { id: 'rules', node: <StorageRules /> },
  // { id: 'budget', node: <StorageBudget /> }, (3.5)
  { id: 'connections', node: <ConnectionsSection /> },
]

export function StoragePanel() {
  return (
    <div className="space-y-10">
      {PARTS.map((p) => (
        <div key={p.id} data-part={p.id}>
          {p.node}
        </div>
      ))}
    </div>
  )
}
