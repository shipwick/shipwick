import { describe, expect, it } from 'vitest'
import { AgentsConfigError, DEFAULT_SERVER, parseAgents, sessionCookieFor } from '../server/utils/agents'
import { clientAddress } from '../server/utils/forwarded'
import { agentBase, decideServer, wantedServer, withServer } from '../app/utils/servers'

describe('parseAgents', () => {
  it('knows the one agent of SHIPWICK_AGENT_URL, and the agent on loopback without it', () => {
    expect(parseAgents(undefined, 'http://agent:9000/')).toEqual([{ name: DEFAULT_SERVER, url: 'http://agent:9000' }])
    expect(parseAgents('', undefined)).toEqual([{ name: DEFAULT_SERVER, url: 'http://127.0.0.1:9000' }])
  })

  it('reads name=URL pairs separated by commas, spaces or lines, in the order written', () => {
    const agents = parseAgents('production=http://agent:9000, staging=https://agent.staging.example.com/\n  lab=http://10.0.0.7:9000', 'http://ignored:9000')
    expect(agents).toEqual([
      { name: 'production', url: 'http://agent:9000' },
      { name: 'staging', url: 'https://agent.staging.example.com' },
      { name: 'lab', url: 'http://10.0.0.7:9000' },
    ])
  })

  it('refuses a pair without a name, a name that cannot be in an address, a name used twice and a URL that is not one', () => {
    expect(() => parseAgents('http://agent:9000', undefined)).toThrow(/name=URL pair/)
    expect(() => parseAgents('Prod=http://agent:9000', undefined)).toThrow(/invalid server name "Prod"/)
    expect(() => parseAgents('a=http://x:1,a=http://y:1', undefined)).toThrow(/used twice/)
    expect(() => parseAgents('a=ftp://x', undefined)).toThrow(/must be http or https/)
    expect(() => parseAgents(undefined, 'not a url')).toThrow(AgentsConfigError)
  })

  it('never repeats the credentials of a URL in its complaint', () => {
    expect(() => parseAgents('a=ftp://user:hunter2@x', undefined)).toThrow(/^(?!.*hunter2).*$/)
  })
})

describe('sessionCookieFor', () => {
  it('keeps the cookie a single server always had, and gives each of several its own', () => {
    const one = parseAgents(undefined, 'http://agent:9000')
    expect(sessionCookieFor(one[0]!, one)).toBe('shipwick_session')
    const two = parseAgents('production=http://a:1,staging=http://b:1', undefined)
    expect(two.map(agent => sessionCookieFor(agent, two))).toEqual(['shipwick_session_production', 'shipwick_session_staging'])
  })
})

describe('clientAddress', () => {
  it('believes the last entry a reverse proxy on a private address reports', () => {
    expect(clientAddress('203.0.113.40', '172.18.0.2')).toBe('203.0.113.40')
    expect(clientAddress('10.1.1.1, 203.0.113.40', '::ffff:127.0.0.1')).toBe('203.0.113.40')
    expect(clientAddress(['198.51.100.7', '203.0.113.40'], '::1')).toBe('203.0.113.40')
  })

  it('ignores the header of a peer with a public address: that is the browser itself, and anyone can write a header', () => {
    expect(clientAddress('10.0.0.1', '198.51.100.24')).toBe('198.51.100.24')
    expect(clientAddress('1.2.3.4', '2001:db8::9f')).toBe('2001:db8::9f')
  })

  it('falls back to the peer when the header holds no address, and to nothing when there is neither', () => {
    expect(clientAddress(undefined, '::ffff:172.18.0.2')).toBe('172.18.0.2')
    expect(clientAddress('unknown, <script>', '127.0.0.1')).toBe('127.0.0.1')
    expect(clientAddress(undefined, undefined)).toBe('')
  })
})

describe('agentBase', () => {
  it('is the one proxy path for one server and names the server among several', () => {
    expect(agentBase('default', false)).toBe('/api/agent')
    expect(agentBase(null, true)).toBe('/api/agent')
    expect(agentBase('staging', true)).toBe('/api/servers/staging/agent')
  })
})

describe('decideServer', () => {
  const names = ['production', 'staging']
  const ask = (over: Partial<Parameters<typeof decideServer>[0]>) => decideServer({ path: '/applications', wanted: null, names, selected: null, remembered: null, ...over })

  it('changes nothing with one server, whatever the address says', () => {
    expect(ask({ names: ['default'], wanted: 'elsewhere' })).toEqual({ action: 'proceed', server: 'default' })
    expect(ask({ names: ['default'], path: '/servers' })).toEqual({ action: 'proceed', server: 'default' })
  })

  it('opens an address on the server it names', () => {
    expect(ask({ wanted: 'staging' })).toEqual({ action: 'proceed', server: 'staging' })
  })

  it('keeps a link inside the page on the page\'s server, the server\'s own page included', () => {
    expect(ask({ selected: 'staging' })).toEqual({ action: 'add', server: 'staging' })
    expect(ask({ path: '/servers', selected: 'staging' })).toEqual({ action: 'add', server: 'staging' })
  })

  it('lists the servers when /servers is opened afresh', () => {
    expect(ask({ path: '/servers', remembered: 'staging' })).toEqual({ action: 'list' })
  })

  it('opens an address without a server on the one used last, and asks when there is none', () => {
    expect(ask({ remembered: 'staging' })).toEqual({ action: 'add', server: 'staging' })
    expect(ask({ remembered: 'gone' })).toEqual({ action: 'choose' })
    expect(ask({})).toEqual({ action: 'choose' })
  })

  it('says so when a link is for a server the dashboard does not have', () => {
    expect(ask({ wanted: 'lab', selected: 'staging' })).toEqual({ action: 'unknown', server: 'lab' })
  })

  it('loads the page afresh when the address names another server than the page holds', () => {
    expect(ask({ wanted: 'production', selected: 'staging' })).toEqual({ action: 'reload' })
  })

  it('lets the sign-in page be opened for a server, for none, and not for an unknown one', () => {
    expect(ask({ path: '/login', wanted: 'staging' })).toEqual({ action: 'proceed', server: 'staging' })
    expect(ask({ path: '/login' })).toEqual({ action: 'proceed', server: null })
    expect(ask({ path: '/login', wanted: 'lab' })).toEqual({ action: 'proceed', server: null })
  })
})

describe('withServer and wantedServer', () => {
  it('adds the server to a path, keeping its query and hash, and leaves one that names a server alone', () => {
    expect(withServer('/', 'staging')).toBe('/?server=staging')
    expect(withServer('/deployments?application=web#top', 'staging')).toBe('/deployments?application=web&server=staging#top')
    expect(withServer('/logs?server=production', 'staging')).toBe('/logs?server=production')
    expect(withServer('/logs', null)).toBe('/logs')
  })

  it('reads one name out of a query value', () => {
    expect(wantedServer('staging')).toBe('staging')
    expect(wantedServer(['staging', 'production'])).toBe('staging')
    expect(wantedServer('')).toBeNull()
    expect(wantedServer(undefined)).toBeNull()
  })
})
