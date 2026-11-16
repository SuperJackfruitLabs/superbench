package quotaoperationjournal

import (
	"context"
	"os"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

// Config is trusted fixed host policy, never an operation request or format flag.
// CheckControl validates metadata-independent custody; CheckReady must read the
// actual bound allocation header, owner anchor and mounted-policy authority.
type Config struct {
	Directory    string
	CheckControl func(context.Context) error
	CheckReady   func(context.Context) (string, error)
}

type FreshConfig struct {
	Control Config
	// Authorize proves independent first-install provenance, including absence of
	// old authorized controllers and allocation ownership. It is not readiness.
	Authorize func(context.Context) error
}

// Control is an immutable existing-format handle. No caller can construct usable
// authority by setting exported paths, checks or an invented operation identity.
type Control struct {
	config    Config
	owner     uint32
	authority string
	verify    func(context.Context) error
}

// Initializer carries no operation identity and can only claim a new ledger.
type Initializer struct {
	config FreshConfig
	owner  uint32
}

func NewInitializer(cfg FreshConfig) (*Initializer, error) { return newInitializer(cfg, 0) }

func newInitializer(cfg FreshConfig, owner uint32) (*Initializer, error) {
	if cfg.Authorize == nil || cfg.Control.CheckControl == nil || cfg.Control.CheckReady == nil || uint32(os.Geteuid()) != owner {
		return nil, ErrAuthority
	}
	if owner == 0 && (os.Getuid() != 0 || os.Getenv("SUDO_UID") != "") {
		return nil, ErrAuthority
	}
	return &Initializer{config: cfg, owner: owner}, nil
}

func (c *Control) check(ctx context.Context) error {
	if c == nil || c.verify == nil || c.config.CheckControl == nil || c.config.CheckReady == nil || !validDigest(c.authority) || uint32(os.Geteuid()) != c.owner {
		return ErrAuthority
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.verify(ctx)
}

// Run holds the retained operations.lock and format checks across a fixed
// generic host-storage action. It creates no operation receipt or denial proof.
func (c *Control) Run(ctx context.Context, effect func() error) error {
	if effect == nil {
		return ErrAuthority
	}
	if err := c.check(ctx); err != nil {
		return err
	}
	backend := journalStore{Directory: c.config.Directory, owner: c.owner, Check: func() error { return c.check(ctx) }}
	return backend.withinExisting(ctx, func(*Journal) error { return effect() })
}

// Operation binds a real immutable helper identity to an existing control.
// The supplied check must verify the actual original helper binary and scope.
func (c *Control) Operation(ctx context.Context, i quotaoperation.Identity, check func(quotaoperation.Identity) error) (*Store, error) {
	if !i.Valid() || check == nil {
		return nil, ErrAuthority
	}
	if err := c.check(ctx); err != nil {
		return nil, err
	}
	if i.Authority != c.authority {
		return nil, ErrAuthority
	}
	if err := check(i); err != nil {
		return nil, err
	}
	frozen := *c
	return &Store{control: &frozen, identity: i, verify: check}, nil
}
