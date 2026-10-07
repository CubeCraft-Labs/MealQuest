// The deck's motion, from the handoff's Motion §2 (Try it), §3 (Pass),
// §4 (swipe), §5 (deck advance) and §6 (reduced motion), with the Web
// Animations API. The exits take the card's starting transform so the swipe
// gesture can throw a dragged card from where it was let go.

const SPRING = 'cubic-bezier(.34,1.4,.64,1)'
const THROW = 'cubic-bezier(.55,0,.8,.4)'
const SOFT = 'cubic-bezier(.2,.8,.2,1)'
const REDUCED_MOTION_QUERY = '(prefers-reduced-motion: reduce)'

// REST is the top card's transform when nothing moves it, written as the
// translate + rotate a drag gives it so the exits interpolate function by
// function.
export const REST = 'translate(0px,0px) rotate(0deg)'

// DEPTH_TRANSFORM is each deck depth's transform, as the stylesheet places it.
export const DEPTH_TRANSFORM = ['translateY(0px) scale(1)', 'translateY(8px) scale(0.95)', 'translateY(16px) scale(0.9)'] as const

const FLY_OFF = 'translate(150%,-60px) rotate(28deg)'
const PASS_ORIGIN = '0% 50%'

// An Exit is a card leaving the deck: finished resolves once it is off
// screen and rejects with an AbortError if cancel stops it first. cancel
// also undoes a finished exit, putting the card back.
export type Exit = {
  finished: Promise<void>
  cancel: () => void
}

export function prefersReducedMotion(): boolean {
  return window.matchMedia(REDUCED_MOTION_QUERY).matches
}

// isAbort says whether an exit's finished rejected because it was canceled.
export function isAbort(error: unknown): boolean {
  return error instanceof DOMException && error.name === 'AbortError'
}

// bump is the press bump of the Try it and Pass buttons and the Quest log pill.
export function bump(element: Element): Animation {
  return element.animate(
    [{ transform: 'scale(1)' }, { transform: 'scale(.88)' }, { transform: 'scale(1.06)' }, { transform: 'scale(1)' }],
    { duration: 300, easing: 'ease-out' },
  )
}

// tryIt pops the "Try it!" stamp and throws the card off to the right. From
// rest it wiggles first and winds up before the throw; from a dragged
// transform it skips both and throws faster.
export function tryIt(card: HTMLElement, from?: string): Exit {
  const exit = recorder()
  exit.run(part(card, 'try-stamp'), [
    { opacity: 0, transform: 'rotate(-12deg) scale(1.5)' },
    { opacity: 1, transform: 'rotate(-12deg) scale(1)' },
  ], { duration: 220, easing: SPRING, fill: 'forwards' })
  const wiggled =
    from === undefined
      ? exit.run(card, [
          { transform: 'rotate(0deg)' },
          { transform: 'rotate(-4deg)' },
          { transform: 'rotate(4deg)' },
          { transform: 'rotate(-3deg)' },
          { transform: 'rotate(2deg)' },
          { transform: 'rotate(0deg)' },
        ], { duration: 420, easing: 'ease-in-out' }).finished
      : Promise.resolve()
  const finished = wiggled.then(() =>
    from === undefined
      ? exit.run(card, [
          { transform: REST },
          { transform: 'translate(-14px,4px) rotate(-3deg)', offset: 0.18 },
          { transform: FLY_OFF, opacity: 0 },
        ], { duration: 480, easing: THROW, fill: 'forwards' }).finished
      : exit.run(card, [{ transform: from }, { transform: FLY_OFF, opacity: 0 }], { duration: 340, easing: THROW, fill: 'forwards' })
          .finished,
  )
  return exit.done(finished)
}

// pass folds the card over its left edge like a page, darkening it, then
// drops it off to the left. A dragged card folds from where it was let go.
export function pass(card: HTMLElement, from: string = REST): Exit {
  const exit = recorder()
  exit.run(part(card, 'shade'), [{ opacity: 0 }, { opacity: 0.4, offset: 0.45 }, { opacity: 0.55 }], {
    duration: 720,
    fill: 'forwards',
  })
  const finished = exit.run(card, [
    { transformOrigin: PASS_ORIGIN, transform: `${from} rotateY(0deg)`, easing: 'cubic-bezier(.45,0,.85,.55)' },
    { transformOrigin: PASS_ORIGIN, transform: `${from} rotateY(-80deg)`, offset: 0.45, easing: 'cubic-bezier(.4,0,.9,.6)' },
    { transformOrigin: PASS_ORIGIN, transform: 'translate(-45%,560px) rotate(-35deg) rotateY(-90deg)', opacity: 0 },
  ], { duration: 720, fill: 'forwards' }).finished
  return exit.done(finished)
}

// dragTransform is the top card's transform dragged dx, dy from where the
// pointer went down.
export function dragTransform(dx: number, dy: number): string {
  return `translate(${dx}px,${dy * 0.3}px) rotate(${dx / 18}deg)`
}

// follow moves a dragged card to its drag transform and shows each stamp as
// far as the drag has gone toward its side, fully at 100 px. It returns the
// transform, for the exit or snap-back the release starts.
export function follow(card: HTMLElement, dx: number, dy: number): string {
  const transform = dragTransform(dx, dy)
  card.style.transform = transform
  part(card, 'try-stamp').style.opacity = String(clamp(dx / 100))
  part(card, 'pass-stamp').style.opacity = String(clamp(-dx / 100))
  return transform
}

// unfollow puts a dragged card and its stamps back where the stylesheet
// places them, at once.
export function unfollow(card: HTMLElement) {
  card.style.removeProperty('transform')
  part(card, 'try-stamp').style.removeProperty('opacity')
  part(card, 'pass-stamp').style.removeProperty('opacity')
}

// snapBack springs a card let go short of a decision back to rest from its
// drag transform, and hides its stamps.
export function snapBack(card: HTMLElement, from: string): Animation {
  unfollow(card)
  return card.animate([{ transform: from }, { transform: REST }], { duration: 420, easing: SPRING })
}

// fadeOut is both exits under prefers-reduced-motion: reduce.
export function fadeOut(card: HTMLElement): Exit {
  const exit = recorder()
  return exit.done(exit.run(card, [{ opacity: 1 }, { opacity: 0 }], { duration: 200, fill: 'forwards' }).finished)
}

// promote moves the cards left after an exit, top first, from the depth
// behind to their own.
export function promote(cards: HTMLElement[]): Animation[] {
  return cards.slice(0, DEPTH_TRANSFORM.length - 1).map((card, depth) =>
    card.animate([{ transform: DEPTH_TRANSFORM[depth + 1] }, { transform: DEPTH_TRANSFORM[depth] }], {
      duration: 300,
      easing: SOFT,
    }),
  )
}

function clamp(value: number): number {
  return Math.min(1, Math.max(0, value))
}

// part is the card's element marked data-part="name", which DeckCard renders.
function part(card: HTMLElement, name: string): HTMLElement {
  const element = card.querySelector<HTMLElement>(`[data-part="${name}"]`)
  if (element === null) throw new Error(`deck card has no ${name}`)
  return element
}

// recorder keeps every animation an exit starts, so cancel can stop them all.
// A step that would start after cancel aborts instead.
function recorder() {
  const animations: Animation[] = []
  let canceled = false
  return {
    run(element: Element, keyframes: Keyframe[], options: KeyframeAnimationOptions): Animation {
      if (canceled) throw new DOMException('The exit was canceled.', 'AbortError')
      const animation = element.animate(keyframes, options)
      animations.push(animation)
      return animation
    },
    done(finished: Promise<unknown>): Exit {
      return {
        finished: finished.then(() => undefined),
        cancel: () => {
          canceled = true
          animations.forEach((animation) => animation.cancel())
        },
      }
    },
  }
}
