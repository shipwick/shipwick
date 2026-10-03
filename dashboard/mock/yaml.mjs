// The part of YAML a deploy.yaml is written in, for the mock agent: block
// mappings and sequences by indentation, flow sequences and mappings on one
// line, plain and quoted scalars, comments. No anchors, tags, multi-line
// scalars or multiple documents; the real agent reads all of YAML.
//
//   parseYaml('name: my-api\nreplicas: 2\n') → { name: 'my-api', replicas: 2 }
//
// Throws an Error whose message starts with "yaml: line N:".

export function parseYaml(text) {
  const lines = []
  for (const [index, raw] of text.split(/\r?\n/).entries()) {
    if (/^\s*(#.*)?$/.test(raw) || /^---\s*$/.test(raw)) continue
    if (/^\s*\t/.test(raw)) throw new Error(`yaml: line ${index + 1}: found a tab where indentation is expected`)
    lines.push({ number: index + 1, indent: raw.length - raw.trimStart().length, text: stripComment(raw.trim()) })
  }
  if (lines.length === 0) return {}
  const state = { lines, at: 0 }
  const value = parseBlock(state, lines[0].indent)
  if (state.at < lines.length) throw new Error(`yaml: line ${lines[state.at].number}: did not find expected key`)
  return value
}

/** Cuts a trailing comment: a # after whitespace, outside quotes. */
function stripComment(text) {
  let quote = ''
  for (let i = 0; i < text.length; i++) {
    const c = text[i]
    if (quote) {
      if (c === quote && text[i - 1] !== '\\') quote = ''
    }
    else if (c === '"' || c === '\'') quote = c
    else if (c === '#' && (i === 0 || /\s/.test(text[i - 1]))) return text.slice(0, i).trimEnd()
  }
  return text
}

function parseBlock(state, indent) {
  const first = state.lines[state.at]
  return first.text.startsWith('- ') || first.text === '-' ? parseSequence(state, indent) : parseMapping(state, indent)
}

function parseSequence(state, indent) {
  const list = []
  while (state.at < state.lines.length) {
    const line = state.lines[state.at]
    if (line.indent !== indent || !(line.text.startsWith('- ') || line.text === '-')) break
    const rest = line.text === '-' ? '' : line.text.slice(2).trim()
    if (rest === '') {
      state.at++
      list.push(nested(state, indent))
    }
    else if (isKeyValue(rest)) {
      // "- name: x" opens a mapping whose further keys are indented to the first one.
      const inner = indent + (line.text.length - line.text.slice(2).trimStart().length)
      state.lines[state.at] = { number: line.number, indent: inner, text: rest }
      list.push(parseMapping(state, inner))
    }
    else {
      state.at++
      list.push(scalar(rest, line.number))
    }
  }
  return list
}

function parseMapping(state, indent) {
  const map = {}
  while (state.at < state.lines.length) {
    const line = state.lines[state.at]
    if (line.indent < indent) break
    if (line.indent > indent) throw new Error(`yaml: line ${line.number}: this line is indented further than the key above it has room for`)
    const match = /^(?:"((?:[^"\\]|\\.)*)"|'((?:[^']|'')*)'|([^:\s][^:]*?))\s*:(?:\s+(.*))?$/.exec(line.text)
    if (!match) {
      if (line.text.startsWith('- ')) break
      throw new Error(`yaml: line ${line.number}: could not find expected ':'`)
    }
    const key = match[1] ?? match[2] ?? match[3]
    if (Object.hasOwn(map, key)) throw new Error(`yaml: line ${line.number}: mapping key "${key}" already defined`)
    const rest = (match[4] ?? '').trim()
    state.at++
    if (rest !== '') map[key] = scalar(rest, line.number)
    else {
      const next = state.lines[state.at]
      // A sequence may sit at the indentation of its key.
      if (next && (next.indent > indent || (next.indent === indent && next.text.startsWith('- ')))) map[key] = parseBlock(state, next.indent)
      else map[key] = null
    }
  }
  return map
}

function nested(state, indent) {
  const next = state.lines[state.at]
  return next && next.indent > indent ? parseBlock(state, next.indent) : null
}

const isKeyValue = text => /^(?:"[^"]*"|'[^']*'|[^:\s"'[{][^:]*?)\s*:(\s|$)/.test(text)

function scalar(text, number) {
  if (text.startsWith('[') || text.startsWith('{')) return flow(text, number)
  if (text.startsWith('"')) {
    try {
      return JSON.parse(text)
    }
    catch {
      throw new Error(`yaml: line ${number}: found unexpected end of a quoted value`)
    }
  }
  if (text.startsWith('\'')) {
    if (!text.endsWith('\'') || text.length < 2) throw new Error(`yaml: line ${number}: found unexpected end of a quoted value`)
    return text.slice(1, -1).replace(/''/g, '\'')
  }
  if (text === 'null' || text === '~') return null
  if (text === 'true') return true
  if (text === 'false') return false
  if (/^-?\d+$/.test(text)) return Number(text)
  if (/^-?\d*\.\d+$/.test(text)) return Number(text)
  return text
}

/** A flow sequence or mapping on one line: [a, "b c", 3], {key: value}. */
function flow(text, number) {
  let at = 0
  const fail = () => new Error(`yaml: line ${number}: did not find expected ',' or the closing bracket`)
  const skip = () => {
    while (text[at] === ' ') at++
  }
  const item = (stop) => {
    skip()
    if (text[at] === '[' || text[at] === '{') return value()
    if (text[at] === '"' || text[at] === '\'') {
      const quote = text[at]
      let end = at + 1
      while (end < text.length && (text[end] !== quote || (quote === '"' && text[end - 1] === '\\'))) end++
      if (end >= text.length) throw fail()
      const token = text.slice(at, end + 1)
      at = end + 1
      return scalar(token, number)
    }
    let end = at
    while (end < text.length && !stop.includes(text[end])) end++
    const token = text.slice(at, end).trim()
    at = end
    return scalar(token, number)
  }
  const value = () => {
    skip()
    if (text[at] === '[') {
      at++
      const list = []
      skip()
      if (text[at] === ']') {
        at++
        return list
      }
      for (;;) {
        list.push(item(',]'))
        skip()
        if (text[at] === ',') at++
        else if (text[at] === ']') {
          at++
          return list
        }
        else throw fail()
      }
    }
    at++
    const map = {}
    skip()
    if (text[at] === '}') {
      at++
      return map
    }
    for (;;) {
      const key = item(':')
      skip()
      if (text[at] !== ':') throw fail()
      at++
      map[String(key)] = item(',}')
      skip()
      if (text[at] === ',') at++
      else if (text[at] === '}') {
        at++
        return map
      }
      else throw fail()
    }
  }
  const result = value()
  skip()
  if (at < text.length) throw fail()
  return result
}
