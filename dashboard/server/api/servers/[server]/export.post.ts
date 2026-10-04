/** POST /api/servers/<name>/export → an export of that server, held for the browser to fetch: server/utils/downloads.ts. */
export default defineEventHandler(event => startExportDownload(event, getRouterParam(event, 'server') ?? ''))
