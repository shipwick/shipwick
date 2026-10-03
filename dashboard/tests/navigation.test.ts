import { describe, expect, it } from 'vitest'
import { NAV_GROUPS, activeItem, isActive, movedPath } from '../app/utils/navigation'
import { activeTab, applicationPath, applicationTabs, serverTabs } from '../app/utils/tabs'

describe('the navigation', () => {
  it('lists every page once', () => {
    const paths = NAV_GROUPS.flatMap(g => g.items.map(i => i.to))
    expect(new Set(paths).size).toBe(paths.length)
    expect(paths).toContain('/settings/access')
  })

  it('names its groups, except the first', () => {
    expect(NAV_GROUPS.map(g => g.label)).toEqual(['', 'Server', 'Settings'])
  })

  it('marks the overview only on the overview', () => {
    const overview = NAV_GROUPS[0]!.items[0]!
    expect(isActive(overview, '/')).toBe(true)
    expect(isActive(overview, '/applications')).toBe(false)
  })

  it('marks an entry on its page and on the pages below it', () => {
    expect(activeItem('/applications')?.label).toBe('Applications')
    expect(activeItem('/applications/my-api/backups')?.label).toBe('Applications')
    expect(activeItem('/deployments/12')?.label).toBe('Deployments')
    expect(activeItem('/servers/backups')?.label).toBe('Status')
    expect(activeItem('/settings/access')?.label).toBe('Access')
  })

  it('does not mistake a longer name for a page below', () => {
    expect(activeItem('/logsearch')).toBeNull()
    expect(activeItem('/login')).toBeNull()
  })
})

describe('addresses that moved', () => {
  it('sends the old pages to where they are now', () => {
    expect(movedPath('/tokens')).toBe('/settings/access')
    expect(movedPath('/secrets')).toBe('/settings/secrets')
    expect(movedPath('/registries')).toBe('/settings/registries')
  })

  it('accepts a trailing slash', () => {
    expect(movedPath('/tokens/')).toBe('/settings/access')
  })

  it('sends the bare settings address to its first page and leaves the pages below alone', () => {
    expect(movedPath('/settings')).toBe('/settings/secrets')
    expect(movedPath('/settings/access')).toBeNull()
  })

  it('leaves everything else where it is', () => {
    for (const path of ['/', '/applications/my-api', '/servers', '/volumes', '/certificates', '/logs', '/secretsauce', '/tokens-of-mine']) {
      expect(movedPath(path)).toBeNull()
    }
  })
})

describe('the tabs of an application', () => {
  const keys = (subject: Parameters<typeof applicationTabs>[0]) => applicationTabs(subject).map(t => t.key)

  it('gives a container application everything but backups', () => {
    expect(keys({ name: 'my-api', static: false, volumes: false })).toEqual(['overview', 'metrics', 'logs', 'deployments', 'jobs', 'configuration'])
  })

  it('adds backups for an application with volumes', () => {
    expect(keys({ name: 'postgres', static: false, volumes: true })).toContain('backups')
  })

  it('leaves out what a static application does not have, and calls its metrics what they are', () => {
    const tabs = applicationTabs({ name: 'landing', static: true, volumes: false })
    expect(tabs.map(t => t.key)).toEqual(['overview', 'metrics', 'deployments', 'configuration'])
    expect(tabs[1]!.label).toBe('Traffic')
  })

  it('keeps the application page itself as the overview', () => {
    expect(applicationPath('my-api')).toBe('/applications/my-api')
    expect(applicationPath('my-api', 'backups')).toBe('/applications/my-api/backups')
  })
})

describe('the active tab', () => {
  const tabs = applicationTabs({ name: 'my-api', static: false, volumes: true })

  it('is the overview on the application page', () => {
    expect(activeTab(tabs, '/applications/my-api')?.key).toBe('overview')
    expect(activeTab(tabs, '/applications/my-api/')?.key).toBe('overview')
  })

  it('is the longest match, not the first', () => {
    expect(activeTab(tabs, '/applications/my-api/backups')?.key).toBe('backups')
    expect(activeTab(serverTabs(), '/servers/transfer')?.key).toBe('transfer')
    expect(activeTab(serverTabs(), '/servers')?.key).toBe('status')
  })

  it('is none on another page', () => {
    expect(activeTab(tabs, '/applications/my-api-2')).toBeNull()
  })
})

describe('the tabs of the server', () => {
  it('marks the standby tab with what waits there', () => {
    expect(serverTabs({ waiting: 3 }).find(t => t.key === 'transfer')?.badge).toBe(3)
    expect(serverTabs().find(t => t.key === 'transfer')?.badge).toBeNull()
  })
})
