import type { TooltipEvent } from '~/utils/tooltip'
import { TOOLTIP_AT_REST, placeTooltip, pointerHovers, pointerTaps, tooltipDelay, tooltipReduce, tooltipTakesEscape, tooltipVisible } from '~/utils/tooltip'

let escapeTaken = false

/**
 * True while the Escape that is being handled has just closed a tooltip. A
 * modal <dialog> hears of Escape through its own `cancel` event, not through
 * the keydown a tooltip can stop: it asks here before it closes.
 */
export function tooltipTookEscape(): boolean {
  return escapeTaken
}

/**
 * The behaviour of one tooltip: shown while a mouse rests on its trigger or on
 * the bubble, while the trigger has the keyboard's focus, and after a tap
 * until the next tap elsewhere; hidden by Escape without moving the focus.
 * The bubble is a popover where the browser has them: the top layer is above
 * a modal dialog and is clipped by no table or panel.
 */
export function useTooltip(bubble: Ref<HTMLElement | null>) {
  const state = shallowRef(TOOLTIP_AT_REST)
  const open = computed(() => tooltipVisible(state.value))
  /** What the trigger under the pointer says, where one tooltip serves many of them. */
  const said = ref('')

  let trigger: HTMLElement | null = null
  let timer: ReturnType<typeof setTimeout> | undefined
  let pointer = ''
  let inTopLayer = false

  function apply(event: TooltipEvent) {
    state.value = tooltipReduce(state.value, event)
  }

  // Only the mouse waits, and its coming and going replace each other: a
  // pointer back on the trigger or on the bubble before the delay is over has
  // never left.
  function send(event: TooltipEvent) {
    const delay = tooltipDelay(event)
    if (delay === 0) return apply(event)
    clearTimeout(timer)
    timer = setTimeout(() => apply(event), delay)
  }

  function place() {
    const el = bubble.value
    if (!el || !trigger) return
    const at = trigger.getBoundingClientRect()
    const size = el.getBoundingClientRect()
    const spot = placeTooltip(at, size, { width: document.documentElement.clientWidth, height: window.innerHeight })
    el.style.left = `${spot.left}px`
    el.style.top = `${spot.top}px`
  }

  function onKeydown(event: KeyboardEvent) {
    if (!tooltipTakesEscape(state.value, event.key)) return
    event.preventDefault()
    event.stopPropagation()
    escapeTaken = true
    setTimeout(() => { escapeTaken = false })
    send('escape')
  }

  function onPress(event: Event) {
    const target = event.target as Node | null
    if (target && (trigger?.contains(target) || bubble.value?.contains(target))) return
    send('outside')
  }

  function listen(on: boolean) {
    const change = on ? document.addEventListener : document.removeEventListener
    // Capturing: before a dialog, a menu or anything else that closes on Escape hears it.
    change.call(document, 'keydown', onKeydown as EventListener, true)
    change.call(document, 'pointerdown', onPress, true)
    // What scrolls may be any box around the trigger; the bubble follows it.
    change.call(document, 'scroll', place, true)
    if (on) window.addEventListener('resize', place)
    else window.removeEventListener('resize', place)
  }

  function lower() {
    if (inTopLayer && bubble.value?.isConnected) bubble.value.hidePopover()
    inTopLayer = false
  }

  watch([open, bubble], ([now, el]) => {
    listen(now)
    if (!el) inTopLayer = false
    if (!now || !el) return lower()
    if (typeof el.showPopover === 'function' && el.isConnected && !inTopLayer) {
      el.showPopover()
      inTopLayer = true
    }
    place()
  }, { flush: 'post' })

  onBeforeUnmount(() => {
    clearTimeout(timer)
    listen(false)
    lower()
  })

  /** Listeners of the trigger. */
  const on = {
    pointerenter(event: PointerEvent) {
      trigger = event.currentTarget as HTMLElement
      if (pointerHovers(event.pointerType)) send('enter')
    },
    pointerleave(event: PointerEvent) {
      if (pointerHovers(event.pointerType)) send('leave')
    },
    pointerdown(event: PointerEvent) {
      pointer = event.pointerType
    },
    click(event: MouseEvent) {
      trigger = event.currentTarget as HTMLElement
      if (pointerTaps(pointer)) {
        // The tap was for the tooltip, not for a row around it that opens on a click.
        event.stopPropagation()
        send('tap')
      }
      pointer = ''
    },
    focus(event: FocusEvent) {
      trigger = event.currentTarget as HTMLElement
      // A click focuses too; what it shows is the pointer's business.
      if (trigger.matches(':focus-visible')) send('focus')
    },
    blur() {
      send('blur')
    },
  }

  /** Moves to another of the triggers one tooltip serves; false where there is none under the event. */
  function turnTo(event: Event): boolean {
    const el = (event.target as Element | null)?.closest<HTMLElement>('[data-tooltip]') ?? null
    if (!el) return false
    if (el !== trigger) {
      trigger = el
      said.value = el.dataset.tooltip ?? ''
      if (open.value) void nextTick(place)
    }
    return true
  }

  /**
   * Listeners of an element whose descendants with `data-tooltip` are the
   * triggers: one tooltip for a list too long to give each row its own.
   */
  const onWithin = {
    pointerover(event: PointerEvent) {
      if (pointerHovers(event.pointerType) && turnTo(event)) send('enter')
    },
    pointerout(event: PointerEvent) {
      if (!pointerHovers(event.pointerType)) return
      const to = event.relatedTarget
      if (to instanceof Node && trigger?.contains(to)) return
      send('leave')
    },
    pointerdown(event: PointerEvent) {
      pointer = event.pointerType
    },
    click(event: MouseEvent) {
      const taps = pointerTaps(pointer)
      pointer = ''
      if (!taps) return
      const before = trigger
      if (!turnTo(event)) return
      event.stopPropagation()
      // A tap on another trigger moves what is shown; on the same one it puts it away.
      if (trigger === before || !open.value) send('tap')
    },
  }

  /** Listeners of the bubble: the pointer may move onto it and stay. */
  const onBubble = {
    pointerenter(event: PointerEvent) {
      if (pointerHovers(event.pointerType)) send('enter')
    },
    pointerleave(event: PointerEvent) {
      if (pointerHovers(event.pointerType)) send('leave')
    },
  }

  return { open, said, on, onWithin, onBubble, place }
}
