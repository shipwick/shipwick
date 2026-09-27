import { describe, expect, it } from 'vitest'
import {
  describeHealth,
  describeLogging,
  formatArgv,
  formatHostname,
  formatPublish,
  hostnamesOf,
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
