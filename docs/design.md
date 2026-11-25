# PetalBook design and phased plan

The design lives in several files under `docs/design/`. This page is the table of
contents. Section numbers (§1 to §17) are the numbers the original single document
used; they are kept in the headings so that old references still make sense.

PetalBook is a self-hosted personal writing and notes application: the content is
Markdown files in a Git repository, edited on the web, with no need to know
Markdown. See the overview for the status and the goals.

## Overview

- [overview.md](design/overview.md): goals, core decisions, reuse, risks, and what is decided and open
  - [§1 Goals and non-goals](design/overview.md#1-goals-and-non-goals): how the app is used, the official build
  - [§2 Core decisions](design/overview.md#2-core-decisions)
  - [§10 Reuse](design/overview.md#10-reuse): the Git storage module, the engine and server boundary
  - [§12 Risks](design/overview.md#12-risks)
  - [§13 Decided and open](design/overview.md#13-decided-and-open)

## Design by topic

- [data-model.md](design/data-model.md): the data model
  - [§3 Data model](design/data-model.md#3-data-model): space directory layout, page files, organization, attachments, validation
- [storage-and-sync.md](design/storage-and-sync.md): storage and sync
  - [§4 Storage and sync](design/storage-and-sync.md#4-storage-and-sync): local and remote modes, credentials, drafts and commits, partial cache, conflicts, two devices
- [database.md](design/database.md): the databases
  - [§5 Database](design/database.md#5-database): state database, index database, the database baseline (5.3), the no-migration rule (5.4), the memory budget of open spaces and the rebuild limit (5.5)
- [auth-and-permissions.md](design/auth-and-permissions.md): sign-in, roles and permissions
  - [§6 Sign-in and permissions](design/auth-and-permissions.md#6-sign-in-and-permissions): sign-in methods, roles, machine access
  - [§16 Reserved: custom roles and permissions](design/auth-and-permissions.md#16-reserved-custom-roles-and-permissions)
- [editor-and-frontend.md](design/editor-and-frontend.md): the editor and the frontend
  - [§7 Editor](design/editor-and-frontend.md#7-editor): editing modes, round-trip and browser findings, server-side rendering
  - [§8 Frontend](design/editor-and-frontend.md#8-frontend): frontend stack, layout, page editing, folder view, quick capture, mobile, history and diff
- [api-shape.md](design/api-shape.md): the shape of the HTTP API
  - [§9 API shape](design/api-shape.md#9-api-shape): the endpoints themselves are in [api.md](api.md)

## Phased plan (§11)

Each phase file keeps a milestone's plan, its contract, what was built ("as built")
and the known issues together.

- [milestones/phase-0-1.md](design/milestones/phase-0-1.md): Phases 0 and 1: validation, and usable for daily work (milestones 1.1 to 1.7)
- [milestones/phase-2.md](design/milestones/phase-2.md): Phase 2, structure and AI: page types and table views (2.1), saved views and boards (2.2), space home and daily text (2.3), links and backlinks (2.4), templates and the validation page (2.5), API tokens and MCP (2.6)
- [milestones/phase-2-connections.md](design/milestones/phase-2-connections.md): Phase 2, connections and sign-in: GitHub App and automatic repository creation (2.7), passkey, SAML and several providers (2.8), the generic connection and "Storage & sync" (2.7b)
- [milestones/phase-3.md](design/milestones/phase-3.md): Phase 3, teams, first part: server storage, partial cache and the asset repository (3.1), groups (3.2), directions for 3.3 and 3.4
- [milestones/phase-3-tenants.md](design/milestones/phase-3-tenants.md): Phase 3, tenants: the 3.3 contract (tenants, layered settings, mail, registration, quotas) and as built
- [milestones/phase-3-storage.md](design/milestones/phase-3-storage.md): Phase 3, storage management (3.5): recycle bin, deleting spaces and people, archive, storage pressure
- [milestones/phase-3-autonomy-and-providers.md](design/milestones/phase-3-autonomy-and-providers.md): Phase 3, tenant autonomy (3.6), several organizations per person (3.7), GitLab and Gitea (3.8), avatars and brand icons (3.9)
- [milestones/phase-4.md](design/milestones/phase-4.md): Phase 4, offline and clients: offline sync (4.1), service worker, desktop and mobile directions
- [milestones/phase-5.md](design/milestones/phase-5.md): Phase 5, everything else not yet scheduled (including 5.17 redundancy)

## Reserved and decisions

- [reserved.md](design/reserved.md): reserved features
  - [§14 Reserved: public publishing](design/reserved.md#14-reserved-public-publishing)
  - [§15 Reserved: tables](design/reserved.md#15-reserved-tables)
- [decisions-2026-11-24.md](design/decisions-2026-11-24.md): owner decisions of 2026-11-24
  - [§17 Owner decisions of 2026-11-24 (collected, placed by milestone)](design/decisions-2026-11-24.md#17-owner-decisions-of-2026-11-24-collected-placed-by-milestone): includes the 3.10 interface rework

See also [api.md](api.md) for the HTTP API reference.
