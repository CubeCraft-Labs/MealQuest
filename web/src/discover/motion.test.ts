import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { stubAnimate, type FakeAnimation } from '../test/animate.ts'
import { bump, dragTransform, fadeOut, follow, isAbort, pass, promote, snapBack, tryIt, unfollow } from './motion.ts'

let animations: FakeAnimation[]

beforeEach(() => {
  animations = stubAnimate({ autoFinish: false })
})

afterEach(() => {
  Reflect.deleteProperty(Element.prototype, 'animate')
})

// card builds a deck card with the parts DeckCard marks.
function card() {
  const article = document.createElement('article')
  article.innerHTML = '<span data-part="try-stamp"></span><span data-part="pass-stamp"></span><span data-part="shade"></span>'
  return {
    article,
    stamp: article.querySelector<HTMLElement>('[data-part="try-stamp"]')!,
    passStamp: article.querySelector<HTMLElement>('[data-part="pass-stamp"]')!,
    shade: article.querySelector('[data-part="shade"]')!,
  }
}

// settled says whether a promise has settled after pending callbacks run.
async function settled(promise: Promise<unknown>) {
  let done = false
  void promise.then(
    () => (done = true),
    () => (done = true),
  )
  await new Promise((resolve) => setTimeout(resolve, 0))
  return done
}

const FLY_OFF = { transform: 'translate(150%,-60px) rotate(28deg)', opacity: 0 }

describe('motion', () => {
  it('bumps: scale 1 → .88 → 1.06 → 1, 300 ms ease-out', () => {
    const button = document.createElement('button')
    bump(button)
    expect(animations).toHaveLength(1)
    expect(animations[0]!.target).toBe(button)
    expect(animations[0]!.keyframes).toEqual([
      { transform: 'scale(1)' },
      { transform: 'scale(.88)' },
      { transform: 'scale(1.06)' },
      { transform: 'scale(1)' },
    ])
    expect(animations[0]!.options).toEqual({ duration: 300, easing: 'ease-out' })
  })

  it('Try it pops the stamp and wiggles, then throws the card right', async () => {
    const { article, stamp } = card()
    const exit = tryIt(article)

    const [pop, wiggle] = animations
    expect(pop!.target).toBe(stamp)
    expect(pop!.keyframes).toEqual([
      { opacity: 0, transform: 'rotate(-12deg) scale(1.5)' },
      { opacity: 1, transform: 'rotate(-12deg) scale(1)' },
    ])
    expect(pop!.options).toEqual({ duration: 220, easing: 'cubic-bezier(.34,1.4,.64,1)', fill: 'forwards' })
    expect(wiggle!.target).toBe(article)
    expect(wiggle!.keyframes).toEqual(['0deg', '-4deg', '4deg', '-3deg', '2deg', '0deg'].map((a) => ({ transform: `rotate(${a})` })))
    expect(wiggle!.options).toEqual({ duration: 420, easing: 'ease-in-out' })
    expect(animations).toHaveLength(2)

    // The throw waits for the wiggle.
    pop!.finish()
    wiggle!.finish()
    await expect.poll(() => animations.length).toBe(3)
    const fly = animations[2]!
    expect(fly.target).toBe(article)
    expect(fly.keyframes).toEqual([
      { transform: 'translate(0px,0px) rotate(0deg)' },
      { transform: 'translate(-14px,4px) rotate(-3deg)', offset: 0.18 },
      FLY_OFF,
    ])
    expect(fly.options).toEqual({ duration: 480, easing: 'cubic-bezier(.55,0,.8,.4)', fill: 'forwards' })

    expect(await settled(exit.finished)).toBe(false)
    fly.finish()
    await expect(exit.finished).resolves.toBeUndefined()
  })

  it('Try it from a dragged transform skips the wiggle and throws in 340 ms', async () => {
    const { article } = card()
    const exit = tryIt(article, 'translate(120px,9px) rotate(6.7deg)')
    await expect.poll(() => animations.length).toBe(2)
    const fly = animations[1]!
    expect(fly.target).toBe(article)
    expect(fly.keyframes).toEqual([{ transform: 'translate(120px,9px) rotate(6.7deg)' }, FLY_OFF])
    expect(fly.options).toEqual({ duration: 340, easing: 'cubic-bezier(.55,0,.8,.4)', fill: 'forwards' })
    fly.finish()
    await expect(exit.finished).resolves.toBeUndefined()
  })

  it('Pass folds the card over its left edge under a darkening shade', async () => {
    const { article, shade } = card()
    const exit = pass(article)

    const [darken, fold] = animations
    expect(animations).toHaveLength(2)
    expect(darken!.target).toBe(shade)
    expect(darken!.keyframes).toEqual([{ opacity: 0 }, { opacity: 0.4, offset: 0.45 }, { opacity: 0.55 }])
    expect(darken!.options).toEqual({ duration: 720, fill: 'forwards' })
    expect(fold!.target).toBe(article)
    expect(fold!.keyframes).toEqual([
      { transformOrigin: '0% 50%', transform: 'translate(0px,0px) rotate(0deg) rotateY(0deg)', easing: 'cubic-bezier(.45,0,.85,.55)' },
      {
        transformOrigin: '0% 50%',
        transform: 'translate(0px,0px) rotate(0deg) rotateY(-80deg)',
        offset: 0.45,
        easing: 'cubic-bezier(.4,0,.9,.6)',
      },
      { transformOrigin: '0% 50%', transform: 'translate(-45%,560px) rotate(-35deg) rotateY(-90deg)', opacity: 0 },
    ])
    expect(fold!.options).toEqual({ duration: 720, fill: 'forwards' })

    darken!.finish()
    expect(await settled(exit.finished)).toBe(false)
    fold!.finish()
    await expect(exit.finished).resolves.toBeUndefined()
  })

  it('Pass from a dragged transform folds from there', () => {
    const { article } = card()
    pass(article, 'translate(-130px,6px) rotate(-7deg)')
    const fold = animations[1]!
    expect(fold.keyframes.map((k) => k.transform)).toEqual([
      'translate(-130px,6px) rotate(-7deg) rotateY(0deg)',
      'translate(-130px,6px) rotate(-7deg) rotateY(-80deg)',
      'translate(-45%,560px) rotate(-35deg) rotateY(-90deg)',
    ])
  })

  it('fades out in 200 ms with reduced motion', async () => {
    const { article } = card()
    const exit = fadeOut(article)
    expect(animations).toHaveLength(1)
    expect(animations[0]!.target).toBe(article)
    expect(animations[0]!.keyframes).toEqual([{ opacity: 1 }, { opacity: 0 }])
    expect(animations[0]!.options).toEqual({ duration: 200, fill: 'forwards' })
    animations[0]!.finish()
    await expect(exit.finished).resolves.toBeUndefined()
  })

  it('promotes the top two cards from the depth behind, 300 ms', () => {
    const cards = [card().article, card().article, card().article]
    promote(cards)
    expect(animations.map((a) => a.target)).toEqual([cards[0], cards[1]])
    expect(animations.map((a) => a.keyframes)).toEqual([
      [{ transform: 'translateY(8px) scale(0.95)' }, { transform: 'translateY(0px) scale(1)' }],
      [{ transform: 'translateY(16px) scale(0.9)' }, { transform: 'translateY(8px) scale(0.95)' }],
    ])
    for (const a of animations) expect(a.options).toEqual({ duration: 300, easing: 'cubic-bezier(.2,.8,.2,1)' })
  })

  it('cancel stops an exit and never starts its next step', async () => {
    const { article } = card()
    const exit = tryIt(article)
    animations[1]!.finish()
    exit.cancel()
    const error: unknown = await exit.finished.catch((e: unknown) => e)
    expect(isAbort(error)).toBe(true)
    expect(animations).toHaveLength(2)
    expect(animations.map((a) => a.playState)).toEqual(['idle', 'idle'])
  })

  it('drags: translate(dx, dy·0.3) rotate(dx/18 deg)', () => {
    expect(dragTransform(90, 20)).toBe('translate(90px,6px) rotate(5deg)')
    expect(dragTransform(-36, -10)).toBe('translate(-36px,-3px) rotate(-2deg)')
  })

  it.each([
    [50, '0.5', '0'],
    [-50, '0', '0.5'],
    [160, '1', '0'],
    [-160, '0', '1'],
    [0, '0', '0'],
  ])('follows a drag of %i px with the stamps at Try it! %s, Pass %s', (dx, tryOpacity, passOpacity) => {
    const { article, stamp, passStamp } = card()
    expect(follow(article, dx, 0)).toBe(dragTransform(dx, 0))
    expect(article.style.transform).toBe(dragTransform(dx, 0))
    expect(stamp.style.opacity).toBe(tryOpacity)
    expect(passStamp.style.opacity).toBe(passOpacity)
    expect(animations).toEqual([])
  })

  it('unfollows at once, back to the stylesheet', () => {
    const { article, stamp, passStamp } = card()
    follow(article, -80, 12)
    unfollow(article)
    expect(article.getAttribute('style')).toBe('')
    expect(stamp.getAttribute('style')).toBe('')
    expect(passStamp.getAttribute('style')).toBe('')
    expect(animations).toEqual([])
  })

  it('snaps back from the drag transform to rest, 420 ms spring, and hides the stamps', () => {
    const { article, stamp, passStamp } = card()
    const from = follow(article, 70, 30)
    snapBack(article, from)
    expect(article.style.transform).toBe('')
    expect(stamp.style.opacity).toBe('')
    expect(passStamp.style.opacity).toBe('')
    expect(animations).toHaveLength(1)
    expect(animations[0]!.target).toBe(article)
    expect(animations[0]!.keyframes).toEqual([{ transform: 'translate(70px,9px) rotate(3.888888888888889deg)' }, { transform: 'translate(0px,0px) rotate(0deg)' }])
    expect(animations[0]!.options).toEqual({ duration: 420, easing: 'cubic-bezier(.34,1.4,.64,1)' })
  })

  it('fails fast on a card without its parts', () => {
    expect(() => tryIt(document.createElement('article'))).toThrow('deck card has no try-stamp')
    expect(() => pass(document.createElement('article'))).toThrow('deck card has no shade')
    expect(() => follow(document.createElement('article'), 10, 0)).toThrow('deck card has no try-stamp')
  })
})
