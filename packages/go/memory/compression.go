package memory

import (
	"sort"
	"time"
)

// Summarizer condenses several records into one value.
//
// NOT IMPLEMENTED IN THIS MODULE, DELIBERATELY. The Phase 10C brief excludes
// LLM summarisation, and summarising a conversation is a model call. What lives
// here is the FRAMEWORK: when to compress, what to select, what budget to
// respect, and what to do with the result.
//
// A deployment supplies the implementation. Until it does, [Compressor] runs
// with a nil Summarizer and performs selection and pruning only, which is
// itself useful and is the mode the tests exercise.
type Summarizer interface {
	// Summarize condenses inputs into one value, ordered oldest first, within
	// the token budget. Returning an error leaves the inputs untouched.
	Summarize(inputs []*Record, budget TokenBudget) (Value, error)
}

// TokenBudget bounds a compression's output.
//
// Tokens rather than bytes because the consumer of a compressed memory is a
// model context window, and a byte budget mis-sizes it — the same text costs
// two to three times more tokens in Devanagari than in Latin under a
// byte-pair encoder.
type TokenBudget struct {
	// MaxTokens is the ceiling for the produced value.
	MaxTokens int
	// Reserve is held back for whatever the consumer adds around the memory.
	Reserve int
	// Estimator converts bytes to tokens. Nil uses [DefaultTokenEstimator].
	Estimator TokenEstimator
}

// Available returns the tokens a summariser may actually spend.
func (b TokenBudget) Available() int {
	n := b.MaxTokens - b.Reserve
	if n < 0 {
		return 0
	}
	return n
}

// TokenEstimator converts a payload to an approximate token count.
type TokenEstimator interface {
	// Estimate returns the approximate token count for a payload.
	Estimate([]byte) int
}

// heuristicEstimator estimates tokens without a tokeniser.
//
// It over-counts deliberately. Under-counting produces a context that a model
// rejects after the latency has been spent; over-counting merely compresses a
// little earlier than strictly necessary. The non-ASCII ratio is calibrated for
// Indic scripts, which the platform serves first and which tokenise far less
// efficiently than Latin.
type heuristicEstimator struct {
	asciiBytesPerToken    float64
	nonASCIIBytesPerToken float64
}

// DefaultTokenEstimator returns the estimator used unless overridden.
func DefaultTokenEstimator() TokenEstimator {
	return heuristicEstimator{asciiBytesPerToken: 3.5, nonASCIIBytesPerToken: 1.8}
}

// Estimate returns the approximate token count.
func (h heuristicEstimator) Estimate(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	nonASCII := 0
	for _, b := range data {
		if b > 127 {
			nonASCII++
		}
	}
	ratio := h.asciiBytesPerToken
	if float64(nonASCII)/float64(len(data)) > 0.15 {
		ratio = h.nonASCIIBytesPerToken
	}
	// Four tokens of per-record framing overhead. Omitting it is how a context
	// that "fits" gets rejected.
	return int(float64(len(data))/ratio) + 4
}

// CompressionTrigger names why compression ran.
type CompressionTrigger string

// The triggers the engine understands.
const (
	// TriggerCount is a record count over threshold.
	TriggerCount CompressionTrigger = "count"
	// TriggerBytes is a payload total over threshold.
	TriggerBytes CompressionTrigger = "bytes"
	// TriggerAge is records older than a horizon.
	TriggerAge CompressionTrigger = "age"
	// TriggerBudget is an explicit token budget request.
	TriggerBudget CompressionTrigger = "budget"
	// TriggerManual is an operator or caller request.
	TriggerManual CompressionTrigger = "manual"
)

// CompressionPolicy decides when and what to compress.
type CompressionPolicy struct {
	// MinRecords is the count below which compression never runs. Compressing
	// three records into one summary loses detail and saves nothing.
	MinRecords int

	// MaxRecords triggers compression when a (subject, kind) group exceeds it.
	MaxRecords int

	// MaxBytes triggers compression on payload total.
	MaxBytes int

	// AgeHorizon compresses records older than this. Recent memories keep
	// their detail; old ones become a summary.
	AgeHorizon time.Duration

	// KeepRecent is how many of the newest records are never compressed. The
	// most recent exchanges carry the most meaning, and summarising them is
	// where compression does the most damage.
	KeepRecent int

	// PreservePinned exempts pinned records. Defaults true: a pinned record is
	// one whose absence changes meaning, and folding it into a summary is a
	// form of absence.
	PreservePinned bool

	// PreserveSensitive exempts Sensitive records from being merged into a
	// shared summary.
	//
	// Defaults TRUE, and it is the least obvious rule here. Summarising several
	// records into one produces a value whose classification is the strictest
	// of its inputs — so one Sensitive record drags an entire summary up to
	// Sensitive, and every less-sensitive memory folded into it inherits
	// handling it never needed. Keeping them separate is both safer and
	// cheaper.
	PreserveSensitive bool

	// Budget bounds the produced summary.
	Budget TokenBudget
}

// DefaultCompressionPolicy returns the policy used unless overridden.
func DefaultCompressionPolicy() CompressionPolicy {
	return CompressionPolicy{
		MinRecords:        8,
		MaxRecords:        64,
		MaxBytes:          128 * 1024,
		AgeHorizon:        7 * 24 * time.Hour,
		KeepRecent:        5,
		PreservePinned:    true,
		PreserveSensitive: true,
		Budget:            TokenBudget{MaxTokens: 2048, Reserve: 256},
	}
}

func (p CompressionPolicy) validate() []string {
	var out []string
	if p.MinRecords < 2 {
		out = append(out, "compression: MinRecords must be at least 2")
	}
	if p.MaxRecords <= p.MinRecords {
		out = append(out, "compression: MaxRecords must exceed MinRecords")
	}
	if p.KeepRecent < 0 {
		out = append(out, "compression: KeepRecent cannot be negative")
	}
	if p.KeepRecent >= p.MaxRecords {
		out = append(out, "compression: KeepRecent must be below MaxRecords, "+
			"or compression can never select anything")
	}
	if p.Budget.MaxTokens <= p.Budget.Reserve {
		out = append(out, "compression: token budget must exceed its reserve")
	}
	return out
}

// CompressionPlan is what a compression would do, before it does it.
//
// Returned by [Compressor.Plan] so a caller — or an operator — can inspect a
// compression before it destroys detail. Compression is lossy and irreversible
// once the sources are removed; making the plan inspectable is the difference
// between a considered operation and a surprise.
type CompressionPlan struct {
	// Trigger names why.
	Trigger CompressionTrigger
	// Subject and Kind scope the group.
	Subject SubjectID
	Kind    Kind
	// Selected lists the records that would be compressed, oldest first.
	Selected []Key
	// Preserved lists records exempted, with the reason.
	Preserved map[Key]string
	// InputTokens is the estimated cost of the selected records.
	InputTokens int
	// BudgetTokens is what the summary may spend.
	BudgetTokens int
	// TargetKey is where the summary would be written.
	TargetKey Key
}

// Viable reports whether the plan is worth executing.
func (p CompressionPlan) Viable() bool { return len(p.Selected) >= 2 }

// Compressor selects and compresses memory groups.
type Compressor struct {
	store      *Store
	policy     CompressionPolicy
	summarizer Summarizer
	estimator  TokenEstimator
	metrics    *Metrics
}

// NewCompressor constructs a compressor.
//
// summarizer may be nil. A nil summariser makes [Compress] a pruning operation:
// it selects and removes, but produces no summary. That is a legitimate mode —
// and it is the only mode available until a deployment supplies a model.
func NewCompressor(s *Store, policy CompressionPolicy, summarizer Summarizer) (*Compressor, error) {
	if problems := policy.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	est := policy.Budget.Estimator
	if est == nil {
		est = DefaultTokenEstimator()
	}
	return &Compressor{store: s, policy: policy, summarizer: summarizer,
		estimator: est, metrics: s.metrics}, nil
}

// Plan computes what compression would do to a group, without doing it.
func (c *Compressor) Plan(subject SubjectID, kind Kind, trigger CompressionTrigger) CompressionPlan {
	keys := c.store.index.ByKind(subject, kind)
	c.metrics.IndexScans.Inc("primary_scan:kind")

	plan := CompressionPlan{
		Trigger: trigger, Subject: subject, Kind: kind,
		Preserved:    make(map[Key]string),
		BudgetTokens: c.policy.Budget.Available(),
		TargetKey:    Key{Subject: subject, Kind: kind, Name: compactName(kind)},
	}

	now := c.store.clock.Now()
	records := make([]*Record, 0, len(keys))
	for _, k := range keys {
		r, ok := c.store.index.Get(k)
		if !ok || !r.State.Readable() {
			continue
		}
		records = append(records, r.Clone())
	}
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})

	if len(records) < c.policy.MinRecords {
		return plan
	}

	// The newest KeepRecent are never candidates. Recent exchanges carry the
	// most meaning and summarising them is where compression does most damage.
	cutoff := len(records) - c.policy.KeepRecent
	if cutoff < 0 {
		cutoff = 0
	}

	// DURABLE EXEMPTIONS ARE REPORTED BEFORE INCIDENTAL ONES.
	//
	// A record can be exempt for several reasons at once — a pinned record that
	// also happens to be recent, say. The plan reports one, and it should be the
	// reason that will still hold tomorrow: "pinned" is a property of the
	// record, "recent" is a property of where the window happens to fall today.
	// Reporting the transient reason tells an operator the exemption is
	// temporary when it is not.
	for i, r := range records {
		if r.Key == plan.TargetKey {
			// A previous summary is itself a candidate — compressing a summary
			// with newer records is how a rolling summary works — but it must
			// not be selected as its own target and removed.
			plan.Preserved[r.Key] = "target"
			continue
		}
		if c.policy.PreservePinned && r.Pinned {
			plan.Preserved[r.Key] = "pinned"
			continue
		}
		if c.policy.PreserveSensitive && r.Sensitivity >= Sensitive {
			plan.Preserved[r.Key] = "sensitive"
			continue
		}
		if i >= cutoff {
			plan.Preserved[r.Key] = "recent"
			continue
		}
		if trigger == TriggerAge && now.Sub(r.CreatedAt) < c.policy.AgeHorizon {
			plan.Preserved[r.Key] = "within_horizon"
			continue
		}
		plan.Selected = append(plan.Selected, r.Key)
		plan.InputTokens += c.estimator.Estimate(r.Value.Data)
	}
	return plan
}

// Compress executes a plan.
//
// With a summariser, the selected records become one summary and are removed.
// Without one, they are pruned — removed with nothing written in their place,
// which is a decision the caller makes by choosing not to supply a summariser.
func (c *Compressor) Compress(plan CompressionPlan, actor string) (*Record, error) {
	if !plan.Viable() {
		return nil, nil
	}

	inputs := make([]*Record, 0, len(plan.Selected))
	inputBytes := 0
	for _, k := range plan.Selected {
		r, err := c.store.Retrieve(k, actor)
		if err != nil {
			continue // expired or redacted between plan and execution
		}
		inputs = append(inputs, r)
		inputBytes += r.Value.Size()
	}
	if len(inputs) < 2 {
		return nil, nil
	}

	if c.summarizer == nil {
		for _, r := range inputs {
			_ = c.store.Delete(r.Key, "pruned")
			c.metrics.Pruned.Inc(string(plan.Trigger))
		}
		c.metrics.Compressions.Inc(string(plan.Trigger))
		return nil, nil
	}

	value, err := c.summarizer.Summarize(inputs, c.policy.Budget)
	if err != nil {
		// A failed summarisation leaves everything intact. Destroying the
		// inputs and failing to write the summary would lose the memories
		// outright — the worst possible outcome of a compression.
		return nil, err
	}

	summary := inputs[0].Clone()
	summary.Key = plan.TargetKey
	summary.Value = value
	summary.Version = 0
	summary.Provenance = Provenance{Source: "compressor", Derived: true}
	summary.Pinned = false

	// The summary inherits the strictest classification and retention of its
	// inputs, for the same reason a merge does: anything else launders a
	// Sensitive memory into a lower class.
	for _, in := range inputs {
		if in.Sensitivity > summary.Sensitivity {
			summary.Sensitivity = in.Sensitivity
			summary.ConsentRef = in.ConsentRef
		}
		if in.Retention > summary.Retention {
			summary.Retention = in.Retention
		}
	}

	stored, err := c.store.Store(summary)
	if err != nil {
		return nil, err
	}

	for _, in := range inputs {
		if in.Key == stored.Key {
			continue
		}
		_ = c.store.Delete(in.Key, "compressed")
	}

	outputBytes := stored.Value.Size()
	if inputBytes > 0 {
		c.metrics.CompressionRatio.Observe(float64(outputBytes) / float64(inputBytes))
	}
	c.metrics.Compressions.Inc(string(plan.Trigger))
	c.store.emit(EventMerged, stored, string(plan.Trigger), stored.Tier)
	return stored, nil
}

// Prune removes records without summarising, oldest first, until the group is
// within MaxRecords.
//
// The bluntest instrument here, and the only one available with no summariser.
// It is bounded by the same preservation rules as compression, so pinned and
// sensitive records survive a prune.
func (c *Compressor) Prune(subject SubjectID, kind Kind) int {
	plan := c.Plan(subject, kind, TriggerCount)
	excess := len(plan.Selected) + len(plan.Preserved) - c.policy.MaxRecords
	if excess <= 0 {
		return 0
	}
	removed := 0
	for _, k := range plan.Selected {
		if removed >= excess {
			break
		}
		if err := c.store.Delete(k, "pruned"); err == nil {
			removed++
			c.metrics.Pruned.Inc("count")
		}
	}
	return removed
}

// compactName is the reserved record name a compressed summary takes.
func compactName(k Kind) string { return "__compact_" + k.String() }
