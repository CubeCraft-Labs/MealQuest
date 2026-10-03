// Reads the CSS Modules' rules straight from the stylesheets Vitest injects
// (css: true). jsdom's getComputedStyle cannot answer two things these
// primitives need checked: it resolves var() colours to transparent, and it
// evaluates only "all" and "screen" media queries, so styles inside
// @media (prefers-reduced-motion: reduce) never apply.

function cssRules(): CSSRule[] {
  return Array.from(document.styleSheets).flatMap((sheet) => Array.from(sheet.cssRules))
}

function collect(element: Element, rules: CSSRule[], style: CSSStyleDeclaration) {
  for (const rule of rules) {
    if (!(rule instanceof CSSStyleRule) || !element.matches(rule.selectorText)) continue
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
