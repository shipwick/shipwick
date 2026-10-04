/**
 * A result that takes the place of the control which produced it — the new
 * token where the form was — takes the focus when it appears: a screen reader
 * reads it, and Tab goes on from it instead of from the top of the page.
 * The element needs tabindex="-1". What is there when the page opens is left
 * alone.
 */
export function useFocusWhenShown(target: Ref<HTMLElement | null>) {
  let mounted = false
  onMounted(() => nextTick(() => { mounted = true }))
  watch(target, (el) => {
    if (el && mounted) el.focus()
  }, { flush: 'post' })
}
