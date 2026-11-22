import { Component, type ErrorInfo, type ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { t } from '@/i18n'

interface State {
  error: Error | null
}

// A crash in one page must not blank the sidebar, and above all must not
// throw away an editor session that still holds unsaved text.
export class ErrorBoundary extends Component<{ children: ReactNode }, State> {
  state: State = { error: null }
  static getDerivedStateFromError(error: Error): State {
    return { error }
  }
  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error(error, info.componentStack)
  }
  render() {
    if (!this.state.error) return this.props.children
    return (
      <div className="flex flex-col items-center gap-3 py-24 text-center">
        <p className="text-sm text-muted-foreground">{t.fmt.labelled(t.errors.unknown, this.state.error.message)}</p>
        <Button variant="outline" size="sm" onClick={() => this.setState({ error: null })}>
          {t.list.retry}
        </Button>
      </div>
    )
  }
}
