/** GET /api/export/<ticket> → the export a ticket stands for, piped into the browser's download. */
export default defineEventHandler(event => claimExportDownload(event, null))
