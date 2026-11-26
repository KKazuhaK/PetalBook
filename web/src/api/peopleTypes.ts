// The shapes of the people search and of the batches (docs/api.md, "Adding people in batches"), apart from the
// requests so that the mock can use them.

// What the picker may show of a person of the caller's own tenant.
export interface PersonRef {
  id: string
  username: string
  name: string
}

export type BatchStatus =
  | 'ok'
  | 'already_member'
  | 'not_found'
  | 'other_tenant'
  | 'disabled'
  | 'email_taken'
  | 'already_invited'
  | 'invalid'
  | 'duplicate'
  | 'forbidden'
  | 'added'
  | 'invited'
  | 'failed'

export interface BatchRowIn {
  user: string
  role?: string
  name?: string
  groups?: string[]
}

export interface BatchRowOut {
  row: number
  user: string
  role?: string
  status: BatchStatus
  userId?: string
  username?: string
  name?: string
  detail?: string
  // Tenant batches: how an invitation went, and the token to hand over when it did not go by mail.
  mail?: 'sent' | 'none' | 'limited'
  token?: string
}

export interface BatchAnswer {
  dryRun: boolean
  results: BatchRowOut[]
  summary: Partial<Record<BatchStatus, number>>
  mail?: { configured: boolean; sender: 'site' | '' }
}

export const MAX_BATCH_ROWS = 1000
