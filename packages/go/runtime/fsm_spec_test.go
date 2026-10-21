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

// An explicit entry declares a state even with no exits; only a state with no
// entry and no terminal listing is refused.
func TestNewFSM_AnExplicitEmptyEntryIsDeclared(t *testing.T) {
	t.Parallel()
	if _, err := NewFSM(FSMSpec[tState]{
		Initial:     sA,
		Transitions: map[tState][]tState{sA: {sB, sEnd}, sB: {}},
		Terminal:    []tState{sEnd},
	}, NewFakeClock(time.Time{})); err != nil {
		t.Fatalf("NewFSM = %v; sB has an explicit entry", err)
	}
}

func TestNewFSM_RefusesAGuardOnATransitionTheSpecDoesNotAllow(t *testing.T) {
	t.Parallel()
	_, err := NewFSM(FSMSpec[tState]{
		Initial:     sA,
		Transitions: map[tState][]tState{sA: {sB}, sB: {sEnd}},
		Terminal:    []tState{sEnd},
		Guards: map[tState]map[tState]Guard[tState]{
			sA: {sEnd: func(tState, tState) error { return nil }},
		},
	}, NewFakeClock(time.Time{}))
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("NewFSM = %v; want a ConfigError for the guard on A -> End", err)
	}
	if len(ce.Problems) != 1 || !strings.Contains(ce.Problems[0], "undeclared transition") {
		t.Errorf("problems = %q; want exactly the guard problem", ce.Problems)
	}
}

// A guard's refusal leaves the machine where it was: no state change, no entry
// counted and no hook fired.
func TestFSM_AGuardRefusalLeavesTheStateUnchanged(t *testing.T) {
	t.Parallel()
	refused := errors.New("guard says no")
	f, err := NewFSM(FSMSpec[tState]{
		Initial:     sA,
		Transitions: map[tState][]tState{sA: {sB}},
		Terminal:    []tState{sB},
		Guards: map[tState]map[tState]Guard[tState]{
			sA: {sB: func(tState, tState) error { return refused }},
		},
	}, NewFakeClock(time.Time{}))
	if err != nil {
		t.Fatalf("NewFSM: %v", err)
	}
	hooked := 0
	f.OnTransition(func(tState, tState) { hooked++ })
	if _, err := f.To(sB); !errors.Is(err, refused) {
		t.Fatalf("To = %v; want the guard's error", err)
	}
	if f.State() != sA || f.EnteredCount(sB) != 0 || hooked != 0 {
		t.Errorf("after a refusal: state %v, entered(B) %d, hooks %d; want A, 0, 0",
			f.State(), f.EnteredCount(sB), hooked)
	}
}
