/**
 * POST /api/export {passphrase, applications?} → asks the one server's agent
 * for an export and answers a ticket; GET /api/export/<ticket> is the file.
 * With several servers: /api/servers/<name>/export. See server/utils/downloads.ts.
 */
export default defineEventHandler(event => startExportDownload(event, null))
