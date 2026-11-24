import { PeopleSection } from './PeopleSection'
import { StorageSummary } from './StorageSummary'

// "存储管理": what the server holds at the top, then the people and what each holds. The spaces' list is the Spaces
// page; the recycle bin and the exports are theirs.
export function ManagePanel() {
  return (
    <div className="space-y-10">
      <StorageSummary />
      <PeopleSection />
    </div>
  )
}
