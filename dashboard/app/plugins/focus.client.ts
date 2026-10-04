import { focusWasLost } from '~/utils/focus'

/**
 * The control that has the focus can go away under it: a link that is
 * followed is replaced with its page, a row is removed by the answer to its
 * own button. The browser then holds the focus nowhere and the next Tab
 * starts at the top of the document. The page's content takes it instead.
 * A control that stays — a link of the sidebar, a tab — keeps the focus, and
 * the new page is announced by its title (NuxtRouteAnnouncer in app.vue). A
 * dialog looks after its own (UiDialog).
 */
export default defineNuxtPlugin(() => {
  let last: Element | null = null

  document.addEventListener('focusin', (event) => {
    last = event.target instanceof Element ? event.target : null
  })

  new MutationObserver(() => {
    if (!last || last.isConnected || !focusWasLost(document.activeElement)) return
    last = null
    if (!document.querySelector('dialog[open]')) document.getElementById('main')?.focus({ preventScroll: true })
  }).observe(document.body, { childList: true, subtree: true })
})
