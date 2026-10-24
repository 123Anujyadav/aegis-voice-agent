package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// Subscriber-id migration checks: OFFLINE DESIGN VALIDATION for ADR-0030
// D30.5. NOT WIRED, like the directory itself. These functions state the
// preconditions of each stage change, of a backup restore and of a
// pseudonym-key rotation as code, so the migration plan
// (docs/phase19/ADR-0030-D30.3-MIGRATION-PLAN.md) is checked by tests before
// anything is executed. Passing them validates the design, not a migration.

// SubscriberIDOrigin records how a mapping's id was chosen.
//
// D30.5 allows a rollback to stage 0 only while every row holds a legacy id,
// and a row's origin cannot be recomputed later: the number is never stored.
// The mapping therefore has to record it (the id_origin column the plan adds to
// D30.5's table).
type SubscriberIDOrigin int

const (
	// OriginLegacy is a legacy derived id, adopted at stage 2.
	OriginLegacy SubscriberIDOrigin = iota + 1
	// OriginRandom is a random id, minted at stage 3 or later.
	OriginRandom
)

// SubscriberMapping is one row of the subscriber directory, as a backup holds
// it: a keyed pseudonym, an opaque id and the id's origin. Nothing else.
type SubscriberMapping struct {
	Pseudonym    string
	SubscriberID string
	Origin       SubscriberIDOrigin
}

// SubscriberMigrationStage is a D30.5 rollout stage.
type SubscriberMigrationStage int

const (
	// StageDerived reads derived ids and writes no mapping (stage 0, today).
	StageDerived SubscriberMigrationStage = iota
	// StageReadMapping reads a mapping if present, else derives; writes none.
	StageReadMapping
	// StageAdoptLegacy reads as stage 1 and writes legacy-preserving mappings.
	StageAdoptLegacy
	// StageMappingOnly reads only the mapping and mints random ids for new
	// numbers.
	StageMappingOnly
	// StageLegacyRetired is stage 3 with the legacy key destroyed. It is
	// irreversible.
	StageLegacyRetired
)

var (
	// ErrSubscriberMappingInvariant is a directory state that breaks an
	// invariant: a malformed row, or a pseudonym or id held twice.
	ErrSubscriberMappingInvariant = errors.New("identity: subscriber mapping invariant violated")
	// ErrSubscriberStageTransition is a stage change whose preconditions do not
	// hold.
	ErrSubscriberStageTransition = errors.New("identity: subscriber-id stage transition refused")
	// ErrSubscriberNotMapped is a pseudonym with no mapping, where one is
	// required.
	ErrSubscriberNotMapped = errors.New("identity: subscriber has no mapping")
)

// CheckSubscriberMappings checks every directory invariant over rows: each
// pseudonym is 64 lowercase hex, each id has the subscriber-id shape, each
// origin is known, and no pseudonym or id appears twice. A backup is restored
// only if it passes.
func CheckSubscriberMappings(rows []SubscriberMapping) error {
	pseudonyms := make(map[string]struct{}, len(rows))
	ids := make(map[string]struct{}, len(rows))
	for i, r := range rows {
		switch {
		case !isLowerHex64(r.Pseudonym):
			return fmt.Errorf("%w: row %d: malformed pseudonym", ErrSubscriberMappingInvariant, i)
		case !validSubscriberID(r.SubscriberID):
			return fmt.Errorf("%w: row %d: malformed subscriber id", ErrSubscriberMappingInvariant, i)
		case r.Origin != OriginLegacy && r.Origin != OriginRandom:
			return fmt.Errorf("%w: row %d: unknown origin %d", ErrSubscriberMappingInvariant, i, r.Origin)
		}
		if _, dup := pseudonyms[r.Pseudonym]; dup {
			return fmt.Errorf("%w: row %d: pseudonym mapped twice", ErrSubscriberMappingInvariant, i)
		}
		if _, dup := ids[r.SubscriberID]; dup {
			return fmt.Errorf("%w: row %d: subscriber id held by two pseudonyms", ErrSubscriberMappingInvariant, i)
		}
		pseudonyms[r.Pseudonym] = struct{}{}
		ids[r.SubscriberID] = struct{}{}
	}
	return nil
}

// StageTransition is a proposed stage change and the facts its preconditions
// depend on. The facts are inputs: gathering them (whether the legacy key
// still exists, whether the owner has decided) is an operational step, not
// this function's.
type StageTransition struct {
	From, To SubscriberMigrationStage
	// LegacyKeyAvailable: the legacy key is still held, so legacy ids can be
	// computed.
	LegacyKeyAvailable bool
	// OwnerAcceptedDormantReassignment: the owner has decided that subscribers
	// who never re-verify receive new ids (D30.5's retirement condition).
	OwnerAcceptedDormantReassignment bool
	// UnmappedSessionSubscribers is how many distinct subscriber ids on live
	// sessions have no mapping row (DormantSubscribers). Zero is the
	// measurable form of "a full session lifetime has passed".
	UnmappedSessionSubscribers int
}

// CheckStageTransition reports whether t may proceed over rows.
//
// Rules:
//   - Forward moves are one stage at a time.
//   - Stages 1 to 3 need the legacy key: adoption computes legacy ids, and
//     stage 3 must still be able to roll back to stage 2.
//   - Retirement (3 to 4) needs the owner's decision and no live session
//     whose subscriber is unmapped.
//   - Nothing rolls back from stage 4.
//   - Moving to stage 0 needs every row to be a legacy id: stage 0 ignores the
//     mapping, so a random id would silently change.
func CheckStageTransition(t StageTransition, rows []SubscriberMapping) error {
	refuse := func(why string) error {
		return fmt.Errorf("%w: %d -> %d: %s", ErrSubscriberStageTransition, t.From, t.To, why)
	}
	if !t.From.valid() || !t.To.valid() {
		return refuse("unknown stage")
	}
	if err := CheckSubscriberMappings(rows); err != nil {
		return err
	}
	if t.To == t.From {
		return nil
	}
	if t.From == StageLegacyRetired {
		return refuse("retiring the legacy key is irreversible")
	}
	if t.To > t.From+1 {
		return refuse("forward moves are one stage at a time")
	}
	if !t.LegacyKeyAvailable {
		return refuse("the legacy key is required before retirement")
	}
	if why := t.targetRefusal(rows); why != "" {
		return refuse(why)
	}
	return nil
}

func (s SubscriberMigrationStage) valid() bool {
	return s >= StageDerived && s <= StageLegacyRetired
}

// targetRefusal is why t's destination stage cannot be entered over rows, or
// "" if it can.
func (t StageTransition) targetRefusal(rows []SubscriberMapping) string {
	switch t.To {
	case StageDerived:
		for _, r := range rows {
			if r.Origin != OriginLegacy {
				return "a random id exists, and stage 0 would replace it with a derived one"
			}
		}
	case StageLegacyRetired:
		if !t.OwnerAcceptedDormantReassignment {
			return "retirement needs the owner's decision on dormant subscribers"
		}
		if t.UnmappedSessionSubscribers != 0 {
			return "live sessions still reference unmapped subscribers"
		}
	}
	return ""
}

// DormantSubscribers returns the distinct subscriber ids in sessionSubscriberIDs
// that no row maps, sorted. These are the subscribers who would get a new id if
// the legacy key were retired now, and the rows the NOT VALID foreign key from
// sessions would reject. Validating that key needs this to be empty.
func DormantSubscribers(sessionSubscriberIDs []string, rows []SubscriberMapping) []string {
	mapped := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		mapped[r.SubscriberID] = struct{}{}
	}
	seen := map[string]struct{}{}
	var out []string
	for _, id := range sessionSubscriberIDs {
		if _, ok := mapped[id]; ok {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ReadSubscriberID is the id a stage reads for a verified number, given the
// directory's mapping (if any) and the number's legacy id. Stages 3 and 4 never
// fall back to derivation: an unmapped number is ErrSubscriberNotMapped, and
// the caller resolves it (creating the mapping) after verification.
func ReadSubscriberID(stage SubscriberMigrationStage, mapped string, ok bool, legacyID string) (string, error) {
	switch stage {
	case StageDerived:
		return legacyID, nil
	case StageReadMapping, StageAdoptLegacy:
		if ok {
			return mapped, nil
		}
		return legacyID, nil
	case StageMappingOnly, StageLegacyRetired:
		if ok {
			return mapped, nil
		}
		return "", ErrSubscriberNotMapped
	default:
		return "", fmt.Errorf("%w: unknown stage %d", ErrSubscriberStageTransition, stage)
	}
}

// Lookup returns pseudonym's mapped id without creating anything.
func (d *MemorySubscriberDirectory) Lookup(_ context.Context, pseudonym string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	m, ok := d.byKey[pseudonym]
	return m.SubscriberID, ok
}

// Snapshot returns every row, sorted by pseudonym: what a backup holds.
func (d *MemorySubscriberDirectory) Snapshot() []SubscriberMapping {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows := make([]SubscriberMapping, 0, len(d.byKey))
	for _, m := range d.byKey {
		rows = append(rows, m)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Pseudonym < rows[j].Pseudonym })
	return rows
}

// RestoreMemorySubscriberDirectory returns a directory in mode holding rows,
// which must pass CheckSubscriberMappings: a corrupt backup is refused whole,
// never partly restored.
func RestoreMemorySubscriberDirectory(mode SubscriberIDMode, mint func() (string, error), rows []SubscriberMapping) (*MemorySubscriberDirectory, error) {
	if err := CheckSubscriberMappings(rows); err != nil {
		return nil, err
	}
	d, err := NewMemorySubscriberDirectory(mode, mint)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		d.byKey[r.Pseudonym] = r
		d.ids[r.SubscriberID] = struct{}{}
	}
	return d, nil
}

// Rekey moves a mapping from its pseudonym under the previous subscriber key to
// its pseudonym under the active one (D30.2's lazy re-keying, on a
// verification). The id and its origin never change. A retry after success is
// a no-op. A move onto a pseudonym that already maps elsewhere is refused, and
// nothing changes.
func (d *MemorySubscriberDirectory) Rekey(_ context.Context, oldPseudonym, newPseudonym string) error {
	if !isLowerHex64(oldPseudonym) || !isLowerHex64(newPseudonym) {
		return ErrInvalidToken
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	m, ok := d.byKey[oldPseudonym]
	target, taken := d.byKey[newPseudonym]
	switch {
	case !ok && taken:
		return nil // already re-keyed
	case !ok:
		return ErrSubscriberNotMapped
	case oldPseudonym == newPseudonym:
		return nil
	case taken && target.SubscriberID != m.SubscriberID:
		return fmt.Errorf("%w: the new pseudonym already maps to another id", ErrSubscriberMappingInvariant)
	}
	delete(d.byKey, oldPseudonym)
	m.Pseudonym = newPseudonym
	d.byKey[newPseudonym] = m
	return nil
}

// ResolveWithKeyring resolves a verified number given its pseudonym under every
// subscriber key still in the lookup ring, the active key first. This is the
// migration plan's §5 rule, which D30.3 does not state:
//   - a mapping found under the active pseudonym is returned;
//   - a mapping found under an older key is moved to the active pseudonym
//     (Rekey), and its id is returned unchanged;
//   - only when no key finds a mapping is one created, under the active
//     pseudonym.
//
// This is what keeps a dormant subscriber's id, and keeps them able to enrol,
// across key rotations. It is also why a subscriber key may leave the ring
// only once no row still uses it (TestMigration_DroppingAnOldSubscriberKeyStrandsRows).
func (d *MemorySubscriberDirectory) ResolveWithKeyring(ctx context.Context, ring []string, legacyID string) (string, bool, error) {
	if len(ring) == 0 {
		return "", false, ErrInvalidToken
	}
	seen := make(map[string]struct{}, len(ring))
	for _, p := range ring {
		if !isLowerHex64(p) {
			return "", false, ErrInvalidToken
		}
		if _, dup := seen[p]; dup {
			return "", false, ErrInvalidToken
		}
		seen[p] = struct{}{}
	}
	active := ring[0]
	if id, ok := d.Lookup(ctx, active); ok {
		return id, false, nil
	}
	for _, older := range ring[1:] {
		id, ok := d.Lookup(ctx, older)
		if !ok {
			continue
		}
		if err := d.Rekey(ctx, older, active); err != nil {
			return "", false, err
		}
		return id, false, nil
	}
	return d.Resolve(ctx, active, legacyID)
}
