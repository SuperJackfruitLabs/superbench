package mirrorstage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

type ownedRecord struct{ Name, Digest string }
type forgetting struct {
	Version    string
	Intent     Intent
	Root       Root
	Published  *Identity `json:",omitempty"`
	PlanDigest string
	Records    []ownedRecord
	StartedAt  time.Time
}

func (f forgetting) plan() Plan {
	return Plan{Version: PlanVersion, Intent: f.Intent, Root: f.Root, Published: f.Published, Digest: f.PlanDigest}
}
func (f forgetting) validate(id, digestValue string) error {
	if f.Version != PlanVersion || f.Intent.ID != id || f.PlanDigest != digestValue || !utc(f.StartedAt) || len(f.Records) < 3 {
		return errors.New("invalid collection metadata retirement")
	}
	p := f.plan()
	p.ObservedAt = f.StartedAt
	p.Tree = reap.Tree{Root: reap.Entry{Path: ".", Mode: f.Root.Identity.Mode, Device: f.Root.Identity.Device, Inode: f.Root.Identity.Inode, SHA256: digestEmpty}}
	p.Digest = p.digest()
	if _, e := p.Bytes(); e != nil {
		return e
	}
	names := map[string]bool{}
	for _, record := range f.Records {
		if names[record.Name] || !validHash(record.Digest, 32) {
			return errors.New("invalid owned metadata record")
		}
		names[record.Name] = true
		if record.Name != id+"-plan.json" && record.Name != id+"-removal.json" && record.Name != id+"-done.json" {
			suffix := strings.TrimSuffix(strings.TrimPrefix(record.Name, id+"-review-"), ".json")
			if record.Name != id+"-review-"+suffix+".json" || !validHash(suffix, 32) {
				return errors.New("unowned metadata path")
			}
		}
	}
	if !names[id+"-plan.json"] || !names[id+"-removal.json"] || !names[id+"-done.json"] || f.Records[0].Name != id+"-plan.json" {
		return errors.New("metadata retirement must disable approval first")
	}
	return nil
}

// ForgetCompleted is explicit retirement of this component's completed metadata.
// The durable marker pins every record; interrupted retries never adopt unknown
// files. Old Apply cannot reauthorize deletion after its source intent is retired.
func (m Manager) ForgetCompleted(ctx context.Context, id, digestValue string) error {
	if !validID(id) || !validHash(digestValue, 32) {
		return errors.New("collection id and digest required")
	}
	return m.within(ctx, func(o *operation) error { return o.forget(id, digestValue) })
}
func (o *operation) forget(id, digestValue string) error {
	name := id + "-forget.json"
	var f forgetting
	e := o.read(name, &f, MaxRecordBytes)
	if os.IsNotExist(e) {
		p, e := o.approved(id)
		if e != nil {
			return e
		}
		if p.Digest != digestValue {
			return errors.New("collection digest differs from completed approval")
		}
		done, e := o.done(*p)
		if e != nil {
			return e
		}
		if !done {
			return errors.New("collection is not complete")
		}
		removing, e := o.removing(*p)
		if e != nil {
			return e
		}
		if !removing {
			return errors.New("completed collection has no removal intent")
		}
		if e = o.absent(*p); e != nil {
			return e
		}
		if e = o.retireSource(*p); e != nil {
			return e
		}
		f = forgetting{Version: PlanVersion, Intent: p.Intent, Root: p.Root, Published: p.Published, PlanDigest: p.Digest, StartedAt: time.Now().UTC()}
		names := []string{p.planName(), p.removalName(), p.doneName()}
		directory, e := o.collection.root.Open(".")
		if e != nil {
			return e
		}
		entries, readErr := directory.ReadDir(MaxJournalEntries + 1)
		closeErr := directory.Close()
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if len(entries) > MaxJournalEntries {
			return errors.New("collection journal exceeds bound")
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), id+"-review-") {
				var review RootRemovalReview
				if e = o.read(entry.Name(), &review, MaxRootRemovalReview); e != nil {
					return e
				}
				if _, e = review.Bytes(); e != nil {
					return e
				}
				if review.ID != id || review.PlanDigest != p.Digest || entry.Name() != id+"-review-"+review.Digest+".json" {
					return errors.New("foreign root disposition record")
				}
				names = append(names, entry.Name())
			}
		}
		for _, recordName := range names {
			var raw json.RawMessage
			limit := MaxRecordBytes
			if recordName == p.planName() {
				limit = MaxPlan
			}
			if e = o.read(recordName, &raw, limit); e != nil {
				return e
			}
			f.Records = append(f.Records, ownedRecord{Name: recordName, Digest: digest(raw)})
		}
		if e = f.validate(id, digestValue); e != nil {
			return e
		}
		if e = o.guard(*p); e != nil {
			return e
		}
		if e = o.absent(*p); e != nil {
			return e
		}
		if e = o.capacity(1); e != nil {
			return e
		}
		if e = saveBound(o.collection, name, f); e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	if e = f.validate(id, digestValue); e != nil {
		return e
	}
	p := f.plan()
	for _, record := range f.Records {
		if e = o.guard(p); e != nil {
			return e
		}
		if e = o.absent(p); e != nil {
			return e
		}
		var raw json.RawMessage
		limit := MaxRecordBytes
		if record.Name == id+"-plan.json" {
			limit = MaxPlan
		}
		e = o.read(record.Name, &raw, limit)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if digest(raw) != record.Digest {
			return errors.New("owned metadata changed; preserve it")
		}
		if e = o.collection.root.Remove(record.Name); e != nil {
			return e
		}
		if e = o.collection.sync(); e != nil {
			return e
		}
	}
	if e = o.guard(p); e != nil {
		return e
	}
	if e = o.absent(p); e != nil {
		return e
	}
	if e = o.collection.root.Remove(name); e != nil {
		return e
	}
	return o.collection.sync()
}
