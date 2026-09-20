/**
 * Where to go after signing in. Only same-site absolute paths are accepted, so
 * a crafted ?redirect= can never send someone to another origin.
 */
export function safeRedirect(value: unknown): string {
  if (typeof value !== 'string') return '/'
  if (!value.startsWith('/') || value.startsWith('//') || value.includes('\\')) return '/'
  if (value === '/login' || value.startsWith('/login?')) return '/'
  return value
}
