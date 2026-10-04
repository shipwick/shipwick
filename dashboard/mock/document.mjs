// A stored spec written back as the deploy.yaml that describes it, for the
// mock agent's GET /applications/:name/config: the agent's own order and
// style (pkg/spec/document.go) — blocks of top-level keys with an empty line
// between them, two spaces, argv on one line.
//
//   documentOf(spec, { env: { DATABASE_URL: 'postgres://app:${DB_PASSWORD}@db/app' } })
//     → { document: 'name: my-api\n\nimage: …', masked: ['env.LOG_LEVEL'] }
//
// A secret value is never written: one that was a reference to a stored
// secret is written as that reference, any other as the mask, and `masked`
// names those fields in document order.

export const MASK = '********'

const MASKED_NOTICE = `# A value shown as "${MASK}" was given when the application was deployed and
# is not handed out. Write it again, or store it on the server with
# shipwick secret set NAME and refer to it as \${NAME}. A deployment of this
# file is refused until every such value has been replaced.

`

const MASKED_COMMENT = '# not handed out: write the value again, or refer to a secret as ${NAME}'

const quoted = value => JSON.stringify(String(value))

/** A string that is not a secret: plain where YAML reads it back as the same string, quoted otherwise. */
function str(value) {
  const text = String(value)
  const plain = /^[A-Za-z0-9_/.$][A-Za-z0-9_/.:@+=,~${}%-]*$/.test(text)
    && !/^(true|false|yes|no|on|off|null|~)$/i.test(text)
    && !/^[-+.]?\d/.test(text)
    && !text.endsWith(':')
  return plain ? text : quoted(text)
}

const argv = args => `[${args.map(quoted).join(', ')}]`

/** 10m0s → 10m, 1h0m0s → 1h, 1m30s → 1m30s: a Go duration without the zero units it ends in. */
function duration(text) {
  let s = String(text)
  if (s.endsWith('m0s')) s = s.slice(0, -2)
  if (s.endsWith('h0m')) s = s.slice(0, -2)
  return s
}

/** A size in the largest unit that says it exactly. */
function memoryText(bytes) {
  for (const [name, size] of [['gb', 1024 ** 3], ['mb', 1024 ** 2], ['kb', 1024]]) {
    if (bytes % size === 0) return `${bytes / size}${name}`
  }
  return String(bytes)
}

/** A list of mappings, each entry's first key after the dash. */
function sequence(entries) {
  return entries.map(pairs => pairs.map(([key, value], i) => `${i === 0 ? '  - ' : '    '}${key}: ${value}`).join('\n')).join('\n')
}

export function documentOf(spec, references = {}) {
  const blocks = []
  const masked = []
  const block = lines => lines.length > 0 && blocks.push(`${lines.join('\n')}\n`)
  const secret = (reference, field) => {
    if (reference && /(?<!\$)\$\{[A-Za-z_][A-Za-z0-9_]*\}/.test(reference)) return str(reference)
    masked.push(field)
    return `"${MASK}" ${MASKED_COMMENT}`
  }

  block([`name: ${str(spec.name)}`])

  const source = []
  if (spec.static) {
    if (spec.static.fallback) source.push('static:', `  dir: ${str(spec.static.dir)}`, `  fallback: ${str(spec.static.fallback)}`)
    else source.push(`static: ${str(spec.static.dir)}`)
  }
  else if (spec.build) {
    if ((spec.build.dockerfile ?? 'Dockerfile') === 'Dockerfile') source.push(`build: ${str(spec.build.context)}`)
    else source.push('build:', `  context: ${str(spec.build.context)}`, `  dockerfile: ${str(spec.build.dockerfile)}`)
  }
  if (spec.image) source.push(`image: ${str(spec.image)}`)
  block(source)

  const process = []
  if (spec.entrypoint?.length) process.push(`entrypoint: ${argv(spec.entrypoint)}`)
  if (spec.command?.length) process.push(`command: ${argv(spec.command)}`)
  if (spec.user) process.push(`user: ${str(spec.user)}`)
  block(process)

  if (spec.init) block(['init: true'])
  if (spec.port) block([`port: ${spec.port}`])

  const hostnames = []
  if (spec.domain) hostnames.push(`domain: ${str(spec.domain)}`)
  if (spec.path) hostnames.push(`path: ${str(spec.path)}`)
  for (const key of ['aliases', 'redirects']) {
    if (spec[key]?.length) hostnames.push(`${key}:`, ...spec[key].map(host => `  - ${str(host)}`))
  }
  block(hostnames)

  if (!spec.static || (spec.replicas ?? 1) !== 1) block([`replicas: ${spec.replicas ?? 1}`])

  const env = Object.keys(spec.env ?? {}).sort()
  if (env.length > 0) block(['env:', ...env.map(name => `  ${name}: ${secret(references.env?.[name], `env.${name}`)}`)])

  if (spec.health) {
    const h = spec.health
    const check = h.command?.length ? `command: ${argv(h.command)}` : h.tcp ? `tcp: ${h.tcp}` : `path: ${str(h.path)}`
    block(['health:', `  ${check}`, `  interval: ${duration(h.interval)}`, `  timeout: ${duration(h.timeout)}`, `  retries: ${h.retries}`, ...(h.start_period ? [`  start_period: ${duration(h.start_period)}`] : [])])
  }

  if (spec.resources?.cpu || spec.resources?.memory_bytes) {
    block(['resources:', ...(spec.resources.cpu ? [`  cpu: ${spec.resources.cpu}`] : []), ...(spec.resources.memory_bytes ? [`  memory: ${memoryText(spec.resources.memory_bytes)}`] : [])])
  }

  const storage = []
  if (spec.volumes?.length) storage.push('volumes:', sequence(spec.volumes.map(v => [['name', str(v.name)], ['path', str(v.path)]])))
  if (spec.publish?.length) {
    storage.push('publish:', sequence(spec.publish.map(p => [['port', p.port], ['host', p.host], ...(p.address ? [['address', str(p.address)]] : []), ['protocol', str(p.protocol)]])))
  }
  if (spec.deploy && (spec.deploy.strategy !== 'rolling' || spec.deploy.stop_timeout)) {
    storage.push('deploy:', `  strategy: ${str(spec.deploy.strategy)}`, ...(spec.deploy.stop_timeout ? [`  stop_timeout: ${duration(spec.deploy.stop_timeout)}`] : []))
  }
  block(storage)

  if (spec.pre_deploy) block(['pre_deploy:', `  command: ${argv(spec.pre_deploy.command)}`, `  timeout: ${duration(spec.pre_deploy.timeout)}`])
  if (spec.jobs?.length) {
    block(['jobs:', sequence(spec.jobs.map(j => [['name', str(j.name)], ['schedule', quoted(j.schedule)], ['command', argv(j.command)], ['timeout', duration(j.timeout)]]))])
  }

  if (spec.logging) {
    const options = Object.keys(spec.logging.options ?? {}).sort()
    block(['logging:', `  driver: ${str(spec.logging.driver)}`, ...(options.length ? ['  options:', ...options.map(k => `    ${k}: ${str(spec.logging.options[k])}`)] : [])])
  }

  if (spec.proxy) {
    const p = spec.proxy
    const lines = ['proxy:']
    if (p.strip_prefix) lines.push('  strip_prefix: true')
    const headers = Object.keys(p.headers ?? {}).sort()
    if (headers.length) lines.push('  headers:', ...headers.map(k => `    ${k}: ${str(p.headers[k])}`))
    if (p.basic_auth?.length) {
      lines.push('  basic_auth:')
      for (const [i, account] of p.basic_auth.entries()) {
        const pairs = [...(account.path ? [['path', str(account.path)]] : []), ['username', str(account.username)], ['password', secret(references.basic_auth?.[i], `proxy.basic_auth[${i}].password`)]]
        lines.push(...pairs.map(([key, value], n) => `${n === 0 ? '    - ' : '      '}${key}: ${value}`))
      }
    }
    if (p.redirects?.length) {
      lines.push('  redirects:')
      for (const r of p.redirects) lines.push(`    - from: ${str(r.from)}`, `      to: ${str(r.to)}`, `      status: ${r.status}`)
    }
    if (lines.length > 1) block(lines)
  }

  if (spec.backups) {
    const b = spec.backups
    const lines = ['backups:', `  schedule: ${quoted(b.schedule)}`, `  keep: ${b.keep}`]
    if (b.before?.length) {
      lines.push(`  before: ${argv(b.before)}`)
      if (b.before_timeout) lines.push(`  before_timeout: ${duration(b.before_timeout)}`)
      if (b.before_in) lines.push(`  before_in: ${str(b.before_in)}`)
    }
    if (b.stop) lines.push('  stop: true')
    block(lines)
  }

  if (!spec.static || spec.restart?.policy !== 'always') block(['restart:', `  policy: ${str(spec.restart?.policy ?? 'always')}`])

  return { document: (masked.length > 0 ? MASKED_NOTICE : '') + blocks.join('\n'), masked }
}

/** The references of a document that has not had its values masked yet: every env value and basic-auth password with a `${NAME}` in it. */
export function referencesOf(body) {
  const has = value => typeof value === 'string' && /(?<!\$)\$\{[A-Za-z_][A-Za-z0-9_]*\}/.test(value)
  const references = { env: {}, basic_auth: {} }
  for (const [name, value] of Object.entries(body?.env ?? {})) if (has(value)) references.env[name] = value
  for (const [i, account] of (body?.proxy?.basic_auth ?? []).entries()) if (has(account?.password)) references.basic_auth[i] = account.password
  return references
}

/** The fields of a document whose value is the mask, in the order the agent reports them. */
export function maskedFields(body) {
  const fields = Object.entries(body?.env ?? {}).filter(([, value]) => value === MASK).map(([name]) => `env.${name}`).sort()
  for (const [i, account] of (body?.proxy?.basic_auth ?? []).entries()) if (account?.password === MASK) fields.push(`proxy.basic_auth[${i}].password`)
  return fields
}
