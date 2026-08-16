package memory

import (
	"sort"
	"time"
)

// ContextScope names a slice of assembled context.
type ContextScope int

const (
	// ScopeConversation is what happened in this dialogue.
	ScopeConversation ContextScope = iota
	// ScopeBusiness is organisation configuration and knowledge.
	ScopeBusiness
	// ScopePersonal is durable knowledge about the subject.
	ScopePersonal
	// ScopeTemporary is working state for the current decision.
	ScopeTemporary
	// ScopeShared is context visible across concurrent interactions.
	ScopeShared
	// ScopeRuntime is operating state — policies, flags, limits.
	ScopeRuntime
)

// String renders the scope for logs and metric labels.
func (s ContextScope) String() string {
	switch s {
	case ScopeBusiness:
		return "business"
	case ScopePersonal:
		return "personal"
	case ScopeTemporary:
		return "temporary"
	case ScopeShared:
		return "shared"
	case ScopeRuntime:
		return "runtime"
	default:
		return "conversation"
	}
}

// scopeSpec describes how a scope is sourced.
type scopeSpec struct {
	kinds    []Kind
	tiers    []Tier
	order    Order
	priority int // lower is filled first under budget pressure
}

// scopeSpecs maps each scope to the kinds and tiers that populate it.
//
// PRIORITY IS THE INTERESTING COLUMN. Under a token budget the builder fills
// low-priority scopes first, so what survives truncation is what a caller can
// least afford to lose. Runtime policy and stated preferences come before
// conversation detail — an assistant that forgets an operating limit is
// dangerous, one that forgets a stated preference is rude, and one that forgets
// the last three exchanges is merely forgetful.
func scopeSpecs() map[ContextScope]scopeSpec {
	return map[ContextScope]scopeSpec{
		ScopeRuntime: {
			kinds: []Kind{KindPolicy}, order: OrderKey, priority: 0,
		},
		ScopePersonal: {
			kinds: []Kind{KindPreference, KindUser},
			tiers: []Tier{TierLongTerm, TierShortTerm}, order: OrderKey, priority: 1,
		},
		ScopeBusiness: {
			kinds: []Kind{KindBusiness}, order: OrderKey, priority: 2,
		},
		ScopeConversation: {
			kinds: []Kind{KindConversation}, order: OrderRecent, priority: 3,
		},
		ScopeShared: {
			kinds: []Kind{KindContact}, order: OrderRecent, priority: 4,
		},
		ScopeTemporary: {
			kinds: []Kind{KindScratchpad}, tiers: []Tier{TierWorking},
			order: OrderRecent, priority: 5,
		},
	}
}

// ContextRequest describes the context to assemble.
type ContextRequest struct {
	// Subject scopes every lookup. Required.
	Subject SubjectID

	// ConversationID narrows the conversation scope. Optional.
	ConversationID string

	// SessionID narrows to a session. Optional.
	SessionID string

	// BusinessID narrows the business scope. Optional.
	BusinessID string

	// Scopes selects which slices to build. Empty means every scope.
	Scopes []ContextScope

	// Budget bounds the assembled context.
	Budget TokenBudget

	// MaxSensitivity refuses records above a ceiling. Defaults to Public, so a
	// caller that forgets to state one gets the least.
	MaxSensitivity Sensitivity

	// PerScopeLimit caps records per scope before the budget applies.
	PerScopeLimit int

	// Actor is recorded in audit entries for Sensitive reads.
	Actor string

	// Since bounds conversation and shared scopes to recent history.
	Since time.Time
}

// ContextSlice is one assembled scope.
type ContextSlice struct {
	// Scope names it.
	Scope ContextScope
	// Records is the content, ordered by the scope's spec.
	Records []*Record
	// Tokens is the estimated cost.
	Tokens int
	// Truncated reports that the budget cut this scope.
	Truncated bool
	// Available is how many records matched before truncation.
	Available int
}

// AssembledContext is the builder's output.
type AssembledContext struct {
	// Subject is whose context this is.
	Subject SubjectID
	// Slices are the assembled scopes, in priority order.
	Slices []ContextSlice
	// TotalTokens is the estimated cost of everything included.
	TotalTokens int
	// BudgetTokens is what was available.
	BudgetTokens int
	// Truncated reports that any scope was cut.
	Truncated bool
	// Dropped names scopes that were omitted entirely for want of budget.
	Dropped []ContextScope
	// BuiltAt is the assembly instant.
	BuiltAt time.Time
}

// Slice returns one scope's slice.
func (a AssembledContext) Slice(s ContextScope) (ContextSlice, bool) {
	for _, sl := range a.Slices {
		if sl.Scope == s {
			return sl, true
		}
	}
	return ContextSlice{}, false
}

// Records returns every record across every slice, in slice order.
func (a AssembledContext) Records() []*Record {
	var out []*Record
	for _, sl := range a.Slices {
		out = append(out, sl.Records...)
	}
	return out
}

// ContextBuilder assembles memory into the context a caller needs.
//
// It is the read-side counterpart of the store: the store knows how to keep a
// memory, the builder knows how to choose which memories matter now. It writes
// nothing — assembling context must never mutate what it assembles, or two
// concurrent builds on one subject would each change what the other sees.
type ContextBuilder struct {
	retriever Retriever
	store     *Store
	estimator TokenEstimator
	metrics   *Metrics
}

// NewContextBuilder constructs a builder.
func NewContextBuilder(s *Store, r Retriever, est TokenEstimator) *ContextBuilder {
	if est == nil {
		est = DefaultTokenEstimator()
	}
	return &ContextBuilder{retriever: r, store: s, estimator: est, metrics: s.metrics}
}

// Build assembles context within a token budget.
//
// Scopes are filled in priority order and the budget is spent as it goes, so a
// tight budget yields complete high-priority scopes and dropped low-priority
// ones rather than every scope half-filled. A partially-filled scope is worse
// than an absent one: a caller can reason about missing conversation history,
// but not about a preference list that silently contains three of five entries.
func (b *ContextBuilder) Build(req ContextRequest) (AssembledContext, error) {
	if req.Subject == "" {
		return AssembledContext{}, invariant("INV-MEM-5",
			"context assembly requires a subject")
	}

	budget := req.Budget.Available()
	if budget <= 0 {
		budget = 4096
	}
	perScope := req.PerScopeLimit
	if perScope <= 0 {
		perScope = 25
	}

	specs := scopeSpecs()
	wanted := req.Scopes
	if len(wanted) == 0 {
		wanted = []ContextScope{ScopeRuntime, ScopePersonal, ScopeBusiness,
			ScopeConversation, ScopeShared, ScopeTemporary}
	}
	sort.SliceStable(wanted, func(i, j int) bool {
		return specs[wanted[i]].priority < specs[wanted[j]].priority
	})

	out := AssembledContext{
		Subject: req.Subject, BudgetTokens: budget,
		BuiltAt: b.store.clock.Now(),
	}
	remaining := budget

	for _, scope := range wanted {
		spec := specs[scope]

		q := Query{
			Subject:        req.Subject,
			Kinds:          spec.kinds,
			Tiers:          spec.tiers,
			Order:          spec.order,
			Limit:          perScope,
			MaxSensitivity: req.MaxSensitivity,
		}
		switch scope {
		case ScopeConversation:
			if req.ConversationID != "" {
				q.Attribute, q.Value = AttrConversation, req.ConversationID
			}
			if !req.Since.IsZero() {
				q.From = req.Since
			}
		case ScopeBusiness:
			if req.BusinessID != "" {
				q.Attribute, q.Value = AttrBusiness, req.BusinessID
			}
		case ScopeShared:
			if req.SessionID != "" {
				q.Attribute, q.Value = AttrSession, req.SessionID
			}
		}

		res, err := b.retriever.Search(q, req.Actor)
		if err != nil {
			// A scope that cannot be read is dropped, not fatal. Failing the
			// whole assembly because one index was unavailable would take an
			// entire conversation down for a missing preference.
			out.Dropped = append(out.Dropped, scope)
			continue
		}

		slice := ContextSlice{Scope: scope, Available: res.Total}
		for _, rec := range res.Records {
			cost := b.estimator.Estimate(rec.Value.Data)
			if cost > remaining {
				slice.Truncated = true
				out.Truncated = true
				break
			}
			slice.Records = append(slice.Records, rec)
			slice.Tokens += cost
			remaining -= cost
		}

		if len(slice.Records) == 0 {
			if res.Total > 0 {
				out.Dropped = append(out.Dropped, scope)
			}
			continue
		}
		out.Slices = append(out.Slices, slice)
		out.TotalTokens += slice.Tokens
	}

	return out, nil
}

// BuildConversation is the common case: everything relevant to one dialogue.
func (b *ContextBuilder) BuildConversation(subject SubjectID, conversationID string, budget TokenBudget, actor string) (AssembledContext, error) {
	return b.Build(ContextRequest{
		Subject: subject, ConversationID: conversationID,
		Budget: budget, MaxSensitivity: Personal, Actor: actor,
	})
}
