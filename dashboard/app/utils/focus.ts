/**
 * Where the keyboard's focus goes: inside a group that is one stop of the Tab
 * order, inside a surface that keeps it, and after a navigation. Pure, so the
 * rules are unit-tested; the composables apply them to elements.
 */

/** What Tab stops at: the same set the browser walks, minus what is disabled or taken out on purpose. */
export const TABBABLE = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  'summary',
  '[tabindex]:not([tabindex="-1"])',
].join(', ')

export type Orientation = 'horizontal' | 'vertical' | 'both'

/**
 * The item an arrow key moves to in a group that is a single stop (a radio
 * group, a toolbar): the arrows of its orientation step and wrap, Home and End
 * jump, and any other key is not the group's business (null).
 */
export function rovingTarget(count: number, current: number, key: string, orientation: Orientation = 'both'): number | null {
  if (count <= 0) return null
  const horizontal = orientation !== 'vertical'
  const vertical = orientation !== 'horizontal'
  const at = current >= 0 && current < count ? current : 0
  if ((key === 'ArrowRight' && horizontal) || (key === 'ArrowDown' && vertical)) return (at + 1) % count
  if ((key === 'ArrowLeft' && horizontal) || (key === 'ArrowUp' && vertical)) return (at - 1 + count) % count
  if (key === 'Home') return 0
  if (key === 'End') return count - 1
  return null
}

/**
 * The one item of such a group that Tab reaches: the selected one, or the
 * first when nothing is selected, so the group is never skipped.
 */
export function rovingTabStop(count: number, selected: number): number {
  return selected >= 0 && selected < count ? selected : 0
}

/**
 * Where Tab goes inside a surface that keeps the focus: null lets the browser
 * move it (the next stop is inside), a number is the stop to wrap to. Focus
 * that is on none of the stops (-1: on the surface itself, or outside) enters
 * at the first, or at the last going backwards.
 */
export function trapTarget(count: number, current: number, backwards: boolean): number | null {
  if (count <= 0) return null
  if (current < 0) return backwards ? count - 1 : 0
  if (!backwards && current === count - 1) return 0
  if (backwards && current === 0) return count - 1
  return null
}

/**
 * After a navigation the focus is lost when the link that was followed is no
 * longer on the page: the browser then starts over at the top of the document.
 * A link that stays (the sidebar, a tab) keeps it.
 */
export function focusWasLost(active: { tagName: string, isConnected: boolean } | null): boolean {
  return active === null || !active.isConnected || active.tagName === 'BODY' || active.tagName === 'HTML'
}
