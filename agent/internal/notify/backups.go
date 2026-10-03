package notify

// BackupFailed is sent when a scheduled backup — of an application's volumes
// or of the agent's own state — did not succeed. A backup taken by hand that
// fails tells the person who asked for it, and nobody else.
const BackupFailed = "backup.failed"
