import { describe, expect, it } from 'vitest'
import { RECREATE_PROXY_COMMAND, describeNeighbourTable, proxyNetworksProblem } from '../app/utils/wiring'

describe('proxyNetworksProblem', () => {
  it('names the networks the proxy is not on and the command that puts it back', () => {
    const one = proxyNetworksProblem({ networks_missing: ['shipwick-control'] })
    expect(one).toContain('not on the shipwick-control network')
    expect(one).toContain(RECREATE_PROXY_COMMAND)
    expect(proxyNetworksProblem({ networks_missing: ['shipwick-services', 'shipwick-control'] })).toContain('not on the shipwick-services and shipwick-control networks')
  })

  it('says nothing when the proxy is on all of them, and nothing when the agent did not say', () => {
    expect(proxyNetworksProblem({ networks_missing: [] })).toBe('')
    expect(proxyNetworksProblem({})).toBe('')
    expect(proxyNetworksProblem(undefined)).toBe('')
  })
})

describe('describeNeighbourTable', () => {
  it('shows the table\'s size and warns from a quarter of it on, the proxy counted as a container', () => {
    expect(describeNeighbourTable({ containers: 12, kernel_limits: { neighbour_table: 1024 } })).toEqual({ size: '1,024 entries', warning: '' })
    // 255 containers + the proxy = 256, four times which is the table: the first count that warns.
    const warned = describeNeighbourTable({ containers: 255, kernel_limits: { neighbour_table: 1024 } })
    expect(warned?.warning).toContain('runs 256 containers')
    expect(warned?.warning).toContain('holds 1,024 entries')
    expect(warned?.warning).toContain('gc_thresh3')
    expect(describeNeighbourTable({ containers: 254, kernel_limits: { neighbour_table: 1024 } })?.warning).toBe('')
  })

  it('is nothing when the agent could not read the table: an agent in a container, or one before 0.9', () => {
    expect(describeNeighbourTable({ containers: 300 })).toBeNull()
    expect(describeNeighbourTable({ containers: 300, kernel_limits: { neighbour_table: 0 } })).toBeNull()
  })
})
