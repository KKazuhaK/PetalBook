import { useState } from 'react'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Switch } from '@/components/ui/switch'
import { SettingsRow } from '@/components/settings/SettingsParts'
import {
  browserNoticeDetails,
  browserNotifyState,
  disableBrowserNotify,
  enableBrowserNotify,
  setBrowserNoticeDetails,
  type BrowserNotifyState,
} from '@/lib/browserNotify'
import { tn } from '@/i18n/notify'

// The opt-in for browser notifications, in the preferences. Switching it on first says what is about to happen, and only
// then asks the browser, so its own prompt is never a surprise; a refusal is explained, with the way out.
export function BrowserNotifySetting() {
  const [state, setState] = useState<BrowserNotifyState>(browserNotifyState)
  const [asking, setAsking] = useState(false)
  const [details, setDetails] = useState(browserNoticeDetails)
  const blocked = state === 'unsupported' || state === 'insecure' || state === 'denied'
  const note =
    state === 'unsupported'
      ? tn.browser.unsupported
      : state === 'insecure'
        ? tn.browser.insecure
        : state === 'denied'
          ? tn.browser.denied
          : null
  return (
    <>
      <SettingsRow title={tn.browser.heading} help={tn.browser.help} controlId="pref-browser-notify">
        <div className="flex flex-col items-end gap-1 max-sm:items-start">
          <Switch
            id="pref-browser-notify"
            checked={state === 'on'}
            disabled={blocked}
            onCheckedChange={(on) => (on ? setAsking(true) : setState(disableBrowserNotify()))}
          />
          <span className="text-xs text-muted-foreground" data-slot="browser-notify-state">
            {state === 'on' ? tn.browser.on : tn.browser.off}
          </span>
        </div>
      </SettingsRow>
      {state === 'on' && (
        <SettingsRow title={tn.browser.details} help={tn.browser.detailsHelp} controlId="pref-browser-notify-details">
          <Switch
            id="pref-browser-notify-details"
            checked={details}
            onCheckedChange={(on) => {
              setBrowserNoticeDetails(on)
              setDetails(on)
            }}
          />
        </SettingsRow>
      )}
      {note && (
        <p role="status" className="-mt-2 px-1 text-xs text-muted-foreground">
          {note}
        </p>
      )}
      <AlertDialog open={asking} onOpenChange={setAsking}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tn.browser.pre.title}</AlertDialogTitle>
            <AlertDialogDescription>{tn.browser.pre.body}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tn.browser.pre.cancel}</AlertDialogCancel>
            <AlertDialogAction onClick={() => void enableBrowserNotify().then(setState)}>
              {tn.browser.pre.continue}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}
