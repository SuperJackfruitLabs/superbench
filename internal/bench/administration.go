package bench

// LocalAdministration serializes a local filesystem adapter with every host
// operation. Existing Git-control intents are reconciled and the state schema
// is validated before invoking the adapter. This grants no lease or execution.
func (s Store) LocalAdministration(fn func() error) error {
	return s.transaction(func(database) error { return fn() })
}
