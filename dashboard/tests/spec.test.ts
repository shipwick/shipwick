import { describe, expect, it } from 'vitest'
import {
  addressOf,
  applicationUrl,
  describeBuild,
  describeHealth,
  describeLogging,
  describeStatic,
  formatArgv,
  formatHostname,
  formatPathRedirect,
  formatPublish,
  hasProxySettings,
  hostnamesOf,
  isLocalImage,
  isWildcard,
  shippedLogDriver,
} from '../app/utils/spec'

describe('formatArgv', () => {
  it('joins arguments with spaces', () => {
    expect(formatArgv(['pg_isready', '-U', 'postgres'])).toBe('pg_isready -U postgres')
    expect(formatArgv(['dotnet'])).toBe('dotnet')
  })

  it('quotes arguments containing spaces or quotes, and only those', () => {
    expect(formatArgv(['sh', '-c', 'echo hi'])).toBe('sh -c "echo hi"')
    expect(formatArgv(['--name', 'say "hi"'])).toBe('--name "say \\"hi\\""')
    expect(formatArgv(['a', ''])).toBe('a ""')
  })

  it('is empty without arguments', () => {
    expect(formatArgv([])).toBe('')
    expect(formatArgv(undefined)).toBe('')
  })
})

describe('describeHealth', () => {
  const schedule = { interval: '10s', timeout: '3s', retries: 3 }

  it('renders each kind the way the CLI does', () => {
    expect(describeHealth({ path: '/health', ...schedule })).toEqual({ check: 'GET /health', schedule: 'every 10s (timeout 3s, 3 retries)' })
    expect(describeHealth({ tcp: 5432, ...schedule })?.check).toBe('TCP :5432')
    expect(describeHealth({ command: ['pg_isready', '-U', 'postgres'], ...schedule })?.check).toBe('command pg_isready -U postgres')
  })

  it('counts one retry in the singular', () => {
    expect(describeHealth({ path: '/', interval: '30s', timeout: '5s', retries: 1 })?.schedule).toBe('every 30s (timeout 5s, 1 retry)')
  })

  it('is null without a health check', () => {
    expect(describeHealth(null)).toBeNull()
    expect(describeHealth(undefined)).toBeNull()
  })

  it('mentions the start period when a replica gets one, tidied the way Go prints it', () => {
    expect(describeHealth({ path: '/actuator/health', interval: '30s', timeout: '5s', retries: 30, start_period: '2m0s' })?.schedule)
      .toBe('every 30s (timeout 5s, 30 retries), after a 2m start period')
    expect(describeHealth({ path: '/', ...schedule, start_period: '1m30s' })?.schedule).toBe('every 10s (timeout 3s, 3 retries), after a 1m30s start period')
    expect(describeHealth({ path: '/', ...schedule, start_period: '1h0m0s' })?.schedule).toBe('every 10s (timeout 3s, 3 retries), after a 1h start period')
    expect(describeHealth({ path: '/', ...schedule, start_period: '45s' })?.schedule).toBe('every 10s (timeout 3s, 3 retries), after a 45s start period')
  })

  it('says nothing about a start period that is absent', () => {
    expect(describeHealth({ path: '/', ...schedule })?.schedule).toBe('every 10s (timeout 3s, 3 retries)')
  })
})

describe('describeStatic', () => {
  it('reads like the CLI\'s status line', () => {
    expect(describeStatic({ digest: 'sha256:3f2a', size_bytes: 3250000, files: 42 })).toBe('42 files, 3.1 MB, served by the proxy')
    expect(describeStatic({ digest: 'sha256:0', size_bytes: 512, files: 1 })).toBe('1 file, 512 B, served by the proxy')
  })

  it('is empty for a container deployment', () => {
    expect(describeStatic(undefined)).toBe('')
    expect(describeStatic(null)).toBe('')
  })
})

describe('build', () => {
  it('names the context and the Dockerfile the CLI builds from', () => {
    expect(describeBuild({ context: '.', dockerfile: 'Dockerfile' })).toBe('built by shipwick deploy from . (Dockerfile)')
    expect(describeBuild({ context: 'services/api', dockerfile: 'Dockerfile.prod' })).toBe('built by shipwick deploy from services/api (Dockerfile.prod)')
    expect(describeBuild(undefined)).toBe('')
  })

  it('recognizes an image the CLI sent, which no registry holds', () => {
    expect(isLocalImage('shipwick.local/my-api:20260927-153000-a1b2')).toBe(true)
    expect(isLocalImage('ghcr.io/acme/my-api:1.4.2')).toBe(false)
    expect(isLocalImage('')).toBe(false)
  })
})

describe('formatPublish', () => {
  it('names the container port, protocol and server port', () => {
    expect(formatPublish({ port: 5432, host: 15432, address: '10.0.0.5', protocol: 'tcp' })).toBe('5432/tcp → server port 15432 on 10.0.0.5')
  })
  it('omits the address when the port is bound on every address', () => {
    expect(formatPublish({ port: 5353, host: 5353, protocol: 'udp' })).toBe('5353/udp → server port 5353')
  })
})

describe('hostnamesOf', () => {
  it('lists the domain, then aliases, then redirects pointing at the domain', () => {
    const list = hostnamesOf({ domain: 'example.com', aliases: ['app.example.com'], redirects: ['www.example.com', 'example.net'] })
    expect(list.map(h => h.kind)).toEqual(['domain', 'alias', 'redirect', 'redirect'])
    expect(list.map(formatHostname)).toEqual(['example.com', 'app.example.com', 'www.example.com → example.com', 'example.net → example.com'])
  })

  it('is just the domain for most applications, and empty without one', () => {
    expect(hostnamesOf({ domain: 'api.example.com' }).map(formatHostname)).toEqual(['api.example.com'])
    expect(hostnamesOf({ domain: '' })).toEqual([])
    expect(hostnamesOf({ domain: undefined, aliases: ['x'] })).toEqual([])
  })

  it('carries the path on the domain and the aliases, and takes a redirect hostname whole', () => {
    const list = hostnamesOf({ domain: 'example.com', path: '/api', aliases: ['app.example.com'], redirects: ['www.example.com'] })
    expect(list.map(h => h.address)).toEqual(['example.com/api', 'app.example.com/api', 'www.example.com'])
    expect(list.map(h => h.url)).toEqual(['https://example.com/api', 'https://app.example.com/api', 'https://www.example.com'])
    expect(list.map(formatHostname)).toEqual(['example.com/api', 'app.example.com/api', 'www.example.com → example.com'])
  })

  it('does not make a link of a wildcard: it is a pattern, not an address', () => {
    const list = hostnamesOf({ domain: '*.example.com', aliases: ['example.com'] })
    expect(list[0]).toMatchObject({ host: '*.example.com', address: '*.example.com', url: null })
    expect(list[1]?.url).toBe('https://example.com')
  })
})

describe('addresses', () => {
  it('writes a hostname with the part of it an application serves', () => {
    expect(addressOf('example.com', '/api')).toBe('example.com/api')
    expect(addressOf('example.com', undefined)).toBe('example.com')
    expect(addressOf('example.com', '')).toBe('example.com')
  })

  it('links an application by domain and path, and not at all without a domain or for a wildcard', () => {
    expect(applicationUrl({ domain: 'example.com', path: '/docs' })).toBe('https://example.com/docs')
    expect(applicationUrl({ domain: 'api.example.com' })).toBe('https://api.example.com')
    expect(applicationUrl({ domain: '' })).toBeNull()
    expect(applicationUrl({ domain: '*.example.com' })).toBeNull()
    expect(isWildcard('*.example.com')).toBe(true)
    expect(isWildcard('example.com')).toBe(false)
  })
})

describe('the proxy block', () => {
  it('writes a redirect with its status', () => {
    expect(formatPathRedirect({ from: '/api/old', to: '/api/new', status: 308 })).toBe('/api/old → /api/new (308)')
  })

  it('knows a block that says nothing from one that does', () => {
    expect(hasProxySettings({})).toBe(false)
    expect(hasProxySettings({ proxy: {} })).toBe(false)
    expect(hasProxySettings({ proxy: { headers: {} } })).toBe(false)
    expect(hasProxySettings({ proxy: { strip_prefix: true } })).toBe(true)
    expect(hasProxySettings({ proxy: { headers: { 'X-Frame-Options': 'DENY' } } })).toBe(true)
    expect(hasProxySettings({ proxy: { basic_auth: [{ username: 'ops', password: '********' }] } })).toBe(true)
    expect(hasProxySettings(null)).toBe(false)
  })
})

describe('logging', () => {
  it('reports a remote driver and stays quiet for the local ones', () => {
    expect(shippedLogDriver({ logging: { driver: 'gelf', options: { 'gelf-address': 'udp://logs.example.com:12201' } } })).toBe('gelf')
    expect(shippedLogDriver({ logging: { driver: 'json-file' } })).toBeNull()
    expect(shippedLogDriver({ logging: { driver: 'local' } })).toBeNull()
    expect(shippedLogDriver({})).toBeNull()
    expect(shippedLogDriver(null)).toBeNull()
  })

  it('summarizes the driver and its option count like the CLI', () => {
    expect(describeLogging({ logging: { driver: 'gelf', options: { 'gelf-address': 'udp://x:1', 'tag': '{{.Name}}' } } })).toBe('gelf (2 options)')
    expect(describeLogging({ logging: { driver: 'syslog', options: { 'syslog-address': 'udp://x:1' } } })).toBe('syslog (1 option)')
    expect(describeLogging({ logging: { driver: 'journald' } })).toBe('journald')
    expect(describeLogging({})).toBe('')
  })
})
