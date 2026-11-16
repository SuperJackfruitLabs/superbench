// Package quotaoperationjournal stores permanent fixed-helper replay identities.
// It performs no quota effects itself and does not establish caller or policy
// authority. Config callbacks must verify actual fixed host-storage authority.
// Control and operation-scoped Store callbacks hold the existing ledger-wide
// operations.lock across caller effects and final checks.
//
// The internal creation primitive handles a genuinely empty operations namespace.
// Its indexed-v2 directory is unknown to old typed helpers, so they refuse before
// indexed admission. It does not fence their generic administrative commands.
// Interrupted creation is retained and never auto-initialized.
// Legacy migration is deliberately not implemented by this adapter foundation.
// Initializer exclusively claims an independent anchor before creating a ledger,
// calls a fixed nonallocating bootstrap under the helper lock, and verifies actual
// readiness before creating its database. It has no operation identity. Existing
// helpers use OpenExisting; missing history and empty historical ledgers never
// grant initialization permission. No command or installer is wired here.
package quotaoperationjournal
