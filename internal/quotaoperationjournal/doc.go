// Package quotaoperationjournal stores permanent fixed-helper replay identities.
// It performs no quota effects and does not establish caller or policy authority.
// Store.Check must revalidate the original helper's authority. The callback runs
// under the existing ledger-wide operations.lock, including any caller effects.
//
// The internal creation primitive handles a genuinely empty operations namespace only. Its indexed-v2
// directory is deliberately unknown to old helpers, so they refuse before any
// indexed admission. Interrupted creation is retained and never auto-initialized.
// Legacy migration is deliberately not implemented by this adapter foundation.
// Production installation uses explicit InitializeFresh, which exclusively
// claims an independent permanent anchor before creating a new ledger. Existing
// helpers use OpenExisting; neither missing history nor an empty historical
// ledger grants permission to initialize. No command or installer is wired here.
package quotaoperationjournal
