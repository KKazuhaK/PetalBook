import { useState, type FormEvent } from 'react'
import { Link } from 'react-router'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { useActiveSpace, useSession, useUpdateSpace } from '@/api/hooks'
import { can } from '@/lib/permissions'
import type { SpaceInfo } from '@/api/types'
import { Field } from '@/components/auth/Field'
import { PageFallback, Spinner } from '@/components/common'
import { Button } from '@/components/ui/button'
import { spaceUrls } from '@/lib/urls'
import { t } from '@/i18n'
import { SettingsNote, SettingsSection } from './SettingsParts'
import { InboxSection } from './SyncPanel'

// The space's own name and its inbox folder. In single-user mode both are set
// when the server starts, so they are shown and not changed here; without the
// `manage` permission they are shown as they are.
export function GeneralPanel({ space }: { space: string }) {
  const session = useSession().data
  const { spaces, resolving } = useActiveSpace()
  const current = spaces.find((s) => s.id === space)
  if (!session || resolving) return <PageFallback />
  if (!current) return <SettingsNote>{t.editor.spaceMissing}</SettingsNote>
  if (current.disabled) return <SettingsNote>{t.spaces.disabledTitle}</SettingsNote>
  const single = session.mode === 'single'
  const readOnly = single || !can(current, 'manage')
  const why = single ? undefined : t.general.noManage
  return (
    <div className="space-y-8">
      <NameSection space={current} readOnly={readOnly} why={why} />
      <InboxSection space={current} readOnly={readOnly} why={why} />
      <CheckSection space={current.id} />
    </div>
  )
}

function NameSection({ space, readOnly, why }: { space: SpaceInfo; readOnly: boolean; why?: string }) {
  const update = useUpdateSpace()
  const [value, setValue] = useState(space.name)
  const [error, setError] = useState('')

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (update.isPending) return
    const name = value.trim()
    const length = Array.from(name).length
    if (length < 1 || length > 80) return setError(t.spaces.nameInvalid)
    setError('')
    if (name === space.name) return setValue(name)
    try {
      const saved = await update.mutateAsync({ id: space.id, name })
      setValue(saved.name)
      toast.success(t.general.nameSaved)
    } catch (err) {
      setError(err instanceof ApiError && err.code === 'invalid' ? err.message : t.fmt.labelled(t.spaces.failed, err instanceof Error ? err.message : ''))
    }
  }

  return (
    <SettingsSection title={t.general.nameTitle} id="space-name-title">
      <form className="grid gap-3" onSubmit={(e) => void submit(e)} noValidate>
        <Field label={t.spaces.nameLabel} help={readOnly ? (why ?? t.general.nameReadOnly) : t.spaces.nameHelp} value={readOnly ? space.name : value} onChange={(e) => setValue(e.target.value)} readOnly={readOnly} error={error} autoComplete="off" />
        {!readOnly && (
          <div>
            <Button type="submit" disabled={update.isPending || value.trim() === space.name}>
              {update.isPending && <Spinner />}
              {t.spaces.save}
            </Button>
          </div>
        )}
      </form>
    </SettingsSection>
  )
}

// The way to the check page of the space, for whoever can read it.
function CheckSection({ space }: { space: string }) {
  return (
    <SettingsSection title={t.general.checkTitle} id="space-check-title" description={t.general.checkHelp}>
      <div>
        <Button variant="outline" render={<Link to={spaceUrls(space).check()} />}>
          {t.general.checkLink}
        </Button>
      </div>
    </SettingsSection>
  )
}
