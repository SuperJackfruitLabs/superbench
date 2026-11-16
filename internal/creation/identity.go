package creation

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"regexp"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

var registrationID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func intentIdentity(c *Intent) error {
	if err := model.ValidateScalarLayout(&c.Bench); err != nil {
		return err
	}
	if c.Bench.Workspace != nil || c.Bench.Lease != nil || c.Version < 0 || c.Version > 1 || c.Version == 0 && c.Bench.RegistrationID != "" || c.Version == 1 && !registrationID.MatchString(c.Bench.RegistrationID) {
		return errors.New("invalid scalar creation intent identity version")
	}
	return nil
}

// Record owns the explicit legacy-intent upgrade. It pins identity before any
// allocation or materialization, and quota retries may never replace that ID.
func (s Files) pinRegistration(c *Intent) error {
	if c == nil {
		return errors.New("missing creation intent")
	}
	if err := intentIdentity(c); err != nil {
		return err
	}
	raw, err := os.ReadFile(s.JournalPath(c.Bench.Key))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		var old Intent
		if err = json.Unmarshal(raw, &old); err != nil {
			return err
		}
		if err = intentIdentity(&old); err != nil {
			return err
		}
		if !sameIntent(old, *c) {
			return errors.New("creation retry changed pinned intent")
		}
		if old.Bench.RegistrationID != "" && old.Bench.RegistrationID != c.Bench.RegistrationID {
			return errors.New("creation registration identity changed")
		}
	}
	if c.Version == 0 {
		c.Bench.RegistrationID, err = model.Nonce()
		if err != nil {
			return err
		}
		c.Version = 1
	}
	return nil
}

func sameIntent(old, next Intent) bool {
	if old.Source != next.Source || old.Base != next.Base || old.Recovering != next.Recovering || old.QuotaRequestPinned && !next.QuotaRequestPinned {
		return false
	}
	a, b := old.Bench, next.Bench
	if old.QuotaRequestPinned && a.RequestedQuotaBytes != b.RequestedQuotaBytes {
		return false
	}
	if a.CreatedFrom != "" && a.CreatedFrom != b.CreatedFrom || b.CreatedFrom != "" && b.CreatedFrom != next.Source {
		return false
	}
	if a.ProjectID != 0 && (a.ProjectID != b.ProjectID || a.QuotaBytes != b.QuotaBytes || a.QuotaInodes != 0 && a.QuotaInodes != b.QuotaInodes) {
		return false
	}
	a.RegistrationID = ""
	b.RegistrationID = ""
	a.CreatedFrom = ""
	b.CreatedFrom = ""
	a.RequestedQuotaBytes = 0
	b.RequestedQuotaBytes = 0
	a.ProjectID = 0
	b.ProjectID = 0
	a.QuotaBytes = 0
	b.QuotaBytes = 0
	a.QuotaInodes = 0
	b.QuotaInodes = 0
	return reflect.DeepEqual(a, b)
}
