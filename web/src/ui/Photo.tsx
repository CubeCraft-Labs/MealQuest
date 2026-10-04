import { useState, type CSSProperties } from 'react'
import styles from './Photo.module.css'

export type PhotoProps = {
  // Photo id; the server stores /media/photos/{id}-400.jpg and -1200.jpg.
  // Without one the striped "dish photo" placeholder is shown.
  id?: string
  // The dinner name, used as the alt text.
  name: string
  // Corner radius and the margin from the containing card edge, as CSS lengths.
  radius?: string
  inset?: string
  // The rendered width hint for srcset selection.
  sizes?: string
  className?: string
  style?: CSSProperties
}

// Dinner photo through the Organic "washed" filter. Falls back to the
// striped placeholder when there is no photo or it fails to load.
export function Photo({ id, name, radius = '24px', inset = '0', sizes = '100vw', className, style }: PhotoProps) {
  // The id whose image failed, so a new id gets a fresh attempt.
  const [failedId, setFailedId] = useState<string>()
  const frame = { '--photo-radius': radius, '--photo-inset': inset, ...style } as CSSProperties

  if (id === undefined || failedId === id) {
    const classes = [styles.frame, styles.placeholder, className].filter(Boolean).join(' ')
    return (
      <div role="img" aria-label={name} className={classes} style={frame}>
        <span aria-hidden="true">dish photo</span>
      </div>
    )
  }

  const base = `/media/photos/${encodeURIComponent(id)}`
  const classes = [styles.frame, styles.photo, className].filter(Boolean).join(' ')
  return (
    <img
      src={`${base}-400.jpg`}
      srcSet={`${base}-400.jpg 400w, ${base}-1200.jpg 1200w`}
      sizes={sizes}
      loading="lazy"
      alt={name}
      className={classes}
      style={frame}
      onError={() => setFailedId(id)}
    />
  )
}
