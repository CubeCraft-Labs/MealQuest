// Reads the CSS Modules' rules straight from the stylesheets Vitest injects
// (css: true). jsdom's getComputedStyle cannot answer two things these
// primitives need checked: it resolves var() colours to transparent, and it
// evaluates only "all" and "screen" media queries, so styles inside
// @media (prefers-reduced-motion: reduce) never apply.

function cssRules(): CSSRule[] {
  return Array.from(document.styleSheets).flatMap((sheet) => Array.from(sheet.cssRules))
}

// A selector with a pseudo-element never matches an element, and jsdom's
// matches() throws on the vendor ones (::-moz-range-track), so those rules
// are skipped.
function collect(element: Element, rules: CSSRule[], style: CSSStyleDeclaration) {
  for (const rule of rules) {
    if (!(rule instanceof CSSStyleRule) || rule.selectorText.includes('::')) continue
    if (!element.matches(rule.selectorText)) continue
    for (const property of Array.from(rule.style)) {
      style.setProperty(property, rule.style.getPropertyValue(property))
    }
  }
}

// The declarations the stylesheets give the element, as written. Read the
// shorthands the CSS uses (background, animation): jsdom does not expand
// animation, nor any shorthand holding a var(). Later rules win; specificity
// is not considered.
export function declaredStyle(element: Element): CSSStyleDeclaration {
  const style = document.createElement('div').style
  collect(element, cssRules(), style)
  return style
}

// Only the declarations @media (prefers-reduced-motion: reduce) gives the element.
export function reducedMotionStyle(element: Element): CSSStyleDeclaration {
  const style = document.createElement('div').style
  for (const rule of cssRules()) {
    if (rule instanceof CSSMediaRule && rule.conditionText.includes('prefers-reduced-motion: reduce')) {
      collect(element, Array.from(rule.cssRules), style)
    }
  }
  return style
}

// The @keyframes the element's animation runs. CSS Modules hash keyframe
// names, so this follows the element's own animation-name.
export function animationKeyframes(element: Element): CSSKeyframesRule {
  const [name] = declaredStyle(element).animation.split(' ')
  const keyframes = cssRules().find(
    (rule): rule is CSSKeyframesRule => rule instanceof CSSKeyframesRule && rule.name === name,
  )
  if (!keyframes) throw new Error(`no @keyframes named ${JSON.stringify(name)}`)
  return keyframes
}

function px(value: string, what: string): number {
  if (value === '') return 0
  const match = /^(\d+(?:\.\d+)?)px$/.exec(value)
  if (!match) throw new Error(`${what} ${JSON.stringify(value)} is not a px length`)
  return Number(match[1])
}

// The border-box size the element's declared rules give it on one axis:
// width or height, plus padding and border unless box-sizing is border-box.
// jsdom does no layout (getBoundingClientRect is all zeros), so this is the
// way to catch padding that would push a fixed box past its specified size.
export function outerSize(element: Element, axis: 'width' | 'height'): number {
  const style = declaredStyle(element)
  const size = px(style.getPropertyValue(axis), axis)
  if (style.boxSizing === 'border-box') return size
  const sides = axis === 'width' ? ['left', 'right'] : ['top', 'bottom']
  return sides.reduce(
    (total, side) =>
      total +
      px(style.getPropertyValue(`padding-${side}`), `padding-${side}`) +
      px(style.getPropertyValue(`border-${side}-width`), `border-${side}-width`),
    size,
  )
}
