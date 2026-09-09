package application

import "invoice-system/backend/internal/securefields"

// PlatformBindingSubjectIndex derives the external_accounts.
// external_subject_hmac that a platform-scoped binding must carry.
//
// external_accounts is UNIQUE NULLS NOT DISTINCT (source_instance_id,
// external_subject_hmac), so at most ONE row per source may leave that column
// NULL. The RC55 production canary found this the hard way: the first
// platform-password binding used up the slot and the second real user's first
// login failed with SQLSTATE 23505. Every platform binding therefore gets a
// deterministic blind index in its own namespace -- distinct from the
// projection's "external-oidc/<source>" namespace, so a password binding can
// never collide with an OIDC-subject binding.
//
// Exported (XM-INV-SHADOW-BINDING) because an operator-attested binding made
// by cmd/account-bind is not written through Service.BindExternalAccount and
// must derive the same value itself. The namespace string exists once, here:
// a bind whose index came from a second, drifted copy of this expression
// would not fail loudly -- it would simply take the NULL-equivalent slot away
// from somebody, months later.
func PlatformBindingSubjectIndex(keys securefields.Keyring, sourceInstanceID, externalUserID string) (string, error) {
	return keys.BlindIndex("external-platform/"+sourceInstanceID, externalUserID)
}

// SourceDependencyKeyHMAC derives the source_ingest_events.
// dependency_key_hmac that parks a fact on a not-yet-satisfied dependency and,
// read back, is what a wake matches on. Exported for the same reason as
// PlatformBindingSubjectIndex: cmd/account-bind must wake exactly the rows
// the ingest pipeline parked, and a wake computed from a drifted copy of this
// namespace matches nothing at all while reporting a perfectly successful
// zero rows released.
func SourceDependencyKeyHMAC(keys securefields.Keyring, kind, sourceInstanceID, value string) (string, error) {
	return keys.BlindIndex("source-dependency/"+kind, sourceInstanceID+"\n"+value)
}

// UserEmailAAD is the additional authenticated data binding an
// invoice_users.email_ciphertext to the identity that owns it. Exported for
// the same reason as the two blind indexes above: cmd/account-bind writes that
// column for a shadow identity and must produce a ciphertext the api can later
// decrypt. A drifted copy of this string fails nowhere near the mistake -- the
// encrypt succeeds, and the customer's first real login is where the decrypt
// fails.
//
// auth/identity_migrate.go's userEmailAADForMigration is a third spelling of
// the same string and is deliberately left in place: package auth is the
// invoice service's independent identity boundary and does not import
// application (auth/doc.go), and that copy carries its own comment saying so.
// Collapsing it too would mean moving this string into a new leaf package both
// can import -- a larger change than this slice should make. Recorded as a
// follow-up in docs/handoffs/XM-INV-SHADOW-BINDING.md.
func UserEmailAAD(issuer, subject string) string {
	return "invoice-user-email\n" + issuer + "\n" + subject
}
