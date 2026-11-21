import { Link } from 'react-router'
import { t } from '@/i18n/zh-CN'
import { TopBar } from '@/components/layout/TopBar'

export function NotFoundPage() {
  return (
    <>
      <TopBar />
      <div className="py-24 text-center">
        <p className="text-muted-foreground">{t.notFound}</p>
        <Link to="/recent" className="mt-3 inline-block text-sm text-brand underline underline-offset-4">
          {t.editor.backToList}
        </Link>
      </div>
    </>
  )
}
