import { vi } from 'vitest'

// A FakeAnimation is one Element.animate call: its target and arguments, and
// a finished promise the test settles with finish (or cancel, which rejects
// it with an AbortError while it runs).
export type FakeAnimation = {
  target: Element
  keyframes: Keyframe[]
  options: KeyframeAnimationOptions
  finished: Promise<FakeAnimation>
  finish: () => void
  cancel: () => void
  playState: 'running' | 'finished' | 'idle'
}

// stubAnimate replaces Element.prototype.animate (jsdom has no Web Animations
// API) and returns every animation it starts, in order. With autoFinish each
// animation finishes as it starts; otherwise the test finishes it. Remove the
// stub with Reflect.deleteProperty(Element.prototype, 'animate').
export function stubAnimate({ autoFinish }: { autoFinish: boolean }): FakeAnimation[] {
  const animations: FakeAnimation[] = []
  Element.prototype.animate = vi.fn(function (this: Element, keyframes, options) {
    let resolve!: (animation: FakeAnimation) => void
    let reject!: (error: DOMException) => void
    const animation: FakeAnimation = {
      target: this,
      keyframes: keyframes as Keyframe[],
      options: options as KeyframeAnimationOptions,
      finished: new Promise((res, rej) => {
        resolve = res
        reject = rej
      }),
      playState: 'running',
      finish() {
        if (animation.playState !== 'running') throw new Error('finish on an animation that is not running')
        animation.playState = 'finished'
        resolve(animation)
      },
      cancel() {
        if (animation.playState === 'running') reject(new DOMException('The animation was canceled.', 'AbortError'))
        animation.playState = 'idle'
      },
    }
    // Browsers mark finished as handled, so a canceled animation nobody waits
    // on is not an unhandled rejection.
    animation.finished.catch(() => {})
    animations.push(animation)
    if (autoFinish) animation.finish()
    return animation as unknown as Animation
  }) as Element['animate']
  return animations
}
