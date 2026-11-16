// Package quotaoperationjournal stores permanent fixed-helper replay identities.
// It performs no quota effects and does not establish caller or policy authority.
// Store.Check must revalidate the original helper's authority. The callback runs
// under the existing ledger-wide operations.lock, including any caller effects.
//
// Create handles a genuinely empty operations namespace only. Its indexed-v2
// directory is deliberately unknown to old helpers, so they refuse before any
// indexed admission. Interrupted creation is retained and never auto-initialized.
// Legacy migration is deliberately not implemented by this adapter foundation.
package quotaoperationjournal
