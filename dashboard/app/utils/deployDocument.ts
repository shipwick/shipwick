/**
 * What the dashboard does with a deploy.yaml in its editor. From 0.7 on the
 * agent takes a document as it is (POST /validate, POST /applications) and
 * reads the application's name itself; the page reads nothing out of it. What
 * is here is the rest: the values the agent masked in a document it handed
 * out, and, for an agent before 0.7 only, the name the address then needs.
 * Pure, so it is unit-tested. None of it is a YAML parser and none of it
 * judges the document: the agent validates it, and its answer is what the
 * page shows.
 */

/** What stands in the place of a value the agent does not hand out; a document that still carries it is refused. */
export const MASK = '********'

/** The variable a masked field is: "LOG_LEVEL" for `env.LOG_LEVEL`; "" for a field that is not a variable. */
export function maskedVariable(field: string): string {
  const match = /^env\.([A-Za-z_][A-Za-z0-9_]*)$/.exec(field)
  return match ? match[1]! : ''
}

/** The position of a masked account's password in `proxy.basic_auth`; -1 for any other field. */
function maskedAccount(field: string): number {
  const match = /^proxy\.basic_auth\[(\d+)\]\.password$/.exec(field)
  return match ? Number(match[1]) : -1
}

/** What a masked field is, in words: "LOG_LEVEL", "the password of account 1 under proxy.basic_auth". */
export function maskedLabel(field: string): string {
  const variable = maskedVariable(field)
  if (variable) return variable
  const account = maskedAccount(field)
  return account >= 0 ? `the password of account ${account + 1} under proxy.basic_auth` : field
}

/**
 * The name to store a masked value under as a secret, for the link to the
 * Secrets page: the variable's own, and for a password one that says what it
 * is. The document then refers to it as `${NAME}`.
 */
export function maskedSecretName(field: string): string {
  const variable = maskedVariable(field)
  if (variable) return variable
  const account = maskedAccount(field)
  if (account < 0) return ''
  return account === 0 ? 'PROXY_PASSWORD' : `PROXY_PASSWORD_${account + 1}`
}

const escapeRegExp = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

/**
 * Whether the text still holds the mask where `field` had it: the variable's
 * line, or the password line of that account. A document that was edited
 * beyond recognition answers false, and the agent says what it finds.
 */
export function stillMasked(text: string, field: string): boolean {
  const variable = maskedVariable(field)
  if (variable) return new RegExp(`(^|[\\s{,"'])${escapeRegExp(variable)}["']?[ \\t]*:[ \\t]*["']?\\*{8}`, 'm').test(text)
  const account = maskedAccount(field)
  if (account < 0) return false
  const passwords = [...text.matchAll(/(^|[\s{,"'-])password["']?[ \t]*:[ \t]*(.*)$/gm)]
  return passwords[account]?.[2]?.includes(MASK) ?? false
}

/** The masked fields of a handed-out document that the text has not replaced yet, in the agent's order. */
export function remainingMasks(text: string, masked: readonly string[]): string[] {
  return masked.filter(field => stillMasked(text, field))
}

/**
 * What a variable's entry in the top-level `env` block says: its line, and
 * the more deeply indented ones under it. "" when the block is not written
 * the way the agent writes it, or has no such variable.
 */
function envEntry(text: string, variable: string): string {
  const lines = text.split(/\r?\n/)
  const start = lines.findIndex(line => /^env[ \t]*:[ \t]*(#.*)?$/.test(line))
  if (start < 0) return ''
  let indent = -1
  let entry: string[] | null = null
  for (const line of lines.slice(start + 1)) {
    if (line.trim() === '') {
      entry?.push(line)
      continue
    }
    const depth = line.length - line.trimStart().length
    if (depth === 0) break
    if (indent < 0) indent = depth
    if (depth > indent) {
      entry?.push(line)
      continue
    }
    if (entry) break
    if (/^([A-Za-z_][A-Za-z0-9_]*)[ \t]*:/.exec(line.trimStart())?.[1] === variable) entry = [line]
  }
  return entry ? entry.join('\n').trimEnd() : ''
}

/**
 * The plain values of a handed-out document that the text still holds exactly
 * as they were handed out: what a deployment of the text may call plain
 * again. The agent takes the sender's word for what is no secret, and the
 * page gives it only for what the agent itself showed. A value that was
 * edited or typed here is somebody's input, which the page cannot vouch for;
 * the agent masks it from then on.
 */
export function keptPlain(text: string, document: string, plain: readonly string[]): string[] {
  return plain.filter((field) => {
    const variable = maskedVariable(field)
    if (variable === '') return false
    const was = envEntry(document, variable)
    return was !== '' && was === envEntry(text, variable)
  })
}

/** The keys of deploy.yaml that came with 0.8: an agent before it answers `unknown field "<key>"` for them, which reads like a typo and is not one. */
const KEYS_SINCE_08: readonly string[] = ['security']

/**
 * The keys the agent refused as unknown because it is older than they are.
 * Whether it is older is read from what it says of itself: an agent from 0.8
 * on describes its Docker daemon. Empty when the server has not answered yet,
 * and for a key the dashboard does not know either, which may be a typo.
 */
export function keysNewerThanAgent(fields: readonly { message: string }[], server: { docker?: unknown } | null | undefined): string[] {
  if (!server || server.docker !== undefined) return []
  const refused = fields.map(field => /^unknown field "([^"]+)"$/.exec(field.message)?.[1] ?? '')
  return KEYS_SINCE_08.filter(key => refused.includes(key))
}

/** Whether the document still describes a folder of files: the uploaded folder is then deployed again, by its digest. */
export function describesStatic(text: string): boolean {
  return /^static[ \t]*:/m.test(text) || /^\s*\{[\s\S]*"static"[ \t]*:/.test(text)
}

export interface DocumentInspection {
  /** The top-level `name`; "" when the document has none that can be read. */
  name: string
  /**
   * image: runs an image from a registry, which the agent pulls.
   * build: built from the project where `shipwick deploy` runs.
   * static: a folder of files, uploaded by `shipwick deploy`.
   */
  kind: 'image' | 'build' | 'static'
  /** Why the document cannot be sent as it is; "" when it can. */
  problem: string
}

const APPLICATION_NAME = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/

/** The agent refuses a larger document. */
export const MAX_DOCUMENT_BYTES = 64 * 1024

/**
 * Reads the name and the kind out of a document, for an agent before 0.7
 * only: it takes a document under its application's name
 * (POST /applications/:name/deploy), so the page has to find that name, and
 * it has no way to deploy a build or a folder from here.
 */
export function inspectDocument(text: string): DocumentInspection {
  const keys = text.trimStart().startsWith('{') ? jsonKeys(text) : yamlKeys(text)
  const name = keys.get('name') ?? ''
  const kind = keys.has('static') ? 'static' : keys.has('build') ? 'build' : 'image'

  let problem = ''
  if (text.trim() === '') problem = ''
  else if (new TextEncoder().encode(text).length > MAX_DOCUMENT_BYTES) problem = 'The document is larger than 64 KB, which is more than the agent reads.'
  else if (name === '') problem = 'The document has no name. Its first line is usually name: my-api.'
  else if (!APPLICATION_NAME.test(name)) problem = `"${name}" cannot be an application's name: lowercase letters, digits and dashes, e.g. my-api.`
  return { name, kind, problem }
}

/** The top-level keys of a YAML mapping written in block style, with the value of those that are plain scalars. */
function yamlKeys(text: string): Map<string, string> {
  const keys = new Map<string, string>()
  for (const line of text.split(/\r?\n/)) {
    // Top level only: a key at the start of a line. Indented lines belong to a block above.
    const match = /^(?:"([^"]+)"|'([^']+)'|([A-Za-z_][\w-]*))\s*:(?:\s+(.*))?$/.exec(line)
    if (!match) continue
    const key = match[1] ?? match[2] ?? match[3]!
    if (!keys.has(key)) keys.set(key, scalar(match[4] ?? ''))
  }
  return keys
}

/** A scalar as written after a key: quotes and a trailing comment removed. */
function scalar(raw: string): string {
  const value = raw.trim()
  const quoted = /^"((?:[^"\\]|\\.)*)"|^'((?:[^']|'')*)'/.exec(value)
  if (quoted) return quoted[1] ?? quoted[2] ?? ''
  return value.replace(/\s+#.*$/, '').trim()
}

function jsonKeys(text: string): Map<string, string> {
  const keys = new Map<string, string>()
  try {
    const value = JSON.parse(text) as unknown
    if (typeof value !== 'object' || value === null || Array.isArray(value)) return keys
    for (const [key, v] of Object.entries(value)) {
      if (v === null || v === undefined || v === '') continue
      keys.set(key, typeof v === 'string' ? v : '')
    }
  }
  catch {
    // Not JSON after all: the agent says what is wrong with it.
  }
  return keys
}

/** A document to start from: an image from a registry behind a domain, which is all this page deploys. */
export const EXAMPLE_DOCUMENT = `name: my-api
image: ghcr.io/company/my-api:1.0.0
port: 8080
domain: api.example.com
replicas: 2
env:
  LOG_LEVEL: info
  DATABASE_URL: postgres://app:\${DATABASE_PASSWORD}@postgres:5432/app
health:
  path: /health
resources:
  cpu: 1
  memory: 512mb
`

/** Why the CLI is needed, and the command, for a document this page cannot deploy; "" for one it can. */
export function cliOnlyReason(kind: DocumentInspection['kind']): string {
  if (kind === 'build') return 'This application is built from its project: the image is built where shipwick deploy runs and sent to the server. The dashboard has no project to build from.'
  if (kind === 'static') return 'This application is a folder of files, which shipwick deploy uploads from the project. The dashboard has no folder to upload.'
  return ''
}
