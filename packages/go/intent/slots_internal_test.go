package intent

import (
	"strings"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
)

// WHITE-BOX TESTS FOR BOUNDS THE PUBLIC API CANNOT REACH.
//
// MaxSlotsPerIntent and MaxSlotNameLen guard extractSlots, but with the
// built-in spec table neither can fire: no intent declares more than two slots
// and every name in SlotVocabulary is far shorter than 32 characters.
//
// Mutation testing found this directly — removing either bound changed no
// observable behaviour, so both were unverifiable defence-in-depth. These
// tests drive extractSlots with crafted specs so the guards become
// load-bearing, and the mutation report records them as CAUGHT only because of
// this file.
//
// In package intent, not intent_test, because slotSpec is unexported. That is
// the narrowest way to reach the bound; exporting a spec type purely to test
// it would widen the API for no caller's benefit.

// TestExtractSlots_CountIsBounded — more specs than the cap must truncate.
func TestExtractSlots_CountIsBounded(t *testing.T) {
	t.Parallel()

	// Six specs, all structurally satisfied, against a cap of four. Names are
	// drawn from the real vocabulary so the vocabulary guard does not reject
	// them first and mask the count guard.
	always := func([]string) bool { return true }
	specs := []slotSpec{
		{Name: SlotCallbackNumber, Structural: always},
		{Name: SlotCallerName, Structural: always},
		{Name: SlotCompanyName, Structural: always},
		{Name: SlotMessageBody, Structural: always},
		{Name: SlotPartyName, Structural: always},
		{Name: SlotTimeReference, Structural: always},
	}

	got := extractSlots([]string{"anything"}, specs)

	if len(specs) <= MaxSlotsPerIntent {
		t.Fatalf("fixture is not exercising the bound: %d specs vs cap %d",
			len(specs), MaxSlotsPerIntent)
	}
	if len(got) != MaxSlotsPerIntent {
		t.Errorf("extractSlots returned %d slots from %d specs, want the cap of %d",
			len(got), len(specs), MaxSlotsPerIntent)
	}
	// Truncation must keep the deterministic name order, not an arbitrary
	// prefix of the input order.
	for i := 1; i < len(got); i++ {
		if got[i-1].Name >= got[i].Name {
			t.Errorf("truncated result is not name-ascending: %v then %v",
				got[i-1].Name, got[i].Name)
		}
	}
}

// TestExtractSlots_OverLongNameIsDropped — a name past MaxSlotNameLen must not
// be emitted, even if it somehow reached a spec.
func TestExtractSlots_OverLongNameIsDropped(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", MaxSlotNameLen+1)
	specs := []slotSpec{
		{Name: long, Structural: func([]string) bool { return true }},
		{Name: SlotPartyName, Structural: func([]string) bool { return true }},
	}

	got := extractSlots([]string{"anything"}, specs)

	for _, s := range got {
		if len(s.Name) > MaxSlotNameLen {
			t.Errorf("emitted slot name of length %d, exceeding MaxSlotNameLen %d",
				len(s.Name), MaxSlotNameLen)
		}
		if s.Name == long {
			t.Error("the over-long name was emitted verbatim")
		}
	}
	// The valid neighbour must survive: one bad spec should not suppress the
	// rest, or a single defect would silently empty an intent's parameters.
	if len(got) != 1 || got[0].Name != SlotPartyName {
		t.Errorf("got %v, want exactly [%s]", names(got), SlotPartyName)
	}
}

// TestExtractSlots_OutOfVocabularyNameIsDropped — same guard, other half.
func TestExtractSlots_OutOfVocabularyNameIsDropped(t *testing.T) {
	t.Parallel()

	specs := []slotSpec{
		{Name: "exfiltrated_secret", Structural: func([]string) bool { return true }},
		{Name: SlotTimeReference, Structural: func([]string) bool { return true }},
	}

	got := extractSlots([]string{"anything"}, specs)

	for _, s := range got {
		if !inSlotVocabulary(s.Name) {
			t.Errorf("emitted out-of-vocabulary slot %q", s.Name)
		}
	}
	if len(got) != 1 || got[0].Name != SlotTimeReference {
		t.Errorf("got %v, want exactly [%s]", names(got), SlotTimeReference)
	}
}

func names(slots []conversation.Slot) []string {
	out := make([]string, 0, len(slots))
	for _, s := range slots {
		out = append(out, s.Name)
	}
	return out
}
