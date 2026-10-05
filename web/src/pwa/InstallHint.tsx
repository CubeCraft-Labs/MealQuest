import { useState } from 'react'
import { Share, X } from 'lucide-react'
import { Card } from '../ui/Card.tsx'
import { Icon } from '../ui/Icon.tsx'
import { IconButton } from '../ui/IconButton.tsx'
import styles from './InstallHint.module.css'

// DISMISSED_KEY is the localStorage key remembering, on this device, that the
// household dismissed the hint.
export const DISMISSED_KEY = 'mealquest.installHint.dismissed'

// iOS browsers other than Safari name themselves in the user agent; Safari
// does not, and in-app web views leave out the Safari/ token.
const OTHER_IOS_BROWSERS = /CriOS|FxiOS|EdgiOS|OPiOS|OPT\/|GSA\/|YaBrowser|DuckDuckGo|Brave/

// isIosSafari reports whether the page is open in Safari on iPhone, iPod or
// iPad. iPadOS 13+ asks for the desktop site and reports itself as a Mac, so
// a "Macintosh" with a touch screen is an iPad.
export function isIosSafari(nav: Pick<Navigator, 'userAgent' | 'maxTouchPoints'>): boolean {
  const ua = nav.userAgent
  const ios = /iPhone|iPod|iPad/.test(ua) || (/Macintosh/.test(ua) && nav.maxTouchPoints > 1)
  return ios && /Safari\//.test(ua) && !OTHER_IOS_BROWSERS.test(ua)
}

// isStandalone reports whether the app is running from the Home Screen.
// Safari sets navigator.standalone; other browsers answer the display-mode
// media query.
export function isStandalone(): boolean {
  const legacy = (navigator as Navigator & { standalone?: boolean }).standalone
  return legacy === true || window.matchMedia('(display-mode: standalone)').matches
}

// Storage can be unavailable (blocked by the browser or privacy settings), in
// which case reading it throws. The hint then counts as not dismissed.
function readDismissed(): boolean {
  try {
    return window.localStorage.getItem(DISMISSED_KEY) === '1'
  } catch {
    return false
  }
}

// Without storage the dismissal lasts until the page is reloaded.
function writeDismissed() {
  try {
    window.localStorage.setItem(DISMISSED_KEY, '1')
  } catch {
    // Nothing to remember it in; the component state still hides the hint.
  }
}

// InstallHint tells iOS and iPadOS Safari users how to install the app, which
// iOS requires before it delivers Web Push. It shows only in a Safari browser
// tab, never once installed, and stays dismissed on this device. Used by
// onboarding step 3 and the Notifications screen.
export function InstallHint() {
  const [visible, setVisible] = useState(() => isIosSafari(navigator) && !isStandalone() && !readDismissed())
  if (!visible) return null

  function dismiss() {
    writeDismissed()
    setVisible(false)
  }

  return (
    <Card tone="muted" role="note" className={styles.hint}>
      <Icon icon={Share} className={styles.icon} />
      <p className={styles.message}>Tap Share, then Add to Home Screen to get notifications</p>
      <IconButton label="Dismiss" onClick={dismiss}>
        <Icon icon={X} />
      </IconButton>
    </Card>
  )
}
