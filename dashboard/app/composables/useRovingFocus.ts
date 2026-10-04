import type { Orientation } from '~/utils/focus'
import { rovingTarget } from '~/utils/focus'

/**
 * A group that is one stop of the Tab order — a radio group, a row of
 * choices: the arrow keys, Home and End move between its items. The component
 * gives the item `rovingTabStop` names tabindex 0 and the others -1, and
 * passes `onMove` when moving also chooses, as it does in a radio group.
 */
export function useRovingFocus(
  group: Ref<HTMLElement | null>,
  options: { selector: string, orientation?: Orientation, onMove?: (index: number) => void },
) {
  function onKeydown(event: KeyboardEvent) {
    if (event.altKey || event.ctrlKey || event.metaKey) return
    const items = [...(group.value?.querySelectorAll<HTMLElement>(options.selector) ?? [])]
    const current = items.indexOf(document.activeElement as HTMLElement)
    const target = rovingTarget(items.length, current, event.key, options.orientation)
    if (target === null) return
    event.preventDefault()
    items[target]?.focus()
    options.onMove?.(target)
  }

  return { onKeydown }
}
