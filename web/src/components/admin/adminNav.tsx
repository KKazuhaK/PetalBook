import { lazy, Suspense, type ComponentType, type ReactNode } from 'react'
import {
  Activity,
  Building2,
  Database,
  FileText,
  Layers,
  LayoutGrid,
  Mail,
  Quote,
  Shield,
  Users,
  UsersRound,
} from 'lucide-react'
import { PageFallback } from '@/components/common'
import { tc } from '@/i18n/console'
import { tdom } from '@/i18n/domains'
import type { AdminSection } from '@/lib/urls'
import { GroupsPanel } from './GroupsPanel'
import { SettingsPanel } from './SettingsPanel'
import { SpacesPanel } from './SpacesPanel'
import { StatusPanel } from './StatusPanel'
import { TenantsPanel } from './TenantsPanel'
import { UsagePanel } from './UsagePanel'
import { InvitesPanel } from './users/InvitesPanel'
import { UsersList } from './users/UsersList'

// The administration's chunks: the daily text's page is the biggest and the least visited; the security pages carry
// the providers and the audit log; the storage ones the rules and the connections.
const lazyNamed = <K extends string>(load: () => Promise<Record<K, ComponentType>>, name: K) =>
  lazy(() => load().then((m) => ({ default: m[name] })))
const DailyPanel = lazyNamed(() => import('./DailyPanel'), 'DailyPanel')
const ManagePanel = lazyNamed(() => import('./manage/ManagePanel'), 'ManagePanel')
const DeletedPeople = lazy(() => import('./manage/RecyclePanel').then((m) => ({ default: m.DeletedPeople })))
const DeletedSpaces = lazy(() => import('./manage/RecyclePanel').then((m) => ({ default: m.DeletedSpaces })))
const AccessPanel = lazyNamed(() => import('./AccessPanel'), 'AccessPanel')
const StorageRules = lazyNamed(() => import('./StorageRules'), 'StorageRules')
const ConnectionsSection = lazyNamed(() => import('./ConnectionsSection'), 'ConnectionsSection')
const OrgGitHub = lazyNamed(() => import('./OrgGitHub'), 'OrgGitHub')
const MailPanel = lazyNamed(() => import('./MailPanel'), 'MailPanel')
const SecurityPanel = lazyNamed(() => import('./AdminSecurityPanel'), 'AdminSecurityPanel')
const ProvidersSection = lazyNamed(() => import('./ProvidersSection'), 'ProvidersSection')
const PlansPanel = lazyNamed(() => import('./PlansPanel'), 'PlansPanel')
const AuditSection = lazyNamed(() => import('./AuditSection'), 'AuditSection')
const OrgDomains = lazyNamed(() => import('./OrgDomains'), 'OrgDomains')
const SiteDomains = lazyNamed(() => import('./SiteDomains'), 'SiteDomains')

export interface NavTab {
  key: string
  label: string
  // Only an administrator of the whole server sees it.
  siteOnly?: boolean
  // Only in an organisation's console (a tenant's own, or a site administrator in a tenant's scope).
  orgOnly?: boolean
  render: () => ReactNode
}
export interface NavSection {
  key: AdminSection
  label: string
  Icon: typeof Users
  siteOnly?: boolean
  tabs: NavTab[]
}

const later = (node: ReactNode) => <Suspense fallback={<PageFallback />}>{node}</Suspense>

// The sidebar's items, each with the pages along its top. What only the server's own administrators may open is marked
// (a tenant's administrator gets the rest, for their tenant).
export const NAV: readonly NavSection[] = [
  {
    key: 'users',
    label: tc.sections.users,
    Icon: Users,
    tabs: [
      { key: 'all', label: tc.tabs.users.all, render: () => <UsersList /> },
      { key: 'invites', label: tc.tabs.users.invites, render: () => <InvitesPanel /> },
      { key: 'deleted', label: tc.tabs.users.deleted, render: () => later(<DeletedPeople />) },
    ],
  },
  {
    key: 'groups',
    label: tc.sections.groups,
    Icon: UsersRound,
    tabs: [{ key: 'groups', label: tc.sections.groups, render: () => <GroupsPanel /> }],
  },
  {
    key: 'spaces',
    label: tc.sections.spaces,
    Icon: LayoutGrid,
    tabs: [
      { key: 'all', label: tc.tabs.spaces.all, render: () => <SpacesPanel /> },
      { key: 'deleted', label: tc.tabs.spaces.deleted, render: () => later(<DeletedSpaces />) },
    ],
  },
  {
    key: 'storage',
    label: tc.sections.storage,
    Icon: Database,
    tabs: [
      { key: 'manage', label: tc.tabs.storage.manage, render: () => later(<ManagePanel />) },
      { key: 'usage', label: tc.tabs.storage.usage, render: () => <UsagePanel /> },
      { key: 'rules', label: tc.tabs.storage.rules, siteOnly: true, render: () => later(<StorageRules />) },
      {
        key: 'connections',
        label: tc.tabs.storage.connections,
        siteOnly: true,
        render: () => later(<ConnectionsSection />),
      },
      { key: 'github', label: tc.tabs.storage.github, orgOnly: true, render: () => later(<OrgGitHub />) },
    ],
  },
  {
    key: 'security',
    label: tc.sections.security,
    Icon: Shield,
    siteOnly: true,
    tabs: [
      { key: 'signin', label: tc.tabs.security.signin, render: () => later(<SecurityPanel />) },
      { key: 'providers', label: tc.tabs.security.providers, render: () => later(<ProvidersSection />) },
      { key: 'access', label: tc.tabs.security.access, render: () => later(<AccessPanel />) },
    ],
  },
  {
    key: 'mail',
    label: tc.sections.mail,
    Icon: Mail,
    siteOnly: true,
    tabs: [{ key: 'mail', label: tc.sections.mail, render: () => later(<MailPanel />) }],
  },
  {
    key: 'daily',
    label: tc.sections.daily,
    Icon: Quote,
    siteOnly: true,
    tabs: [{ key: 'daily', label: tc.sections.daily, render: () => later(<DailyPanel />) }],
  },
  {
    key: 'settings',
    label: tc.sections.settings,
    Icon: Layers,
    tabs: [
      { key: 'settings', label: tc.sections.settings, render: () => <SettingsPanel /> },
      // The organisation's sign-in: its own SSO (3.6 d3) and email domains (d2); a site administrator has them on each
      // tenant's page.
      { key: 'signin', label: tdom.signinTab, orgOnly: true, render: () => later(<OrgDomains />) },
    ],
  },
  {
    key: 'tenants',
    label: tc.sections.tenants,
    Icon: Building2,
    siteOnly: true,
    tabs: [
      { key: 'tenants', label: tc.tabs.tenants.tenants, render: () => <TenantsPanel /> },
      { key: 'plans', label: tc.tabs.tenants.plans, render: () => later(<PlansPanel />) },
      { key: 'domains', label: tdom.site.title, render: () => later(<SiteDomains />) },
    ],
  },
  {
    key: 'audit',
    label: tc.sections.audit,
    Icon: FileText,
    tabs: [{ key: 'audit', label: tc.sections.audit, render: () => later(<AuditSection />) }],
  },
  {
    key: 'status',
    label: tc.sections.status,
    Icon: Activity,
    siteOnly: true,
    tabs: [{ key: 'status', label: tc.sections.status, render: () => <StatusPanel /> }],
  },
]

// What this administrator may open. `org`: the console is an organisation's (not the site's, nor the default tenant's,
// which is no organisation).
export function navFor(site: boolean, org = !site): NavSection[] {
  return NAV.filter((s) => site || !s.siteOnly).map((s) => ({
    ...s,
    tabs: s.tabs.filter((t) => (site || !t.siteOnly) && (org || !t.orgOnly)),
  }))
}
