/**
 * When a tooltip shows and where: for a pointer that rests on its trigger, for
 * the keyboard's focus, and for a finger. Pure, so the rules are unit-tested;
 * UiTooltip applies them to elements.
 */

export interface TooltipState {
  /** A mouse is over the trigger or over the bubble itself. */
  hover: boolean
  /** The trigger has the focus, and it came by keyboard. */
  focus: boolean
  /** Shown by a tap: a finger neither hovers nor leaves. */
  pinned: boolean
  /** Sent away with Escape; it stays away until the pointer or the focus comes again. */
  dismissed: boolean
}

/** `enter` and `leave` are the mouse's, `tap` a finger's on the trigger, `outside` a press anywhere else. */
export type TooltipEvent = 'enter' | 'leave' | 'focus' | 'blur' | 'tap' | 'outside' | 'escape'

export const TOOLTIP_AT_REST: TooltipState = { hover: false, focus: false, pinned: false, dismissed: false }

export function tooltipVisible(state: TooltipState): boolean {
  return !state.dismissed && (state.hover || state.focus || state.pinned)
}

/**
 * The state after an event. What shows it is independent of what else shows
 * it: the pointer leaving does not hide what the focus still shows. Escape
 * hides it whatever shows it, without the focus or the pointer having moved.
 */
export function tooltipReduce(state: TooltipState, event: TooltipEvent): TooltipState {
  switch (event) {
    case 'enter':
      return state.hover ? state : { ...state, hover: true, dismissed: false }
    case 'leave':
      return { ...state, hover: false }
    case 'focus':
      return state.focus ? state : { ...state, focus: true, dismissed: false }
    case 'blur':
      return { ...state, focus: false, pinned: false }
    case 'tap':
      return tooltipVisible(state) && state.pinned
        ? { ...state, pinned: false }
        : { ...state, pinned: true, dismissed: false }
    case 'outside':
      return { ...state, pinned: false, hover: false }
    case 'escape':
      return tooltipVisible(state) ? { ...state, pinned: false, dismissed: true } : state
  }
}

/**
 * Escape belongs to the tooltip only while it shows: then a dialog around it
 * stays open, and the next Escape is the dialog's.
 */
export function tooltipTakesEscape(state: TooltipState, key: string): boolean {
  return key === 'Escape' && tooltipVisible(state)
}

/** Only a mouse hovers: a finger's pointerenter is followed by its leave at once, and its tap is the click. */
export function pointerHovers(pointerType: string): boolean {
  return pointerType === 'mouse'
}

export function pointerTaps(pointerType: string): boolean {
  return pointerType === 'touch' || pointerType === 'pen'
}

/** A pointer that only crosses a row of triggers shows none of them. */
export const SHOW_DELAY_MS = 350
/** Long enough for the pointer to cross from the trigger onto the bubble. */
export const HIDE_DELAY_MS = 150

/** How long an event waits before it counts. Only the mouse waits; the keyboard and Escape are answered at once. */
export function tooltipDelay(event: TooltipEvent): number {
  if (event === 'enter') return SHOW_DELAY_MS
  if (event === 'leave') return HIDE_DELAY_MS
  return 0
}

export interface Box { left: number, top: number, width: number, height: number }
export interface Size { width: number, height: number }
export interface Placement { left: number, top: number, side: 'above' | 'below' }

/** Between the trigger and the bubble. */
export const TOOLTIP_GAP = 6
/** Between the bubble and the edge of the viewport. */
export const TOOLTIP_MARGIN = 8

/**
 * Where the bubble goes, in viewport coordinates: above the trigger and
 * centered on it, below when there is no room above, and pushed sideways as
 * far as it takes to stay on the screen. A bubble too tall for either side
 * takes the side with more room and is kept below the top edge, so that its
 * first line can be read.
 */
export function placeTooltip(trigger: Box, bubble: Size, viewport: Size): Placement {
  const above = trigger.top - TOOLTIP_GAP - bubble.height
  const below = trigger.top + trigger.height + TOOLTIP_GAP
  const fitsAbove = above >= TOOLTIP_MARGIN
  const fitsBelow = below + bubble.height <= viewport.height - TOOLTIP_MARGIN
  const roomAbove = trigger.top
  const roomBelow = viewport.height - trigger.top - trigger.height
  const side = fitsAbove || (!fitsBelow && roomAbove >= roomBelow) ? 'above' : 'below'

  const maxLeft = Math.max(TOOLTIP_MARGIN, viewport.width - TOOLTIP_MARGIN - bubble.width)
  const centered = trigger.left + trigger.width / 2 - bubble.width / 2
  const maxTop = Math.max(TOOLTIP_MARGIN, viewport.height - TOOLTIP_MARGIN - bubble.height)
  return {
    left: Math.round(Math.min(Math.max(centered, TOOLTIP_MARGIN), maxLeft)),
    top: Math.round(Math.min(Math.max(side === 'above' ? above : below, TOOLTIP_MARGIN), maxTop)),
    side,
  }
}

export type ButtonMode = 'enabled' | 'disabled' | 'inert'

/**
 * How a button that is off is off. `disabled` is the native attribute: out of
 * the Tab order, and dropping the focus the moment it is set. `inert` is
 * aria-disabled with its clicks swallowed: still a stop of the Tab order, for
 * a button that is busy (the focus must not be lost when it is pressed) and
 * for one that is off for a reason (which could otherwise never be read).
 */
export function buttonMode(disabled: boolean, pending: boolean, hint: string | null | undefined): ButtonMode {
  if (pending) return 'inert'
  if (!disabled) return 'enabled'
  return hasText(hint) ? 'inert' : 'disabled'
}

export function hasText(text: string | null | undefined): text is string {
  return typeof text === 'string' && text.trim() !== ''
}

/**
 * Elements that take the focus by themselves. Anything else that explains
 * itself with a tooltip is made a stop of the Tab order, or the explanation
 * would be the pointer's alone.
 */
const FOCUSABLE_TAGS = new Set(['a', 'button', 'input', 'select', 'textarea', 'summary'])

export function needsTabStop(tag: string, text: string | null | undefined): boolean {
  return hasText(text) && !FOCUSABLE_TAGS.has(tag.toLowerCase())
}
