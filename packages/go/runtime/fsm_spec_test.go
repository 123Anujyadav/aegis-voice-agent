package runtime

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// NewFSM's contract: a transition into an undeclared state is refused at
// construction. A state is declared by an entry in Transitions or by being
// terminal; one that is only ever reached would be a silent dead end.
func TestNewFSM_RefusesAReachedStateThatIsNeitherDeclaredNorTerminal(t *testing.T) {
	t.Parallel()
	_, err := NewFSM(FSMSpec[tState]{
		Initial:     sA,
		Transitions: map[tState][]tState{sA: {sB, sEnd}},
		Terminal:    []tState{sEnd},
	}, NewFakeClock(time.Time{}))
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("NewFSM = %v; want a ConfigError for the undeclared state", err)
	}
	if len(ce.Problems) != 1 || !strings.Contains(ce.Problems[0], "neither declared") {
		t.Errorf("problems = %q; want exactly the undeclared-state problem", ce.Problems)
	}
}

func TestNewFSM_RefusesAnUndeclaredInitialState(t *testing.T) {
	t.Parallel()
	_, err := NewFSM(FSMSpec[tState]{
		Initial:     sA,
		Transitions: map[tState][]tState{sB: {sEnd}},
		Terminal:    []tState{sEnd},
	}, NewFakeClock(time.Time{}))
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("NewFSM = %v; want a ConfigError for the undeclared initial state", err)
	}
}

func TestNewFSM_ATerminalStateNeedsNoTransitions(t *testing.T) {
	t.Parallel()
	if _, err := NewFSM(FSMSpec[tState]{
		Initial:     sA,
		Transitions: map[tState][]tState{sA: {sEnd}},
		Terminal:    []tState{sEnd},
	}, NewFakeClock(time.Time{})); err != nil {
		t.Fatalf("NewFSM = %v; a terminal target is declared", err)
	}
}

// Every problem is reported together, sorted, so one boot shows them all.
func TestNewFSM_ReportsEveryProblemSorted(t *testing.T) {
	t.Parallel()
	_, err := NewFSM(FSMSpec[tState]{
		Initial: sA,
		Transitions: map[tState][]tState{
			sA:   {sA, sB},
			sEnd: {sA},
		},
		Terminal: []tState{sEnd},
	}, NewFakeClock(time.Time{}))
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("NewFSM = %v; want a ConfigError", err)
	}
	if len(ce.Problems) != 3 {
		t.Fatalf("problems = %q; want 3 (self-transition, exit from terminal, undeclared state)", ce.Problems)
	}
	for i := 1; i < len(ce.Problems); i++ {
		if ce.Problems[i-1] > ce.Problems[i] {
			t.Errorf("problems are not sorted: %q", ce.Problems)
		}
	}
}
