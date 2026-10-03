/**
 * /api/servers/<name>/agent/** → that server's agent, /api/v1/**. The name is
 * one of those the dashboard was configured with; anything else is 404
 * UNKNOWN_SERVER, and no address is ever built from the request.
 */
export default defineEventHandler(event => proxyToAgent(event, getRouterParam(event, 'server') ?? ''))
