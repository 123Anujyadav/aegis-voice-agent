package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// ADR-0030 D30.5 design validation: offline, nothing here migrates anything.

// migrationNumbers are distinct canonical numbers for the directory tests.
var migrationNumbers = []string{"+919876543210", "+919876543211", "+919876543212", "+14155550100"}

// resolveNumber verifies-then-resolves number in d under key, as the wired
// flow will: pseudonym and legacy id from the number, then Resolve.
func resolveNumber(t *testing.T, d *MemorySubscriberDirectory, key []byte, number string) (string, bool) {
	t.Helper()
	p, err := SubscriberPseudonym(key, number)
	if err != nil {
		t.Fatal(err)
	}
	id, created, err := d.Resolve(context.Background(), p, LegacySubscriberID(key, number))
	if err != nil {
		t.Fatalf("Resolve(%s): %v", number, err)
	}
	return id, created
}

func row(p, id string, o SubscriberIDOrigin) SubscriberMapping {
	return SubscriberMapping{Pseudonym: p, SubscriberID: id, Origin: o}
}

func TestMigration_InvariantsRefuseCorruptMappings(t *testing.T) {
	p1, p2 := strings.Repeat("a", 64), strings.Repeat("b", 64)
	id1, id2 := "usr_"+strings.Repeat("1", 32), "usr_"+strings.Repeat("2", 32)
	if err := CheckSubscriberMappings([]SubscriberMapping{row(p1, id1, OriginLegacy), row(p2, id2, OriginRandom)}); err != nil {
		t.Fatalf("a valid directory is refused: %v", err)
	}
	for name, rows := range map[string][]SubscriberMapping{
		"pseudonym mapped twice":       {row(p1, id1, OriginLegacy), row(p1, id2, OriginLegacy)},
		"id held by two pseudonyms":    {row(p1, id1, OriginLegacy), row(p2, id1, OriginLegacy)},
		"uppercase pseudonym":          {row(strings.Repeat("A", 64), id1, OriginLegacy)},
		"short pseudonym":              {row(p1[:63], id1, OriginLegacy)},
		"malformed id":                 {row(p1, "usr_"+strings.Repeat("G", 32), OriginLegacy)},
		"id without prefix":            {row(p1, strings.Repeat("1", 36), OriginLegacy)},
		"unknown origin":               {row(p1, id1, 0)},
		"a number stored as an id":     {row(p1, "+919876543210", OriginLegacy)},
		"a number stored as pseudonym": {row("+919876543210", id1, OriginLegacy)},
	} {
		if err := CheckSubscriberMappings(rows); !errors.Is(err, ErrSubscriberMappingInvariant) {
			t.Errorf("%s: err = %v, want ErrSubscriberMappingInvariant", name, err)
		}
		if _, err := RestoreMemorySubscriberDirectory(AdoptLegacyIDs, nil, rows); !errors.Is(err, ErrSubscriberMappingInvariant) {
			t.Errorf("%s: a corrupt backup was restored (err = %v)", name, err)
		}
	}
}

func TestMigration_StageTransitions(t *testing.T) {
	p1, p2 := strings.Repeat("a", 64), strings.Repeat("b", 64)
	legacyOnly := []SubscriberMapping{row(p1, "usr_"+strings.Repeat("1", 32), OriginLegacy)}
	withRandom := append([]SubscriberMapping{row(p2, "usr_"+strings.Repeat("2", 32), OriginRandom)}, legacyOnly...)
	key := StageTransition{LegacyKeyAvailable: true}
	with := func(from, to SubscriberMigrationStage, f func(*StageTransition)) StageTransition {
		tr := key
		tr.From, tr.To = from, to
		if f != nil {
			f(&tr)
		}
		return tr
	}
	ownerOK := func(tr *StageTransition) { tr.OwnerAcceptedDormantReassignment = true }
	noKey := func(tr *StageTransition) { tr.LegacyKeyAvailable = false }

	cases := []struct {
		name string
		tr   StageTransition
		rows []SubscriberMapping
		ok   bool
	}{
		{"0 -> 1", with(0, 1, nil), nil, true},
		{"1 -> 2", with(1, 2, nil), nil, true},
		{"2 -> 3", with(2, 3, nil), legacyOnly, true},
		{"3 -> 4 with the owner's decision and no unmapped session", with(3, 4, ownerOK), withRandom, true},
		{"0 -> 2 skips a stage", with(0, 2, nil), nil, false},
		{"1 -> 2 without the legacy key", with(1, 2, noKey), nil, false},
		{"3 -> 4 without the owner's decision", with(3, 4, nil), withRandom, false},
		{"3 -> 4 with live unmapped sessions", with(3, 4, func(tr *StageTransition) {
			ownerOK(tr)
			tr.UnmappedSessionSubscribers = 1
		}), withRandom, false},
		{"2 -> 1 preserves ids", with(2, 1, nil), legacyOnly, true},
		{"2 -> 0 while every row is legacy", with(2, 0, nil), legacyOnly, true},
		{"2 -> 0 once a random id exists", with(2, 0, nil), withRandom, false},
		{"3 -> 2 with the legacy key", with(3, 2, nil), withRandom, true},
		{"3 -> 2 without the legacy key", with(3, 2, noKey), withRandom, false},
		{"3 -> 0 with random ids", with(3, 0, nil), withRandom, false},
		{"4 -> 3: retirement is irreversible", with(4, 3, ownerOK), withRandom, false},
		{"4 -> 0", with(4, 0, ownerOK), legacyOnly, false},
		{"unknown stage", with(0, 5, nil), nil, false},
		{"a corrupt directory blocks every move", with(1, 2, nil), []SubscriberMapping{row(p1, "bad", OriginLegacy)}, false},
	}
	for _, c := range cases {
		err := CheckStageTransition(c.tr, c.rows)
		if c.ok && err != nil {
			t.Errorf("%s: refused: %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: allowed, want refused", c.name)
		}
	}
}

// Each stage reads what D30.5's table says, and the reads explain the rollback
// rules: stage 2 keeps a random id (so 3 -> 2 is safe), stage 0 does not (so a
// random id forbids a move to stage 0).
func TestMigration_ReadsPerStage(t *testing.T) {
	legacy, random := goldenLegacyID, "usr_"+strings.Repeat("7", 32)
	for _, c := range []struct {
		stage  SubscriberMigrationStage
		mapped string
		ok     bool
		want   string
		err    error
	}{
		{StageDerived, random, true, legacy, nil},
		{StageReadMapping, "", false, legacy, nil},
		{StageReadMapping, random, true, random, nil},
		{StageAdoptLegacy, random, true, random, nil},
		{StageAdoptLegacy, "", false, legacy, nil},
		{StageMappingOnly, random, true, random, nil},
		{StageMappingOnly, "", false, "", ErrSubscriberNotMapped},
		{StageLegacyRetired, "", false, "", ErrSubscriberNotMapped},
	} {
		got, err := ReadSubscriberID(c.stage, c.mapped, c.ok, legacy)
		if got != c.want || !errors.Is(err, c.err) {
			t.Errorf("stage %d, mapped (%q, %v): (%q, %v), want (%q, %v)", c.stage, c.mapped, c.ok, got, err, c.want, c.err)
		}
	}
	if _, err := ReadSubscriberID(9, "", false, legacy); !errors.Is(err, ErrSubscriberStageTransition) {
		t.Errorf("unknown stage: err = %v", err)
	}
}

// Stage 1 creates nothing: reading through it leaves the directory empty, and
// every subscriber keeps today's id.
func TestMigration_StageOneChangesNothing(t *testing.T) {
	d, _ := NewMemorySubscriberDirectory(AdoptLegacyIDs, nil)
	for _, n := range migrationNumbers {
		p, _ := SubscriberPseudonym(dirKey, n)
		mapped, ok := d.Lookup(context.Background(), p)
		got, err := ReadSubscriberID(StageReadMapping, mapped, ok, LegacySubscriberID(dirKey, n))
		if err != nil || got != LegacySubscriberID(dirKey, n) {
			t.Fatalf("%s: (%s, %v), want today's id", n, got, err)
		}
	}
	if rows := d.Snapshot(); len(rows) != 0 {
		t.Fatalf("stage 1 created %d rows", len(rows))
	}
}

// A backup restored whole gives every subscriber the same id, creates nothing,
// and holds no number.
func TestMigration_BackupRestoreIsLossless(t *testing.T) {
	d, _ := NewMemorySubscriberDirectory(AdoptLegacyIDs, nil)
	want := map[string]string{}
	for _, n := range migrationNumbers[:2] {
		want[n], _ = resolveNumber(t, d, dirKey, n)
	}
	random, _ := RestoreMemorySubscriberDirectory(MintRandomIDs, nil, d.Snapshot())
	for _, n := range migrationNumbers[2:] {
		want[n], _ = resolveNumber(t, random, dirKey, n)
	}
	backup := random.Snapshot()

	for _, r := range backup {
		for _, n := range migrationNumbers {
			digits := strings.TrimPrefix(n, "+")
			if strings.Contains(r.Pseudonym, digits) || strings.Contains(r.SubscriberID, digits) {
				t.Fatalf("a backup row carries the number %s: %+v", n, r)
			}
		}
	}
	restored, err := RestoreMemorySubscriberDirectory(MintRandomIDs, nil, backup)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Snapshot(), backup) {
		t.Fatal("the restored directory differs from the backup")
	}
	for n, id := range want {
		if got, created := resolveNumber(t, restored, dirKey, n); got != id || created {
			t.Fatalf("%s after restore: (%s, created=%v), want (%s, false)", n, got, created, id)
		}
	}
}

// Recovery from an OLDER backup differs by stage. In stage 2 it loses nothing:
// a lost row is re-adopted with the same legacy id. From stage 3 a lost random
// id cannot be recovered, and the subscriber gets a new one. That is D30.3's
// consequence, and why backups are mandatory from stage 3.
func TestMigration_RestoringAnOlderBackup(t *testing.T) {
	late := migrationNumbers[1]

	adopt, _ := NewMemorySubscriberDirectory(AdoptLegacyIDs, nil)
	resolveNumber(t, adopt, dirKey, migrationNumbers[0])
	old := adopt.Snapshot()
	before, _ := resolveNumber(t, adopt, dirKey, late)
	recovered, _ := RestoreMemorySubscriberDirectory(AdoptLegacyIDs, nil, old)
	if after, created := resolveNumber(t, recovered, dirKey, late); after != before || !created {
		t.Fatalf("stage 2: a row lost with the backup came back as (%s, created=%v), want (%s, true)", after, created, before)
	}

	mint, _ := NewMemorySubscriberDirectory(MintRandomIDs, nil)
	resolveNumber(t, mint, dirKey, migrationNumbers[0])
	old = mint.Snapshot()
	before, _ = resolveNumber(t, mint, dirKey, late)
	recovered, _ = RestoreMemorySubscriberDirectory(MintRandomIDs, nil, old)
	if after, _ := resolveNumber(t, recovered, dirKey, late); after == before {
		t.Fatal("stage 3: a random id lost with the backup was recovered, which is impossible; the test is wrong")
	}
}

// Rotating the subscriber-pseudonym key never changes an id: the mapping moves
// to the new pseudonym, a retry is a no-op, and a move onto a pseudonym that
// maps elsewhere is refused with nothing changed.
func TestMigration_PseudonymKeyRotationNeverChangesAnID(t *testing.T) {
	ctx := context.Background()
	oldKey, newKey := dirKey, []byte("the_next_subscriber_key_long_enough!")
	d, _ := NewMemorySubscriberDirectory(AdoptLegacyIDs, nil)
	id, _ := resolveNumber(t, d, oldKey, dirNumber)
	oldP, _ := SubscriberPseudonym(oldKey, dirNumber)
	newP, _ := SubscriberPseudonym(newKey, dirNumber)
	if oldP == newP {
		t.Fatal("the keys give the same pseudonym; the test proves nothing")
	}

	if err := d.Rekey(ctx, oldP, newP); err != nil {
		t.Fatal(err)
	}
	if err := d.Rekey(ctx, oldP, newP); err != nil {
		t.Fatalf("a retried re-key: %v", err)
	}
	got, created, err := d.Resolve(ctx, newP, LegacySubscriberID(oldKey, dirNumber))
	if err != nil || got != id || created {
		t.Fatalf("after rotation: (%s, created=%v, %v), want (%s, false)", got, created, err, id)
	}
	if _, ok := d.Lookup(ctx, oldP); ok {
		t.Fatal("the old pseudonym still maps after re-keying")
	}
	if rows := d.Snapshot(); len(rows) != 1 || rows[0].Origin != OriginLegacy {
		t.Fatalf("re-keying changed the rows: %+v", rows)
	}

	other := migrationNumbers[3]
	otherID, _ := resolveNumber(t, d, oldKey, other)
	otherP, _ := SubscriberPseudonym(oldKey, other)
	before := d.Snapshot()
	if err := d.Rekey(ctx, otherP, newP); !errors.Is(err, ErrSubscriberMappingInvariant) {
		t.Fatalf("re-keying onto another subscriber's pseudonym: err = %v", err)
	}
	if !reflect.DeepEqual(d.Snapshot(), before) {
		t.Fatal("a refused re-key changed the directory")
	}
	if got, _ := d.Lookup(ctx, otherP); got != otherID {
		t.Fatal("a refused re-key moved the mapping")
	}
	if err := d.Rekey(ctx, strings.Repeat("c", 64), strings.Repeat("d", 64)); !errors.Is(err, ErrSubscriberNotMapped) {
		t.Fatalf("re-keying an unmapped pseudonym: err = %v", err)
	}
	if err := d.Rekey(ctx, "bad", newP); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("a malformed pseudonym: err = %v", err)
	}
}

// A dormant subscriber has a live session but never re-verifies, so no row maps
// them. DormantSubscribers finds them, retirement is refused while any exist,
// and the consequence the owner must accept is shown: after retirement a
// dormant subscriber who returns gets a new id, while an active one keeps theirs.
func TestMigration_DormantSubscribersAtRetirement(t *testing.T) {
	active, dormant := migrationNumbers[0], migrationNumbers[1]
	d, _ := NewMemorySubscriberDirectory(AdoptLegacyIDs, nil)
	activeID, _ := resolveNumber(t, d, dirKey, active)
	dormantID := LegacySubscriberID(dirKey, dormant)

	sessions := []string{activeID, dormantID, dormantID}
	got := DormantSubscribers(sessions, d.Snapshot())
	if !reflect.DeepEqual(got, []string{dormantID}) {
		t.Fatalf("DormantSubscribers = %v, want [%s]", got, dormantID)
	}
	err := CheckStageTransition(StageTransition{
		From: StageMappingOnly, To: StageLegacyRetired, LegacyKeyAvailable: true,
		OwnerAcceptedDormantReassignment: true, UnmappedSessionSubscribers: len(got),
	}, d.Snapshot())
	if !errors.Is(err, ErrSubscriberStageTransition) {
		t.Fatalf("retirement with a dormant subscriber: err = %v", err)
	}

	retired, _ := RestoreMemorySubscriberDirectory(MintRandomIDs, nil, d.Snapshot())
	if id, _ := resolveNumber(t, retired, dirKey, active); id != activeID {
		t.Fatalf("an active subscriber's id changed at retirement: %s, want %s", id, activeID)
	}
	if id, _ := resolveNumber(t, retired, dirKey, dormant); id == dormantID {
		t.Fatal("a dormant subscriber kept the legacy id after retirement; the consequence is not what the plan states")
	}
	if len(DormantSubscribers(sessions, retired.Snapshot())) != 1 {
		t.Fatal("the dormant subscriber's old sessions should still reference an unmapped id: the case NOT VALID must tolerate")
	}
}

// The origin each mode records is the one the rollback rule depends on.
func TestMigration_ModesRecordTheirOrigin(t *testing.T) {
	for mode, want := range map[SubscriberIDMode]SubscriberIDOrigin{AdoptLegacyIDs: OriginLegacy, MintRandomIDs: OriginRandom} {
		d, _ := NewMemorySubscriberDirectory(mode, nil)
		resolveNumber(t, d, dirKey, dirNumber)
		if rows := d.Snapshot(); len(rows) != 1 || rows[0].Origin != want {
			t.Errorf("mode %d: rows %s, want origin %d", mode, fmt.Sprint(rows), want)
		}
	}
}

// A design hazard D30.3 does not state, pinned so it cannot be forgotten.
// Mapping rows are permanent, and a pseudonym cannot be recomputed without the
// number. Dropping an old subscriber-pseudonym key from the ring therefore
// strands the rows of every subscriber who has not re-verified since the
// switch. When such a subscriber returns, legacy adoption under the new key
// tries to store the id the stranded row already holds, and the UNIQUE rule
// refuses it: they are locked out, fail closed. So a subscriber key may leave
// the lookup ring only when no row still uses it (see the migration plan).
func TestMigration_DroppingAnOldSubscriberKeyStrandsRows(t *testing.T) {
	oldKey, newKey := dirKey, []byte("the_next_subscriber_key_long_enough!")
	d, _ := NewMemorySubscriberDirectory(AdoptLegacyIDs, nil)
	resolveNumber(t, d, oldKey, dirNumber) // mapped under the old key, then dormant

	// The old key is dropped, so the lookup only tries the new pseudonym.
	newP, _ := SubscriberPseudonym(newKey, dirNumber)
	_, _, err := d.Resolve(context.Background(), newP, LegacySubscriberID(oldKey, dirNumber))
	if !errors.Is(err, ErrSubscriberIDCollision) {
		t.Fatalf("a stranded subscriber returning: err = %v, want the fail-closed collision", err)
	}
}

// The plan's §5 mitigation, checked: while the lookup ring keeps every key a
// row still uses, a dormant subscriber who returns after one or more
// rotations keeps their id, and their row moves to the active key. The same
// return with the old key dropped is the fail-closed lockout above.
func TestMigration_KeyringLookupKeepsDormantSubscribersAcrossRotations(t *testing.T) {
	ctx := context.Background()
	k1, k2, k3 := dirKey, []byte("the_next_subscriber_key_long_enough!"), []byte("and_the_one_after_that_long_enough!!")
	ring := func(keys ...[]byte) []string {
		var out []string
		for _, k := range keys {
			p, err := SubscriberPseudonym(k, dirNumber)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, p)
		}
		return out
	}
	legacy := LegacySubscriberID(k1, dirNumber)

	d, _ := NewMemorySubscriberDirectory(AdoptLegacyIDs, nil)
	id, created, err := d.ResolveWithKeyring(ctx, ring(k1), legacy)
	if err != nil || !created || id != legacy {
		t.Fatalf("first resolution: (%s, %v, %v)", id, created, err)
	}
	// Two rotations pass while the subscriber is dormant; the ring keeps all
	// three keys, active first.
	got, created, err := d.ResolveWithKeyring(ctx, ring(k3, k2, k1), legacy)
	if err != nil || created || got != id {
		t.Fatalf("after two rotations: (%s, created=%v, %v), want (%s, false)", got, created, err, id)
	}
	rows := d.Snapshot()
	if len(rows) != 1 || rows[0].Pseudonym != ring(k3)[0] || rows[0].SubscriberID != id || rows[0].Origin != OriginLegacy {
		t.Fatalf("the row was not moved to the active key unchanged: %+v", rows)
	}
	if got, created, _ := d.ResolveWithKeyring(ctx, ring(k3, k2, k1), legacy); got != id || created {
		t.Fatal("a second resolution after re-keying changed something")
	}

	// The contrast: the same dormant return with the old keys dropped.
	stranded, _ := NewMemorySubscriberDirectory(AdoptLegacyIDs, nil)
	_, _, _ = stranded.ResolveWithKeyring(ctx, ring(k1), legacy)
	if _, _, err := stranded.ResolveWithKeyring(ctx, ring(k3), legacy); !errors.Is(err, ErrSubscriberIDCollision) {
		t.Fatalf("with the old keys dropped: err = %v, want the fail-closed lockout", err)
	}
}

// A number no key has seen is created under the active pseudonym; in random
// mode it gets a random id.
func TestMigration_KeyringLookupCreatesUnderTheActiveKey(t *testing.T) {
	ctx := context.Background()
	oldP, _ := SubscriberPseudonym(dirKey, dirNumber)
	newP, _ := SubscriberPseudonym([]byte("the_next_subscriber_key_long_enough!"), dirNumber)
	d, _ := NewMemorySubscriberDirectory(MintRandomIDs, nil)
	id, created, err := d.ResolveWithKeyring(ctx, []string{newP, oldP}, LegacySubscriberID(dirKey, dirNumber))
	if err != nil || !created || id == LegacySubscriberID(dirKey, dirNumber) {
		t.Fatalf("(%s, %v, %v), want a new random id", id, created, err)
	}
	if rows := d.Snapshot(); len(rows) != 1 || rows[0].Pseudonym != newP || rows[0].Origin != OriginRandom {
		t.Fatalf("rows %+v, want one random row under the active pseudonym", rows)
	}
}

// Concurrent returns of one dormant subscriber through the ring agree on one
// id and leave exactly one row, under the active key.
func TestMigration_KeyringLookupConcurrentReturnsAgree(t *testing.T) {
	ctx := context.Background()
	oldP, _ := SubscriberPseudonym(dirKey, dirNumber)
	newP, _ := SubscriberPseudonym([]byte("the_next_subscriber_key_long_enough!"), dirNumber)
	legacy := LegacySubscriberID(dirKey, dirNumber)
	d, _ := NewMemorySubscriberDirectory(AdoptLegacyIDs, nil)
	want, _, _ := d.Resolve(ctx, oldP, legacy)

	const n = 16
	var wg sync.WaitGroup
	ids := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids[i], _, errs[i] = d.ResolveWithKeyring(ctx, []string{newP, oldP}, legacy)
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil || ids[i] != want {
			t.Fatalf("resolution %d: (%s, %v), want %s", i, ids[i], errs[i], want)
		}
	}
	if rows := d.Snapshot(); len(rows) != 1 || rows[0].Pseudonym != newP {
		t.Fatalf("rows %+v, want one under the active pseudonym", rows)
	}
}

func TestMigration_KeyringLookupRefusesMalformedRings(t *testing.T) {
	ctx := context.Background()
	d, _ := NewMemorySubscriberDirectory(AdoptLegacyIDs, nil)
	p := strings.Repeat("a", 64)
	for name, ring := range map[string][]string{
		"empty":     nil,
		"malformed": {p, "not-hex"},
		"duplicate": {p, p},
	} {
		if _, _, err := d.ResolveWithKeyring(ctx, ring, goldenLegacyID); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if rows := d.Snapshot(); len(rows) != 0 {
		t.Fatalf("a refused ring created rows: %+v", rows)
	}
}
