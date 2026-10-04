import { TABBABLE, focusWasLost, trapTarget } from '~/utils/focus'

/**
 * Keeps Tab inside a surface while it is open: from its last stop to its
 * first, and back with Shift. A modal <dialog> already makes the rest of the
 * page inert but lets Tab go on to the browser's own controls; this closes
 * the loop. For a surface that is not a <dialog> it is the whole trap.
 * Bind `onKeydown` to the surface's keydown.
 */
export function useFocusTrap(surface: Ref<HTMLElement | null>) {
  function stops(): HTMLElement[] {
    const all = [...(surface.value?.querySelectorAll<HTMLElement>(TABBABLE) ?? [])]
    return all.filter((el) => {
      if (el.getClientRects().length === 0) return false
      // Of a group of radios Tab reaches the chosen one only.
      if (el instanceof HTMLInputElement && el.type === 'radio' && !el.checked && el.name !== '') {
        return !all.some(other => other instanceof HTMLInputElement && other.type === 'radio' && other.name === el.name && other.checked)
      }
      return true
    })
  }

  function onKeydown(event: KeyboardEvent) {
    if (event.key !== 'Tab' || event.altKey || event.ctrlKey || event.metaKey) return
    const all = stops()
    const target = trapTarget(all.length, all.indexOf(document.activeElement as HTMLElement), event.shiftKey)
    if (target === null) return
    event.preventDefault()
    all[target]?.focus()
  }

  return { onKeydown }
}

/**
 * After a surface closed: the control that opened it has the focus again,
 * unless it is gone (a row that was removed) or cannot take it. Then the
 * page's content takes it, so the next Tab does not start at the top. Focus
 * that something took on purpose meanwhile (a result that appeared) stays.
 */
export function restoreFocus(opener: Element | null) {
  if (!focusWasLost(document.activeElement)) return
  if (opener instanceof HTMLElement && opener.isConnected) {
    opener.focus()
    if (document.activeElement === opener) return
  }
  document.getElementById('main')?.focus({ preventScroll: true })
}
