import { Link } from 'react-router'
import { useActiveSpace } from '@/api/hooks'
import { t } from '@/i18n'
import { TopBar } from '@/components/layout/TopBar'
import { spaceUrls } from '@/lib/urls'

export function NotFoundPage({ message = t.notFound }: { message?: string }) {
  const { id } = useActiveSpace()
  return (
    <>
      <TopBar />
      <div className="py-24 text-center">
        <p className="text-muted-foreground">{message}</p>
        <Link to={id ? spaceUrls(id).home() : '/'} className="mt-3 inline-block text-sm text-brand underline underline-offset-4">
          {t.editor.backToList}
        </Link>
      </div>
    </>
  )
}
