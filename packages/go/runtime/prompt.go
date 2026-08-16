package runtime

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// PromptRecord is one immutable version of a registered prompt.
//
// The runtime STORES and SERVES prompts. It does not author them, render them,
// or interpolate variables into them — that is prompt templating, which is
// explicitly out of scope for Phase 10A and belongs to the orchestration layer.
// What lives here is versioning, rollout gating and retrieval.
type PromptRecord struct {
	// ID identifies the prompt family.
	ID PromptID

	// Version is the immutable version number within the family.
	Version int

	// Body is the prompt text.
	//
	// Classified INTERNAL, not SECRET. A prompt is a reviewable artefact, and
	// treating it as a credential prevents exactly the review that keeps it
	// safe (docs/domain/14 §14.16).
	Body string

	// Tier is the ladder rung this prompt is written for. A prompt tuned for a
	// fast model is frequently wrong on a deep one, so serving is tier-scoped.
	Tier ModelTier

	// Digest is a content hash, used to detect an accidental edit of a
	// published version.
	Digest string

	// PublishedAt records when the version was published.
	PublishedAt time.Time

	// EvaluationRef links to the evaluation run that gated this version.
	//
	// EMPTY IS MEANINGFUL: a version with no evaluation reference cannot be
	// activated. Phase 4 INV-AI-12 makes a passing evaluation a precondition of
	// rollout, and enforcing it here means the gate cannot be skipped by an
	// operator in a hurry.
	EvaluationRef string
}

// PromptRegistry stores versioned prompts and serves the active version.
//
// It is read-heavy on the request path and written only by a rollout, so it
// uses an RWMutex and returns values rather than pointers.
type PromptRegistry struct {
	mu       sync.RWMutex
	versions map[PromptID][]PromptRecord
	active   map[PromptID]int
	clock    Clock
}

// NewPromptRegistry returns an empty registry.
func NewPromptRegistry(clock Clock) *PromptRegistry {
	if clock == nil {
		clock = SystemClock{}
	}
	return &PromptRegistry{
		versions: make(map[PromptID][]PromptRecord),
		active:   make(map[PromptID]int),
		clock:    clock,
	}
}

// Publish adds a new version. Versions are immutable once published.
func (r *PromptRegistry) Publish(rec PromptRecord) error {
	if rec.ID == "" {
		return &ConfigError{Problems: []string{"prompt: ID is required"}}
	}
	if rec.Body == "" {
		return &ConfigError{Problems: []string{
			fmt.Sprintf("prompt %s: body cannot be empty", rec.ID)}}
	}
	if !rec.Tier.Valid() || rec.Tier == TierNone {
		return &ConfigError{Problems: []string{
			fmt.Sprintf("prompt %s: tier must name a model-invoking rung", rec.ID)}}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	existing := r.versions[rec.ID]
	for _, v := range existing {
		if v.Version == rec.Version {
			// Refuse rather than overwrite. A mutated published version means
			// a log line naming version 3 no longer identifies what actually
			// ran, which destroys the audit trail a rollout depends on.
			return fmt.Errorf("prompt %s: version %d is already published and is immutable",
				rec.ID, rec.Version)
		}
	}
	if rec.Version <= 0 {
		rec.Version = len(existing) + 1
	}
	if rec.PublishedAt.IsZero() {
		rec.PublishedAt = r.clock.Now()
	}
	if rec.Digest == "" {
		rec.Digest = digest(rec.Body)
	}

	r.versions[rec.ID] = append(existing, rec)
	sort.Slice(r.versions[rec.ID], func(i, j int) bool {
		return r.versions[rec.ID][i].Version < r.versions[rec.ID][j].Version
	})
	return nil
}

// Activate makes a version the one served by Get.
//
// It refuses a version with no evaluation reference (INV-AI-12). This is the
// runtime's half of the rollout gate: the tool that starts a rollout checks
// evaluation results, and this refuses to serve an ungated version even if that
// check is bypassed.
func (r *PromptRegistry) Activate(id PromptID, version int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, v := range r.versions[id] {
		if v.Version != version {
			continue
		}
		if v.EvaluationRef == "" {
			return invariant("INV-AI-12",
				"prompt %s version %d has no evaluation reference and cannot be activated",
				id, version)
		}
		r.active[id] = version
		return nil
	}
	return fmt.Errorf("%w: prompt %s version %d", ErrNotFound, id, version)
}

// Get returns the active version of a prompt for a tier.
func (r *PromptRegistry) Get(id PromptID, tier ModelTier) (PromptRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	active, ok := r.active[id]
	if !ok {
		return PromptRecord{}, fmt.Errorf("%w: prompt %s has no active version", ErrNotFound, id)
	}
	for _, v := range r.versions[id] {
		if v.Version == active && v.Tier == tier {
			return v, nil
		}
	}
	return PromptRecord{}, fmt.Errorf("%w: prompt %s version %d for tier %s",
		ErrNotFound, id, active, tier)
}

// GetVersion returns a specific version regardless of activation. Used by
// diagnostics and by an evaluation harness, never on the request path.
func (r *PromptRegistry) GetVersion(id PromptID, version int) (PromptRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.versions[id] {
		if v.Version == version {
			return v, nil
		}
	}
	return PromptRecord{}, fmt.Errorf("%w: prompt %s version %d", ErrNotFound, id, version)
}

// Versions returns every version of a prompt, oldest first.
func (r *PromptRegistry) Versions(id PromptID) []PromptRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]PromptRecord, len(r.versions[id]))
	copy(out, r.versions[id])
	return out
}

// digest computes a stable content hash.
//
// FNV-1a rather than SHA-256: this detects accidental mutation, not tampering.
// A cryptographic digest would imply a guarantee against a determined
// adversary that this function does not provide, and implying it would be
// worse than not having it.
func digest(s string) string {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	var h uint64 = offset64
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return fmt.Sprintf("fnv1a-%016x", h)
}
