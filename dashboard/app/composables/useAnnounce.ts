import type { Spoken } from '~/utils/announce'
import { shouldAnnounce } from '~/utils/announce'

export type Urgency = 'polite' | 'assertive'

/** Long enough to be read, short enough that the sentence is not found later at the end of the page. */
const CLEAR_AFTER_MS = 8000

const last: Record<Urgency, Spoken | null> = { polite: null, assertive: null }
const timers: Record<Urgency, ReturnType<typeof setTimeout> | null> = { polite: null, assertive: null }

/**
 * Says a sentence to a screen reader through the two live regions that
 * LiveAnnouncer keeps on every page: `polite` waits for the reader to finish
 * (a step of a deployment, "Copied"), `assertive` interrupts (the page lost
 * its data). What is on screen stays as it is; this only adds the voice.
 */
export function useAnnounce() {
  const regions: Record<Urgency, Ref<string>> = {
    polite: useState('announcer:polite', () => ''),
    assertive: useState('announcer:assertive', () => ''),
  }

  function announce(text: string, urgency: Urgency = 'polite') {
    const now = Date.now()
    if (!shouldAnnounce(last[urgency], text, now)) return
    last[urgency] = { text, at: now }
    const region = regions[urgency]
    // A region that already holds this sentence would not change, and nothing would be said: empty it first.
    region.value = ''
    if (timers[urgency]) clearTimeout(timers[urgency])
    timers[urgency] = setTimeout(() => {
      region.value = text
      timers[urgency] = setTimeout(() => { region.value = '' }, CLEAR_AFTER_MS)
    }, 60)
  }

  return { announce, polite: regions.polite, assertive: regions.assertive }
}
