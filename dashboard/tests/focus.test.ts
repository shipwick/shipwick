import { describe, expect, it } from 'vitest'
import { TABBABLE, focusWasLost, rovingTabStop, rovingTarget, trapTarget } from '../app/utils/focus'

describe('a group that is one stop of the Tab order', () => {
  it('steps to the next item with the right and the down arrow, and wraps at the end', () => {
    expect(rovingTarget(3, 0, 'ArrowRight')).toBe(1)
    expect(rovingTarget(3, 1, 'ArrowDown')).toBe(2)
    expect(rovingTarget(3, 2, 'ArrowRight')).toBe(0)
  })

  it('steps back with the left and the up arrow, and wraps at the start', () => {
    expect(rovingTarget(3, 2, 'ArrowLeft')).toBe(1)
    expect(rovingTarget(3, 1, 'ArrowUp')).toBe(0)
    expect(rovingTarget(3, 0, 'ArrowLeft')).toBe(2)
  })

  it('jumps to the first item with Home and to the last with End', () => {
    expect(rovingTarget(4, 2, 'Home')).toBe(0)
    expect(rovingTarget(4, 0, 'End')).toBe(3)
  })

  it('leaves every other key to the page', () => {
    for (const key of ['Tab', 'Enter', ' ', 'Escape', 'a', 'PageDown']) expect(rovingTarget(3, 1, key)).toBeNull()
  })

  it('answers only the arrows of its orientation', () => {
    expect(rovingTarget(3, 0, 'ArrowDown', 'horizontal')).toBeNull()
    expect(rovingTarget(3, 0, 'ArrowRight', 'horizontal')).toBe(1)
    expect(rovingTarget(3, 0, 'ArrowRight', 'vertical')).toBeNull()
    expect(rovingTarget(3, 0, 'ArrowDown', 'vertical')).toBe(1)
  })

  it('starts from the first item when the focus is on none of them', () => {
    expect(rovingTarget(3, -1, 'ArrowRight')).toBe(1)
    expect(rovingTarget(3, 7, 'ArrowLeft')).toBe(2)
  })

  it('has nothing to move to when it is empty', () => {
    expect(rovingTarget(0, 0, 'ArrowRight')).toBeNull()
    expect(rovingTarget(0, -1, 'Home')).toBeNull()
  })

  it('is entered by Tab at the selected item', () => {
    expect(rovingTabStop(3, 2)).toBe(2)
  })

  it('is entered at the first item when nothing is selected, so Tab never skips it', () => {
    expect(rovingTabStop(3, -1)).toBe(0)
    expect(rovingTabStop(3, 3)).toBe(0)
  })
})

describe('a surface that keeps the focus', () => {
  it('wraps Tab from its last stop to its first', () => {
    expect(trapTarget(4, 3, false)).toBe(0)
  })

  it('wraps Shift+Tab from its first stop to its last', () => {
    expect(trapTarget(4, 0, true)).toBe(3)
  })

  it('lets the browser move between the stops in between', () => {
    expect(trapTarget(4, 1, false)).toBeNull()
    expect(trapTarget(4, 2, true)).toBeNull()
    expect(trapTarget(4, 0, false)).toBeNull()
    expect(trapTarget(4, 3, true)).toBeNull()
  })

  it('takes focus that is on none of its stops in at the first, or at the last going backwards', () => {
    expect(trapTarget(4, -1, false)).toBe(0)
    expect(trapTarget(4, -1, true)).toBe(3)
  })

  it('keeps the focus on its only stop', () => {
    expect(trapTarget(1, 0, false)).toBe(0)
    expect(trapTarget(1, 0, true)).toBe(0)
  })

  it('has nowhere to send the focus when it has no stop', () => {
    expect(trapTarget(0, -1, false)).toBeNull()
  })

  it('counts what Tab stops at, and neither what is disabled nor what was taken out of the order', () => {
    expect(TABBABLE).toContain('button:not([disabled])')
    expect(TABBABLE).toContain('[tabindex]:not([tabindex="-1"])')
    expect(TABBABLE).toContain('summary')
  })
})

describe('the focus after a navigation', () => {
  it('is lost when the link that was followed went with the old page', () => {
    expect(focusWasLost({ tagName: 'A', isConnected: false })).toBe(true)
  })

  it('is lost when nothing but the document has it', () => {
    expect(focusWasLost({ tagName: 'BODY', isConnected: true })).toBe(true)
    expect(focusWasLost({ tagName: 'HTML', isConnected: true })).toBe(true)
    expect(focusWasLost(null)).toBe(true)
  })

  it('is kept by a link that stays, such as one of the sidebar or a tab', () => {
    expect(focusWasLost({ tagName: 'A', isConnected: true })).toBe(false)
  })
})
