import { useParams, useSearchParams } from 'react-router'
import { PageListView } from '@/components/PageListView'

export function RecentList() {
  return <PageListView source={{ kind: 'recent' }} />
}

// `f/*` and `t/*` carry the path as a splat: the root folder is "/f" with an
// empty splat, and a tag may itself contain slashes.
export function FolderList() {
  const folder = useParams()['*'] ?? ''
  return <PageListView source={{ kind: 'folder', folder }} />
}

export function TagList() {
  const tag = useParams()['*'] ?? ''
  return <PageListView source={{ kind: 'tag', tag }} />
}

export function SearchList() {
  const [params] = useSearchParams()
  return <PageListView source={{ kind: 'search', q: params.get('q') ?? '' }} />
}
