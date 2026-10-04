import { readFileSync, readdirSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import type { TooltipEvent, TooltipState } from '../app/utils/tooltip'
import {
  HIDE_DELAY_MS,
  SHOW_DELAY_MS,
  TOOLTIP_AT_REST,
  TOOLTIP_GAP,
  TOOLTIP_MARGIN,
  buttonMode,
  needsTabStop,
  placeTooltip,
  pointerHovers,
  pointerTaps,
  tooltipDelay,
  tooltipReduce,
  tooltipTakesEscape,
  tooltipVisible,
} from '../app/utils/tooltip'

function after(...events: TooltipEvent[]): TooltipState {
  return events.reduce(tooltipReduce, TOOLTIP_AT_REST)
}

describe('when a tooltip shows', () => {
  it('is hidden until something asks for it', () => {
    expect(tooltipVisible(TOOLTIP_AT_REST)).toBe(false)
  })

  it('shows while a mouse is over its trigger and hides when it has left', () => {
    expect(tooltipVisible(after('enter'))).toBe(true)
    expect(tooltipVisible(after('enter', 'leave'))).toBe(false)
  })

  it('shows while its trigger has the keyboard\'s focus and hides when the focus moves on', () => {
    expect(tooltipVisible(after('focus'))).toBe(true)
    expect(tooltipVisible(after('focus', 'blur'))).toBe(false)
  })

  it('stays for the focus when the pointer leaves, and for the pointer when the focus leaves', () => {
    expect(tooltipVisible(after('focus', 'enter', 'leave'))).toBe(true)
    expect(tooltipVisible(after('enter', 'focus', 'blur'))).toBe(true)
  })

  it('shows on a tap and stays, since a finger neither hovers nor leaves', () => {
    expect(tooltipVisible(after('tap'))).toBe(true)
  })

  it('is put away by a second tap, by a tap elsewhere, and when the focus moves on', () => {
    expect(tooltipVisible(after('tap', 'tap'))).toBe(false)
    expect(tooltipVisible(after('tap', 'outside'))).toBe(false)
    expect(tooltipVisible(after('tap', 'blur'))).toBe(false)
  })

  it('keeps showing for the focus when something else is pressed without taking the focus', () => {
    expect(tooltipVisible(after('focus', 'outside'))).toBe(true)
  })

  it('is hidden by Escape whatever shows it, while the focus and the pointer stay where they are', () => {
    for (const shown of [after('enter'), after('focus'), after('tap'), after('focus', 'enter')]) {
      const dismissed = tooltipReduce(shown, 'escape')
      expect(tooltipVisible(dismissed)).toBe(false)
      expect(dismissed.focus).toBe(shown.focus)
      expect(dismissed.hover).toBe(shown.hover)
    }
  })

  it('stays away after Escape while the pointer only moves between the trigger and the bubble', () => {
    expect(tooltipVisible(after('enter', 'escape', 'enter'))).toBe(false)
  })

  it('comes back after Escape once the pointer or the focus has left and come again', () => {
    expect(tooltipVisible(after('enter', 'escape', 'leave', 'enter'))).toBe(true)
    expect(tooltipVisible(after('focus', 'escape', 'blur', 'focus'))).toBe(true)
    expect(tooltipVisible(after('focus', 'escape', 'enter'))).toBe(true)
    expect(tooltipVisible(after('focus', 'escape', 'tap'))).toBe(true)
  })

  it('does not change its state object for an event that changes nothing, so nothing is rendered again', () => {
    const hovered = after('enter')
    expect(tooltipReduce(hovered, 'enter')).toBe(hovered)
    expect(tooltipReduce(TOOLTIP_AT_REST, 'escape')).toBe(TOOLTIP_AT_REST)
  })
})

describe('Escape with a tooltip open', () => {
  it('is the tooltip\'s, so that a dialog around it stays open', () => {
    expect(tooltipTakesEscape(after('focus'), 'Escape')).toBe(true)
    expect(tooltipTakesEscape(after('enter'), 'Escape')).toBe(true)
  })

  it('is the dialog\'s again once the tooltip is hidden', () => {
    expect(tooltipTakesEscape(TOOLTIP_AT_REST, 'Escape')).toBe(false)
    expect(tooltipTakesEscape(after('focus', 'escape'), 'Escape')).toBe(false)
    expect(tooltipTakesEscape(after('enter', 'leave'), 'Escape')).toBe(false)
  })

  it('leaves every other key alone', () => {
    for (const key of ['Enter', ' ', 'Tab', 'Esc', 'ArrowDown']) expect(tooltipTakesEscape(after('focus'), key)).toBe(false)
  })
})

describe('what a pointer does to a tooltip', () => {
  it('hovers only when it is a mouse', () => {
    expect(pointerHovers('mouse')).toBe(true)
    expect(pointerHovers('touch')).toBe(false)
    expect(pointerHovers('pen')).toBe(false)
  })

  it('taps when it is a finger or a pen, and not when it is a mouse or the keyboard\'s Enter', () => {
    expect(pointerTaps('touch')).toBe(true)
    expect(pointerTaps('pen')).toBe(true)
    expect(pointerTaps('mouse')).toBe(false)
    expect(pointerTaps('')).toBe(false)
  })

  it('waits before it shows, so that crossing a table shows nothing', () => {
    expect(tooltipDelay('enter')).toBe(SHOW_DELAY_MS)
    expect(SHOW_DELAY_MS).toBeGreaterThan(0)
  })

  it('waits before it hides, so that the pointer can move onto the bubble', () => {
    expect(tooltipDelay('leave')).toBe(HIDE_DELAY_MS)
    expect(HIDE_DELAY_MS).toBeGreaterThan(0)
  })

  it('answers the keyboard, a tap and Escape at once', () => {
    for (const event of ['focus', 'blur', 'tap', 'outside', 'escape'] as const) expect(tooltipDelay(event)).toBe(0)
  })
})

describe('where a tooltip goes', () => {
  const desktop = { width: 1280, height: 800 }
  const phone = { width: 360, height: 640 }
  const bubble = { width: 200, height: 30 }

  it('stands above its trigger, centered on it, a gap away', () => {
    const spot = placeTooltip({ left: 500, top: 400, width: 100, height: 20 }, bubble, desktop)
    expect(spot).toEqual({ left: 450, top: 400 - TOOLTIP_GAP - 30, side: 'above' })
  })

  it('goes below a trigger at the top of the window', () => {
    const spot = placeTooltip({ left: 500, top: 10, width: 100, height: 20 }, bubble, desktop)
    expect(spot.side).toBe('below')
    expect(spot.top).toBe(10 + 20 + TOOLTIP_GAP)
  })

  it('is pushed right at the left edge and left at the right edge instead of leaving the window', () => {
    expect(placeTooltip({ left: 4, top: 400, width: 40, height: 20 }, bubble, desktop).left).toBe(TOOLTIP_MARGIN)
    expect(placeTooltip({ left: 1240, top: 400, width: 40, height: 20 }, bubble, desktop).left).toBe(1280 - TOOLTIP_MARGIN - 200)
  })

  it('stays inside a phone\'s screen with a bubble nearly as wide as the screen', () => {
    const wide = { width: 344, height: 72 }
    for (const left of [0, 150, 330]) {
      const spot = placeTooltip({ left, top: 300, width: 30, height: 18 }, wide, phone)
      expect(spot.left).toBeGreaterThanOrEqual(TOOLTIP_MARGIN)
      expect(spot.left + wide.width).toBeLessThanOrEqual(phone.width - TOOLTIP_MARGIN)
      expect(spot.top).toBeGreaterThanOrEqual(TOOLTIP_MARGIN)
      expect(spot.top + wide.height).toBeLessThanOrEqual(phone.height - TOOLTIP_MARGIN)
    }
  })

  it('takes the side with more room when it fits on neither, and keeps its first line on the screen', () => {
    const tall = { width: 200, height: 560 }
    const low = placeTooltip({ left: 100, top: 480, width: 60, height: 20 }, tall, phone)
    expect(low.side).toBe('above')
    expect(low.top).toBe(TOOLTIP_MARGIN)
    const high = placeTooltip({ left: 100, top: 100, width: 60, height: 20 }, tall, phone)
    expect(high.side).toBe('below')
    expect(high.top).toBeGreaterThanOrEqual(TOOLTIP_MARGIN)
  })

  it('does not cover the trigger it explains when it fits beside it', () => {
    const trigger = { left: 100, top: 20, width: 60, height: 20 }
    const spot = placeTooltip(trigger, bubble, desktop)
    expect(spot.top >= trigger.top + trigger.height || spot.top + bubble.height <= trigger.top).toBe(true)
  })
})

describe('a button that is off', () => {
  it('is disabled natively when it has no reason to tell', () => {
    expect(buttonMode(true, false, undefined)).toBe('disabled')
    expect(buttonMode(true, false, '')).toBe('disabled')
    expect(buttonMode(true, false, '   ')).toBe('disabled')
    expect(buttonMode(true, false, null)).toBe('disabled')
  })

  it('stays a stop of the Tab order when it has a reason, so that the reason can be read', () => {
    expect(buttonMode(true, false, 'A deployment is in progress')).toBe('inert')
  })

  it('stays a stop of the Tab order while it is busy, so that the focus is not dropped when it is pressed', () => {
    expect(buttonMode(false, true, undefined)).toBe('inert')
    expect(buttonMode(true, true, undefined)).toBe('inert')
  })

  it('is an ordinary button when it is on, with or without a hint', () => {
    expect(buttonMode(false, false, undefined)).toBe('enabled')
    expect(buttonMode(false, false, 'Go back to an earlier deployment')).toBe('enabled')
  })
})

describe('what a tooltip makes of its trigger', () => {
  it('makes text that explains itself a stop of the Tab order', () => {
    expect(needsTabStop('span', '2026-03-01 10:00:00 UTC')).toBe(true)
    expect(needsTabStop('time', '2026-03-01 10:00:00 UTC')).toBe(true)
    expect(needsTabStop('dd', 'Recorded before tokens had names')).toBe(true)
  })

  it('adds no stop to what Tab reaches anyway', () => {
    for (const tag of ['a', 'button', 'summary', 'select', 'input', 'textarea', 'BUTTON']) expect(needsTabStop(tag, 'Why')).toBe(false)
  })

  it('adds no stop where there is nothing to say', () => {
    expect(needsTabStop('span', undefined)).toBe(false)
    expect(needsTabStop('span', null)).toBe(false)
    expect(needsTabStop('span', '')).toBe(false)
  })
})

// --- the templates ------------------------------------------------------------

const APP = fileURLToPath(new URL('../app', import.meta.url))

/**
 * Components whose `title` is a prop of their own: the heading they render.
 * On anything else `title` would end as the browser's tooltip, which only a
 * mouse can read.
 */
const TITLE_IS_A_PROP = ['ConfirmDialog', 'EmptyState', 'UiDialog', 'UiPanel']

interface Element { tag: string, attributes: string[], line: number }

function vueFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap(entry =>
    entry.isDirectory() ? vueFiles(join(dir, entry.name)) : entry.name.endsWith('.vue') ? [join(dir, entry.name)] : [])
}

/** The opening tags of a component's template, with the names of their attributes. */
function elements(source: string): Element[] {
  const start = source.indexOf('<template')
  const end = source.lastIndexOf('</template>')
  const found: Element[] = []
  let i = start
  while (start >= 0 && i < end) {
    if (source.startsWith('<!--', i)) {
      i = source.indexOf('-->', i) + 3
    }
    else if (source.startsWith('{{', i)) {
      i = source.indexOf('}}', i) + 2
    }
    else if (source[i] === '<' && /[A-Za-z]/.test(source[i + 1] ?? '')) {
      const line = source.slice(0, i).split('\n').length
      let j = i + 1
      while (/[\w.-]/.test(source[j] ?? '')) j++
      const tag = source.slice(i + 1, j)
      const attributes: string[] = []
      for (;;) {
        while (/[\s/]/.test(source[j] ?? '')) j++
        if (j >= end || source[j] === '>') break
        let k = j
        while (k < end && !/[\s=>]/.test(source[k]!)) k++
        attributes.push(source.slice(j, k))
        j = k
        if (source[j] === '=') {
          const quote = source[j + 1]!
          j = source.indexOf(quote, j + 2) + 1
        }
      }
      found.push({ tag, attributes, line })
      i = j + 1
    }
    else {
      i++
    }
  }
  return found
}

function titled(source: string): Element[] {
  return elements(source).filter(el =>
    el.attributes.some(name => name === 'title' || name === ':title' || name === 'v-bind:title')
    && !TITLE_IS_A_PROP.includes(el.tag))
}

describe('the templates of the dashboard', () => {
  it('are read tag by tag, with attributes that span lines and values that hold a >', () => {
    const source = [
      '<script setup lang="ts">const title = "x"</script>',
      '<template>',
      '  <!-- <span title="in a comment"> -->',
      '  <td',
      '    :class="a > b ? \'x\' : \'y\'"',
      '    :title="why"',
      '  >{{ a < b ? title : "" }}</td>',
      '  <UiPanel title="Backups"><span title="native" /></UiPanel>',
      '  <UiButton title="falls through to the button" />',
      '</template>',
    ].join('\n')
    expect(titled(source).map(el => `${el.tag}:${el.line}`)).toEqual(['td:4', 'span:8', 'UiButton:9'])
  })

  it('have files to read', () => {
    expect(vueFiles(APP).length).toBeGreaterThan(50)
  })

  it('say nothing in a title attribute, which only a mouse can read: a tooltip is a UiTooltip, or a UiButton\'s hint', () => {
    const offenders = vueFiles(APP).flatMap((file) => {
      const source = readFileSync(file, 'utf8')
      return titled(source).map(el => `${relative(APP, file).replaceAll('\\', '/')}:${el.line} <${el.tag}>`)
    })
    expect(offenders).toEqual([])
  })

  it('except only components that take their heading as `title`', () => {
    for (const name of TITLE_IS_A_PROP) {
      const source = readFileSync(join(APP, 'components', `${name}.vue`), 'utf8')
      const script = source.slice(0, source.indexOf('<template'))
      expect(script, name).toMatch(/defineProps<\{[^}]*\btitle\??:/)
    }
  })
})
