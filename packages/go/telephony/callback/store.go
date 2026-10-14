package callback

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
)

// ReserveStatus is what [Store.Reserve] found.
type ReserveStatus int

// The reservation statuses.
const (
	// StatusReserved: the key was new and is now held by the caller, who must
	// Settle or Release it.
	StatusReserved ReserveStatus = iota + 1
	// StatusDuplicate: the key was settled earlier; Prior holds its result.
	StatusDuplicate
	// StatusInFlight: another caller holds the key right now.
	StatusInFlight
	// StatusMismatch: the key exists with a different [Fingerprint].
	StatusMismatch
)

// Reservation is the result of [Store.Reserve].
type Reservation struct {
	Status ReserveStatus
	Prior  Result
	// Token identifies THIS reservation. Set only for StatusReserved; it must
	// be presented to Settle or Release, so a holder whose reservation has
	// been lost — a lease that expired and was taken over — cannot settle or
	// release its successor's.
	Token string
}

// ErrNotOwner is returned by Settle or Release when the token does not hold
// the key: it was never reserved, already settled, released, or — in a store
// with leases — the lease expired and the key may now belong to someone else.
var ErrNotOwner = errors.New("callback: reservation not held by this token")

// ErrStoreUnavailable is the provider-neutral class of "durable state could
// not answer". Durable adapters (telephonyredis, telephonystore) wrap it, so a
// caller such as the HTTP handler can recognise an outage without importing
// them. It never means "absent" or "new".
var ErrStoreUnavailable = errors.New("callback: durable state unavailable")

// Store is the deduplication and reconciliation state the [Ingestor] needs.
//
// Reserve must be atomic: two concurrent Reserve calls for one new key must
// not both return StatusReserved. A durable implementation must preserve that
// across every process sharing it.
//
// Every method takes a context and reports failure. A store that cannot answer
// MUST return an error: the ingestor treats any error as "do not proceed", and
// never as "new event" or "not terminal".
type Store interface {
	Reserve(ctx context.Context, k Key, fp Fingerprint) (Reservation, error)
	Settle(ctx context.Context, k Key, token string, r Result) error
	Release(ctx context.Context, k Key, token string) error

	// MarkTerminal and Terminal remember calls this store has seen end. The
	// registry forgets an ended call, so without this a late event for it
	// would be indistinguishable from an event for a call not yet registered.
	MarkTerminal(ctx context.Context, id telephony.CallID) error
	Terminal(ctx context.Context, id telephony.CallID) (bool, error)

	// RecordConflict keeps an event for reconciliation.
	RecordConflict(ctx context.Context, c Conflict) error
}

// NewToken returns a random reservation token. Stores may use it; the token's
// only requirement is that two reservations never share one.
func NewToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("callback: generating a reservation token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// MemoryStore is an in-process [Store].
//
// # What it guarantees
//
// Within this process: Reserve is atomic, so one key is applied at most once
// while it is remembered, and a released key can be reserved again.
//
// # What it does not
//
//   - NOTHING SURVIVES A RESTART. Every key, terminal mark and conflict is
//     lost; a redelivery after a restart is processed as new. The ingestor's
//     state-based judgement then prevents a second transition for every kind
//     it applies — but a call that ended before the restart is no longer
//     known to be terminal, and its late events report ErrUnknownCall.
//   - NOTHING IS SHARED BETWEEN REPLICAS. Two processes behind one webhook
//     endpoint each accept the same event. Not sufficient for a multi-replica
//     deployment; that needs a durable store with an atomic reserve.
//   - THE WINDOW IS BOUNDED. At most Capacity keys are remembered. When full,
//     the OLDEST SETTLED key is forgotten to make room, and a redelivery of it
//     is then judged afresh rather than reported as Duplicate. A key still in
//     flight is never forgotten; if every remembered key is in flight,
//     Reserve fails with [ErrStoreFull] rather than forget one. Terminal marks
//     and conflicts are bounded by the same Capacity, oldest dropped first.
type MemoryStore struct {
	mu       sync.Mutex
	capacity int

	entries map[Key]*entry
	settled []Key // settlement order, oldest first; may hold forgotten keys

	terminal      map[telephony.CallID]struct{}
	terminalOrder []telephony.CallID

	conflicts        []Conflict
	droppedConflicts uint64
	forgotten        uint64
}

type entry struct {
	fp      Fingerprint
	token   string
	settled bool
	result  Result
}

// NewMemoryStore builds a store remembering at most capacity keys.
func NewMemoryStore(capacity int) (*MemoryStore, error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("callback: MemoryStore capacity must be positive, got %d; "+
			"an unbounded store is unbounded memory", capacity)
	}
	return &MemoryStore{
		capacity: capacity,
		entries:  make(map[Key]*entry),
		terminal: make(map[telephony.CallID]struct{}),
	}, nil
}

// Reserve implements [Store].
func (s *MemoryStore) Reserve(ctx context.Context, k Key, fp Fingerprint) (Reservation, error) {
	if err := ctx.Err(); err != nil {
		return Reservation{}, err
	}
	token, err := NewToken()
	if err != nil {
		return Reservation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if e, ok := s.entries[k]; ok {
		switch {
		case e.fp != fp:
			return Reservation{Status: StatusMismatch}, nil
		case e.settled:
			return Reservation{Status: StatusDuplicate, Prior: e.result}, nil
		default:
			return Reservation{Status: StatusInFlight}, nil
		}
	}
	if len(s.entries) >= s.capacity && !s.forgetOldestSettledLocked() {
		return Reservation{}, ErrStoreFull
	}
	s.entries[k] = &entry{fp: fp, token: token}
	return Reservation{Status: StatusReserved, Token: token}, nil
}

func (s *MemoryStore) forgetOldestSettledLocked() bool {
	for len(s.settled) > 0 {
		k := s.settled[0]
		s.settled = s.settled[1:]
		if e, ok := s.entries[k]; ok && e.settled {
			delete(s.entries, k)
			s.forgotten++
			return true
		}
	}
	return false
}

// Settle implements [Store]. The in-memory store has no leases: a process
// that crashes loses every reservation with it.
func (s *MemoryStore) Settle(ctx context.Context, k Key, token string, r Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[k]
	if !ok || e.settled || token == "" || e.token != token {
		return ErrNotOwner
	}
	e.settled, e.result, e.token = true, r, ""
	s.settled = append(s.settled, k)
	return nil
}

// Release implements [Store]. It does not consult ctx: a release must not be
// skipped because the request that held the key ended.
func (s *MemoryStore) Release(_ context.Context, k Key, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[k]
	if !ok || e.settled || token == "" || e.token != token {
		return ErrNotOwner
	}
	delete(s.entries, k)
	return nil
}

// MarkTerminal implements [Store].
func (s *MemoryStore) MarkTerminal(_ context.Context, id telephony.CallID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.terminal[id]; ok {
		return nil
	}
	if len(s.terminalOrder) >= s.capacity {
		delete(s.terminal, s.terminalOrder[0])
		s.terminalOrder = s.terminalOrder[1:]
	}
	s.terminal[id] = struct{}{}
	s.terminalOrder = append(s.terminalOrder, id)
	return nil
}

// Terminal implements [Store].
func (s *MemoryStore) Terminal(ctx context.Context, id telephony.CallID) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.terminal[id]
	return ok, nil
}

// RecordConflict implements [Store].
func (s *MemoryStore) RecordConflict(_ context.Context, c Conflict) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.conflicts) >= s.capacity {
		s.conflicts = s.conflicts[1:]
		s.droppedConflicts++
	}
	s.conflicts = append(s.conflicts, c)
	return nil
}

// Conflicts returns the recorded conflicts, oldest first.
func (s *MemoryStore) Conflicts() []Conflict {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Conflict(nil), s.conflicts...)
}

// Stats reports how much the bounded window has dropped.
func (s *MemoryStore) Stats() (remembered int, forgotten, droppedConflicts uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries), s.forgotten, s.droppedConflicts
}
