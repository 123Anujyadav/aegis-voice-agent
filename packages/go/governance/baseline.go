package governance

import "time"

// BaselinePolicies returns the platform's starting rule set.
//
// AN EXPLICIT CALL, NOT A DEFAULT. An engine with no policies denies
// everything, and that is the correct starting state: a safety engine that
// ships with permissive defaults is a safety engine whose most common
// production configuration was never reviewed by anybody.
//
// These are the floor, not the whole policy set. They exist so a deployment has
// something reviewable to start from and so the engine's behaviour can be
// demonstrated without a fixture inventing plausible-looking rules. Every one is
// deliberately conservative; a deployment loosens by writing higher-priority
// policies in its own scope, which appear in every trace.
//
// They contain NO business logic, NO vendor policy and NO fraud rules — those
// are excluded from Phase 10E and would be wrong here anyway, because a
// baseline that encodes one business's rules is a baseline every other business
// has to fight.
func BaselinePolicies() []Policy {
	return []Policy{
		secretsNeverStored(),
		personalDataNeedsBasis(),
		irreversibleNeedsConfirmation(),
		externalActionsRestricted(),
		readsAllowed(),
	}
}

// secretsNeverStored refuses authentication material everywhere.
//
// COMPLIANCE SCOPE, so no emergency can relax it. There is no incident that
// makes storing a credential in a conversation platform acceptable, and a
// mechanism that could be used to permit it would eventually be.
func secretsNeverStored() Policy {
	return Policy{
		ID: "baseline.secrets", Version: 1, Scope: ScopeCompliance, Priority: 1000,
		Title: "Secret material is never stored or transmitted",
		Description: "Authentication material — passwords, tokens, keys, OTPs — is " +
			"refused for every action kind. Identity holds credentials under its own " +
			"handling; a conversation platform that accepts them becomes one more " +
			"place they leak from.",
		Owner:   "platform-security",
		Enabled: true,
		Rules: []Rule{{
			Name: "refuse-secret-classification",
			When: []Condition{{Field: FieldClassification, Selector: SelAtLeast,
				Value: Str("secret")}},
			Then:        OutcomeDeny,
			Reason:      "secret_material",
			Explanation: "secret material is never stored or transmitted by this platform",
		}},
		Default:       OutcomeAllow,
		DefaultReason: "not_secret_material",
	}
}

// personalDataNeedsBasis requires consent before personal data is written,
// exported or sent anywhere.
//
// Reads are deliberately NOT covered. A platform that cannot read a subject's
// own preference without a consent check cannot answer their call, and the
// lawful basis for serving somebody who rang you is not the same question as
// the basis for retaining what they said.
func personalDataNeedsBasis() Policy {
	return Policy{
		ID: "baseline.personal-data", Version: 1, Scope: ScopeGlobal, Priority: 900,
		Title:       "Personal data requires a lawful basis before it is retained or shared",
		Description: "Writes, exports and notifications involving personal data require consent.",
		Owner:       "platform-privacy",
		Enabled:     true,
		Rules: []Rule{
			{
				Name: "personal-write-needs-consent",
				When: []Condition{
					{Field: FieldClassification, Selector: SelAtLeast, Value: Str("personal")},
					{Field: FieldClassification, Selector: SelLessThan, Value: Str("secret")},
					{Field: FieldOperation, Selector: SelIn,
						Values: []string{"write", "update", "export", "send", "schedule"}},
				},
				Then:        OutcomeRequireConsent,
				Reason:      "personal_data_retention",
				Explanation: "retaining or sharing personal data requires a lawful basis",
				Obligations: []Obligation{
					{Kind: ObligationConsent, Target: "data_processing",
						Reason: "personal_data", Policy: "baseline.personal-data"},
				},
			},
			{
				Name: "sensitive-external-denied",
				When: []Condition{
					{Field: FieldClassification, Selector: SelAtLeast, Value: Str("sensitive")},
					{Field: FieldClassification, Selector: SelLessThan, Value: Str("secret")},
					{Field: FieldKind, Selector: SelEquals, Value: Str("external")},
				},
				Then:        OutcomeDeny,
				Reason:      "sensitive_data_external",
				Explanation: "sensitive data does not leave the platform boundary",
			},
		},
		Default:       OutcomeAllow,
		DefaultReason: "no_personal_data_concern",
	}
}

// irreversibleNeedsConfirmation makes anything that cannot be undone ask first.
//
// The single most valuable rule in the baseline. An AI acting on somebody's
// behalf will eventually be wrong; the difference between an embarrassment and
// an incident is whether the wrong thing could be undone.
func irreversibleNeedsConfirmation() Policy {
	return Policy{
		ID: "baseline.irreversible", Version: 1, Scope: ScopeGlobal, Priority: 800,
		Title: "Irreversible actions require confirmation",
		Description: "Anything that cannot be undone — a sent message, a placed call, " +
			"a released payment — requires explicit confirmation before it proceeds.",
		Owner:   "platform-safety",
		Enabled: true,
		Rules: []Rule{{
			Name: "irreversible-confirm",
			When: []Condition{{Field: FieldReversibility, Selector: SelEquals,
				Value: Str("never")}},
			Then:        OutcomeRequireConfirmation,
			Reason:      "irreversible_action",
			Explanation: "this action cannot be undone, so it is confirmed before it happens",
			Obligations: []Obligation{
				{Kind: ObligationConfirmation, Target: "user",
					Reason: "irreversible", Policy: "baseline.irreversible"},
				{Kind: ObligationAudit, Target: "irreversible",
					Reason: "irreversible", Policy: "baseline.irreversible"},
			},
		}},
		Default:       OutcomeAllow,
		DefaultReason: "reversible_action",
	}
}

// externalActionsRestricted holds the boundary.
//
// ActionExternal is the catch-all kind, which makes it the kind nobody
// classified — and a kind nobody classified is a kind nobody reasoned about.
// The baseline treats it as the strictest by default.
func externalActionsRestricted() Policy {
	return Policy{
		ID: "baseline.external", Version: 1, Scope: ScopeGlobal, Priority: 700,
		Title: "External actions are escalated unless a policy permits them",
		Description: "Anything leaving the platform boundary that no more specific " +
			"policy addressed requires human approval.",
		Owner:   "platform-safety",
		Enabled: true,
		Match:   Match{Kinds: []ActionKind{ActionExternal}},
		Rules: []Rule{{
			Name:        "external-needs-human",
			Then:        OutcomeRequireHuman,
			Reason:      "unclassified_external_action",
			Explanation: "an external action with no governing policy is reviewed by a person",
			Obligations: []Obligation{
				{Kind: ObligationApproval, Target: "operator",
					Reason: "external_action", Policy: "baseline.external"},
			},
		}},
		Default:       OutcomeRequireHuman,
		DefaultReason: "unclassified_external_action",
	}
}

// readsAllowed is the one permissive baseline, and it is stated as a RULE
// rather than as a default so that it appears in every trace.
//
// Without it an engine loaded with only the baseline denies its own reads,
// which is technically safe and practically useless. Stating it explicitly
// means an operator reading a trace sees "allowed by baseline.reads" rather
// than a silent absence of denial — and can see exactly what to change if reads
// should be restricted.
func readsAllowed() Policy {
	return Policy{
		ID: "baseline.reads", Version: 1, Scope: ScopeGlobal, Priority: 100,
		Title:       "Non-mutating actions are permitted by default",
		Description: "Reads and conversation actions that change nothing are allowed.",
		Owner:       "platform-safety",
		Enabled:     true,
		Rules: []Rule{{
			Name: "allow-reads",
			When: []Condition{{Field: FieldReversibility, Selector: SelEquals,
				Value: Str("none")}},
			Then:        OutcomeAllow,
			Reason:      "non_mutating",
			Explanation: "the action changes nothing",
		}},
		Default:       OutcomeDeny,
		DefaultReason: "mutating_action_without_policy",
	}
}

// TemporaryPolicy is a helper for building a time-boxed policy.
//
// Provided because the one thing that goes wrong with temporary policies is
// that somebody forgets the end date, and [Policy.validate] refuses a temporary
// policy without one. A helper that takes the duration as a required argument
// makes the correct thing the easy thing.
func TemporaryPolicy(id PolicyID, owner string, from time.Time, d time.Duration, rules ...Rule) Policy {
	return Policy{
		ID: id, Version: 1, Scope: ScopeTemporary, Priority: 500,
		Title: "Temporary policy " + string(id), Owner: owner, Enabled: true,
		Rules: rules, Default: OutcomeDeny, DefaultReason: "temporary_policy_default",
		EffectiveFrom: from, EffectiveUntil: from.Add(d),
	}
}

// FeatureFlagPolicy is a helper for building a flag-scoped restriction.
//
// Deliberately produces a policy that can only RESTRICT: it sits in the lowest
// scope, so anything above it saying deny wins, and a flag can never be the
// reason something dangerous was permitted.
func FeatureFlagPolicy(id PolicyID, owner string, m Match, then Outcome, reason string) Policy {
	return Policy{
		ID: id, Version: 1, Scope: ScopeFeatureFlag, Priority: 500,
		Title: "Feature flag " + string(id), Owner: owner, Enabled: true, Match: m,
		Rules: []Rule{{
			Name: "flag", Then: then, Reason: reason,
			Explanation: "feature flag " + string(id) + " is in effect",
		}},
		Default: OutcomeAllow, DefaultReason: "flag_not_applicable",
	}
}
