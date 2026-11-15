package protocolv2

// Authority is independent producer evidence, never a request or wire field.
// Nil InventoryKeys means unknown; a nonnil empty slice proves an empty host.
// RootPath is the exact independently authorized path, not a client suggestion.
type Authority struct {
	GrantToken    string
	Fenced        bool
	InventoryKeys []string
	RootPath      string
}

// ValidateAuthority supplements client-observable validation with exact private
// producer evidence. It cannot make an otherwise invalid response acceptable.
func ValidateAuthority(response Response, authority Authority) error {
	if mutation := responseMutation(response.Result); mutation != nil {
		if mutation.RootPath != "" && mutation.RootPath != authority.RootPath {
			return ErrResponse
		}
		if mutation.Grant != nil && (authority.GrantToken == "" || mutation.Grant.LeaseToken != authority.GrantToken || response.Operation == "stop_admin" && !authority.Fenced) {
			return ErrResponse
		}
	}
	if status, ok := response.Result.(*Status); ok && authority.InventoryKeys != nil {
		if status == nil {
			return ErrResponse
		}
		if len(status.Benches) != len(authority.InventoryKeys) {
			return ErrResponse
		}
		for i, key := range authority.InventoryKeys {
			if inventoryKey(status.Benches[i]) != key || i > 0 && authority.InventoryKeys[i-1] >= key {
				return ErrResponse
			}
		}
	}
	return nil
}
