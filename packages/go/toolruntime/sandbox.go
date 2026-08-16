package toolruntime

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Budget bounds one execution's resource use.
//
// READ THE NEXT PARAGRAPH BEFORE RELYING ON THIS FOR SECURITY.
//
// This is a BUDGET, not a jail. An in-process Go tool runs on the same
// goroutine scheduler, in the same address space, with the same file
// descriptors and the same network access as the runtime that invoked it. No
// amount of accounting here changes that. What a budget can do is bound
// cooperative resource use and detect the uncooperative case; what it cannot do
// is stop a hostile in-process tool from allocating until the process dies,
// spinning a core, or opening a socket.
//
// Real isolation requires the tool to run somewhere the runtime can kill:
// another process, another container, another machine. [Sandbox] is the seam
// for that, and an out-of-process implementation is Phase 10E's business. The
// honest position for Phase 10D is stated in SECURITY_REVIEW §R1 rather than
// implied by naming a struct "Sandbox" and hoping.
type Budget struct {
	// WallClock bounds the TOTAL time for an execution including every retry
	// and every backoff. Distinct from Contract.Timeout, which bounds one
	// attempt: three attempts of a 5 s tool is 15 s of a caller's patience, and
	// a per-attempt timeout alone never notices.
	WallClock time.Duration

	// InputBytes bounds the estimated size of arguments.
	InputBytes int

	// OutputBytes bounds the estimated size of a result, and of the cumulative
	// stream. A tool that streams unboundedly is the same denial of service as
	// one that returns a huge result, arriving more slowly.
	OutputBytes int

	// MaxAttempts caps total invocations of one step, retries included. Zero
	// takes the retry policy's value; the smaller of the two wins, so a budget
	// can tighten a policy but never loosen it.
	MaxAttempts int

	// Slots is how much scheduler capacity one execution consumes. A tool that
	// holds a downstream connection for a second should not count the same as
	// one that reads a cache.
	Slots int
}

// DefaultBudget returns the runtime's default limits.
//
// The wall clock is 30 seconds because that is roughly the length of a whole
// screening call: a tool that has not finished by then has already lost the
// conversation it was serving, and continuing to wait only delays telling the
// caller so.
func DefaultBudget() Budget {
	return Budget{
		WallClock:   30 * time.Second,
		InputBytes:  256 * 1024,
		OutputBytes: 1024 * 1024,
		MaxAttempts: 3,
		Slots:       1,
	}
}

func (b Budget) validate(d Descriptor) []string {
	var problems []string
	if b.WallClock < 0 {
		problems = append(problems, fmt.Sprintf("%s budget: WallClock must not be negative", d))
	}
	if b.InputBytes < 0 || b.OutputBytes < 0 {
		problems = append(problems, fmt.Sprintf("%s budget: byte limits must not be negative", d))
	}
	if b.MaxAttempts < 0 {
		problems = append(problems, fmt.Sprintf("%s budget: MaxAttempts must not be negative", d))
	}
	if b.Slots < 0 {
		problems = append(problems, fmt.Sprintf("%s budget: Slots must not be negative", d))
	}
	return problems
}

// withDefaults fills zero fields from a fallback.
func (b Budget) withDefaults(def Budget) Budget {
	if b.WallClock == 0 {
		b.WallClock = def.WallClock
	}
	if b.InputBytes == 0 {
		b.InputBytes = def.InputBytes
	}
	if b.OutputBytes == 0 {
		b.OutputBytes = def.OutputBytes
	}
	if b.MaxAttempts == 0 {
		b.MaxAttempts = def.MaxAttempts
	}
	if b.Slots == 0 {
		b.Slots = def.Slots
	}
	return b
}

// tighten returns the stricter of two budgets, field by field.
//
// Always the stricter one, never a merge that could relax a limit. A tool's
// contract may lower the runtime's ceiling; nothing may raise it. That
// direction is the whole point of a ceiling, and getting it backwards is how a
// configuration file ends up able to disable a safety limit.
func (b Budget) tighten(other Budget) Budget {
	out := b
	if other.WallClock > 0 && (out.WallClock == 0 || other.WallClock < out.WallClock) {
		out.WallClock = other.WallClock
	}
	if other.InputBytes > 0 && (out.InputBytes == 0 || other.InputBytes < out.InputBytes) {
		out.InputBytes = other.InputBytes
	}
	if other.OutputBytes > 0 && (out.OutputBytes == 0 || other.OutputBytes < out.OutputBytes) {
		out.OutputBytes = other.OutputBytes
	}
	if other.MaxAttempts > 0 && (out.MaxAttempts == 0 || other.MaxAttempts < out.MaxAttempts) {
		out.MaxAttempts = other.MaxAttempts
	}
	if other.Slots > out.Slots {
		out.Slots = other.Slots
	}
	return out
}

// Sandbox admits an execution and accounts for what it consumes.
//
// An interface with one in-process implementation here, because the interesting
// implementation — one that runs the tool in a subprocess or a separate
// container and can actually kill it — needs process management that Phase 10D
// does not deliver. Declaring the seam now means the executor already asks
// permission before invoking anything, so adding real isolation later changes
// one constructor rather than the execution path.
type Sandbox interface {
	// Enter admits an execution, returning a Lease. A refused entry is a
	// budget violation, not a failure of the tool.
	Enter(d Descriptor, b Budget) (*Lease, error)
}

// Lease is one execution's hold on sandbox capacity.
//
// Release is idempotent and MUST be called; the executor defers it immediately
// after Enter succeeds. A leaked lease is a slot that never comes back, and a
// runtime that slowly loses slots looks exactly like a runtime whose downstream
// is slowly getting slower — a diagnosis that costs hours.
type Lease struct {
	descriptor Descriptor
	budget     Budget
	sandbox    *BudgetSandbox
	startedAt  time.Time
	outBytes   atomic.Int64
	released   atomic.Bool
}

// Budget returns the limits this lease was granted.
func (l *Lease) Budget() Budget { return l.budget }

// Descriptor returns what is running under this lease.
func (l *Lease) Descriptor() Descriptor { return l.descriptor }

// ChargeOutput accounts for produced bytes and reports whether the output
// budget is now exhausted.
//
// Called on every stream chunk as well as on the final result, because a stream
// that never terminates is the interesting way to exceed an output budget and
// checking only the final result never sees it.
func (l *Lease) ChargeOutput(n int) error {
	if n <= 0 {
		return nil
	}
	total := l.outBytes.Add(int64(n))
	if l.budget.OutputBytes > 0 && total > int64(l.budget.OutputBytes) {
		return fmt.Errorf("%w: %s produced %d bytes, budget is %d",
			ErrBudgetExceeded, l.descriptor, total, l.budget.OutputBytes)
	}
	return nil
}

// OutputBytes returns what has been charged so far.
func (l *Lease) OutputBytes() int64 { return l.outBytes.Load() }

// Release returns capacity. Safe to call more than once.
func (l *Lease) Release() {
	if l.released.Swap(true) {
		return
	}
	if l.sandbox != nil {
		l.sandbox.release(l.descriptor.Tool, l.budget.Slots)
	}
}

// BudgetSandbox is the in-process sandbox.
//
// It enforces exactly three things, and it is worth being precise about which:
//
//  1. Total concurrent slots across the runtime.
//  2. Concurrent executions per tool.
//  3. Output bytes per execution, charged incrementally.
//
// Wall clock and per-attempt timeouts are enforced by the executor through
// context cancellation, not here, because enforcing a deadline requires
// cancelling the work and a sandbox that only accounts cannot cancel.
//
// It does not enforce memory or CPU, and it says so rather than implying
// otherwise through a field name.
type BudgetSandbox struct {
	maxSlots    int
	defaultTool int

	mu       sync.Mutex
	inUse    int
	perTool  map[ToolID]int
	toolCaps map[ToolID]int

	admitted atomic.Uint64
	refused  atomic.Uint64
}

// NewBudgetSandbox builds an in-process sandbox.
func NewBudgetSandbox(maxSlots, defaultPerTool int) *BudgetSandbox {
	if maxSlots <= 0 {
		maxSlots = 64
	}
	if defaultPerTool <= 0 {
		defaultPerTool = 8
	}
	return &BudgetSandbox{
		maxSlots:    maxSlots,
		defaultTool: defaultPerTool,
		perTool:     make(map[ToolID]int),
		toolCaps:    make(map[ToolID]int),
	}
}

// SetToolConcurrency caps concurrent executions of one tool.
func (s *BudgetSandbox) SetToolConcurrency(t ToolID, n int) {
	if n <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolCaps[t] = n
}

// Enter admits an execution.
func (s *BudgetSandbox) Enter(d Descriptor, b Budget) (*Lease, error) {
	slots := b.Slots
	if slots <= 0 {
		slots = 1
	}

	s.mu.Lock()
	cap := s.defaultTool
	if c, ok := s.toolCaps[d.Tool]; ok {
		cap = c
	}
	// The refusal messages report counts, and those counts MUST be captured
	// while the mutex is still held.
	//
	// Both branches previously unlocked first and then read s.inUse and
	// s.perTool[d.Tool] again to format the error. That is a data race against
	// release(), which mutates both under the lock — and for the map it is the
	// dangerous kind: Go maps are not safe for concurrent read/write, so it can
	// abort the process with "concurrent map read and map write" rather than
	// merely print a stale number. It fires only on the refusal path under
	// concurrency, which is to say precisely under overload, which is when a
	// budget sandbox is the thing standing between the platform and a stampede.
	//
	// Found by the nightly repeated shuffled race gate, seed
	// 1786881587963648469, in TestStress_OverloadShedsCleanlyAndIsAccountedFor.
	//
	// s.maxSlots and cap are not captured because they are written only by
	// NewBudgetSandbox and never mutated afterwards.
	switch {
	case s.inUse+slots > s.maxSlots:
		inUse := s.inUse
		s.mu.Unlock()
		s.refused.Add(1)
		return nil, fmt.Errorf("%w: sandbox has %d of %d slots in use, %s wants %d",
			ErrBudgetExceeded, inUse, s.maxSlots, d, slots)
	case s.perTool[d.Tool]+1 > cap:
		running := s.perTool[d.Tool]
		s.mu.Unlock()
		s.refused.Add(1)
		return nil, fmt.Errorf("%w: %s already has %d concurrent executions, cap is %d",
			ErrBudgetExceeded, d.Tool, running, cap)
	}
	s.inUse += slots
	s.perTool[d.Tool]++
	s.mu.Unlock()

	s.admitted.Add(1)
	return &Lease{descriptor: d, budget: b, sandbox: s, startedAt: time.Now()}, nil
}

func (s *BudgetSandbox) release(t ToolID, slots int) {
	if slots <= 0 {
		slots = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inUse -= slots
	if s.inUse < 0 {
		s.inUse = 0
	}
	if n := s.perTool[t]; n <= 1 {
		delete(s.perTool, t)
	} else {
		s.perTool[t] = n - 1
	}
}

// InUse returns the slots currently held.
func (s *BudgetSandbox) InUse() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inUse
}

// Stats returns admission counts.
func (s *BudgetSandbox) Stats() (admitted, refused uint64) {
	return s.admitted.Load(), s.refused.Load()
}
