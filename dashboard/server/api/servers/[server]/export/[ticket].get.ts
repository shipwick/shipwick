/** GET /api/servers/<name>/export/<ticket> → that export, piped into the browser's download. */
export default defineEventHandler(event => claimExportDownload(event, getRouterParam(event, 'server') ?? ''))
