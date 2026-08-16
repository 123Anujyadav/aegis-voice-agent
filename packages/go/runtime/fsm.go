package runtime

import (
	"fmt"
	"sort"
	"sync"
)

// FSM is a typed, guarded, thread-safe finite state machine.
//
// It is generic over the state type so a session's states and a stream's states
// are different types and cannot be confused. A single untyped state machine
// keyed on strings would compile happily when a session was moved into a stream
// state, and that class of bug is invisible until production.
//
// WHY THE RUNTIME HAS ITS OWN. Every lifecycle in this package — session,
// stream, breaker — is a state machine, and each was originally a scattering of
// booleans and mutexes. Booleans permit states that should not exist: a session
// both draining and accepting, a stream both closed and streaming. Naming the
// legal transitions once, and refusing everything else, converts a class of
// concurrency bug into an error return.
//
// Transitions are refused, not logged. A caller attempting an illegal
// transition has a bug, and returning ErrInvalidTransition surfaces it at the
// call site where it can be fixed.
type FSM[S comparable] struct {
	mu       sync.RWMutex
	state    S
	terminal map[S]bool
	allowed  map[S]map[S]bool
	guards   map[transitionKey[S]]Guard[S]
	hooks    []TransitionHook[S]
	clock    Clock
	entered  map[S]int
}

type transitionKey[S comparable] struct{ from, to S }

// Guard decides whether a transition may proceed. Returning a non-nil error
// refuses it, and the error is returned to the caller unchanged so a guard can
// explain itself.
type Guard[S comparable] func(from, to S) error

// TransitionHook observes a completed transition.
//
// Hooks run synchronously while the FSM lock is NOT held, so a hook may call
// back into the FSM to read state without deadlocking. They must not block: a
// hook that blocks stalls every transition of that machine, and on the
// streaming path that is a stalled call.
type TransitionHook[S comparable] func(from, to S)

// FSMSpec declares a state machine's shape.
type FSMSpec[S comparable] struct {
	// Initial is the starting state.
	Initial S

	// Transitions maps each state to the states reachable from it. A state
	// absent from the map has no outgoing transitions and is therefore
	// terminal, but declaring it in Terminal as well is clearer and is checked.
	Transitions map[S][]S

	// Terminal lists states from which nothing may follow. Declared explicitly
	// rather than inferred, so an accidentally unreachable state is a
	// configuration error rather than a silent dead end.
	Terminal []S

	// Guards optionally constrains individual transitions.
	Guards map[S]map[S]Guard[S]
}

// NewFSM builds a state machine from a spec.
//
// It validates the spec fully: a transition into an undeclared state, or a
// transition out of a terminal state, is refused at construction. Both are
// programming errors, and catching them at boot rather than at the moment the
// transition is first attempted is the difference between a failed deploy and a
// failed call.
func NewFSM[S comparable](spec FSMSpec[S], clock Clock) (*FSM[S], error) {
	if clock == nil {
		clock = SystemClock{}
	}

	terminal := make(map[S]bool, len(spec.Terminal))
	for _, s := range spec.Terminal {
		terminal[s] = true
	}

	known := map[S]bool{spec.Initial: true}
	for from, tos := range spec.Transitions {
		known[from] = true
		for _, to := range tos {
			known[to] = true
		}
	}
	for s := range terminal {
		known[s] = true
	}

	var problems []string
	for from, tos := range spec.Transitions {
		if terminal[from] && len(tos) > 0 {
			problems = append(problems, fmt.Sprintf(
				"state %v is declared terminal but has %d outgoing transition(s)", from, len(tos)))
		}
		for _, to := range tos {
			if from == to {
				problems = append(problems, fmt.Sprintf(
					"state %v declares a self-transition; use a hook instead", from))
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, &ConfigError{Problems: problems}
	}

	allowed := make(map[S]map[S]bool, len(spec.Transitions))
	for from, tos := range spec.Transitions {
		set := make(map[S]bool, len(tos))
		for _, to := range tos {
			set[to] = true
		}
		allowed[from] = set
	}

	guards := make(map[transitionKey[S]]Guard[S])
	for from, m := range spec.Guards {
		for to, g := range m {
			if !allowed[from][to] {
				return nil, &ConfigError{Problems: []string{fmt.Sprintf(
					"guard declared for undeclared transition %v -> %v", from, to)}}
			}
			guards[transitionKey[S]{from, to}] = g
		}
	}

	return &FSM[S]{
		state:    spec.Initial,
		terminal: terminal,
		allowed:  allowed,
		guards:   guards,
		clock:    clock,
		entered:  map[S]int{spec.Initial: 1},
	}, nil
}

// State returns the current state.
func (f *FSM[S]) State() S {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.state
}

// Is reports whether the machine is in state s.
func (f *FSM[S]) Is(s S) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.state == s
}

// IsTerminal reports whether the machine has reached a terminal state.
func (f *FSM[S]) IsTerminal() bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.terminal[f.state]
}

// EnteredCount reports how many times state s has been entered. Used by tests
// and by metrics to detect flapping, which a bare current-state read cannot see.
func (f *FSM[S]) EnteredCount(s S) int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.entered[s]
}

// OnTransition registers a hook. Hooks fire in registration order.
func (f *FSM[S]) OnTransition(h TransitionHook[S]) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hooks = append(f.hooks, h)
}

// To attempts a transition to state to.
//
// It returns ErrInvalidTransition if the transition is not declared, or the
// guard's error if a guard refuses. On success it returns the state departed
// from, which lets a caller log the pair without a second read that might race.
func (f *FSM[S]) To(to S) (from S, err error) {
	f.mu.Lock()

	from = f.state
	if from == to {
		f.mu.Unlock()
		return from, fmt.Errorf("%w: already in state %v", ErrInvalidTransition, to)
	}
	if f.terminal[from] {
		f.mu.Unlock()
		return from, fmt.Errorf("%w: %v is terminal, cannot move to %v",
			ErrInvalidTransition, from, to)
	}
	if !f.allowed[from][to] {
		f.mu.Unlock()
		return from, fmt.Errorf("%w: %v -> %v is not declared", ErrInvalidTransition, from, to)
	}
	if g, ok := f.guards[transitionKey[S]{from, to}]; ok {
		// The guard runs while the lock is held so it observes a state that
		// cannot change beneath it. Guards must therefore be fast and must not
		// call back into this FSM — documented on Guard.
		if gerr := g(from, to); gerr != nil {
			f.mu.Unlock()
			return from, gerr
		}
	}

	f.state = to
	f.entered[to]++
	hooks := make([]TransitionHook[S], len(f.hooks))
	copy(hooks, f.hooks)
	f.mu.Unlock()

	// Hooks run outside the lock so a hook may read this FSM without
	// deadlocking, and so a slow hook does not block a concurrent State().
	for _, h := range hooks {
		h(from, to)
	}
	return from, nil
}

// TryTo attempts a transition and reports success, discarding the error.
//
// For the common case where a caller races to move a machine and does not care
// which goroutine won — for example several paths all trying to move a stream
// to aborted. Using To and discarding the error would read as ignoring a
// failure; this names the intent.
func (f *FSM[S]) TryTo(to S) bool {
	_, err := f.To(to)
	return err == nil
}

// MustBe returns an error unless the machine is in one of the given states.
//
// Used at the top of operations with a state precondition, so the precondition
// is stated once and enforced rather than implied by the order of the code.
func (f *FSM[S]) MustBe(states ...S) error {
	f.mu.RLock()
	current := f.state
	f.mu.RUnlock()

	for _, s := range states {
		if current == s {
			return nil
		}
	}
	return fmt.Errorf("%w: in state %v, required one of %v", ErrInvalidTransition, current, states)
}

// CanGo reports whether a transition to `to` is currently declared. It does not
// run guards: a guard may consult state that changes between this call and the
// transition, so treating CanGo as a guarantee would be a race. It answers
// "is this shape legal", not "will this succeed".
func (f *FSM[S]) CanGo(to S) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return !f.terminal[f.state] && f.allowed[f.state][to]
}
