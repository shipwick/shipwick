/**
 * /api/agent/** → the agent's /api/v1/**, for a dashboard that serves one
 * server. With several configured the server is named in the address,
 * /api/servers/<name>/agent/**, and this one answers 400 SERVER_REQUIRED.
 * The proxy itself is server/utils/proxy.ts.
 */
export default defineEventHandler(event => proxyToAgent(event, null))
