// Package eventbus defines the conventions every Kafka producer and consumer in
// the platform follows.
//
// At Phase 2 it owns the topic-naming contract and the interfaces services code
// against. The concrete Kafka client is introduced alongside the first service
// that publishes a real event, so that the driver choice is made with an actual
// workload in view rather than guessed at in advance.
//
// This module is stdlib-only for the same reason as packages/go/platform: it is
// imported by every service that touches the event backbone, so its dependency
// graph is the union of their mandatory supply-chain exposure.
package eventbus

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Topic naming is not cosmetic. Kafka topics are effectively permanent: once a
// producer and a set of consumers agree on a name, renaming it requires a
// dual-write migration with a cutover, coordinated across every service that
// touches it. Getting the shape right at the start is far cheaper than any
// later correction, which is why this file exists before the first topic does.

// topicPattern matches the naming convention from Phase 1 §5:
//
//	<domain>.<entity>.<event>.v<major>
//
// for example "telephony.call.answered.v1".
//
// Each segment is lowercase alphanumeric with underscores permitted inside a
// segment but not at its edges. Hyphens are excluded deliberately: Kafka
// permits them, but they collide with the metric-name normalisation applied by
// Prometheus exporters, which silently rewrites them to underscores and makes
// two distinct topics indistinguishable on a dashboard.
var topicPattern = regexp.MustCompile(
	`^([a-z][a-z0-9_]*[a-z0-9]|[a-z])\.([a-z][a-z0-9_]*[a-z0-9]|[a-z])\.([a-z][a-z0-9_]*[a-z0-9]|[a-z])\.v([1-9][0-9]*)$`,
)

// maxTopicNameLength is Kafka's own limit. Exceeding it fails at topic creation
// with a broker-side error that names neither the topic nor the producer, so it
// is checked here where the message can be useful.
const maxTopicNameLength = 249

// ErrInvalidTopic indicates a topic name that violates the naming convention.
var ErrInvalidTopic = errors.New("eventbus: invalid topic name")

// Topic is a validated Kafka topic name.
//
// It is a distinct type rather than a bare string so that an arbitrary string
// cannot be passed where a topic is expected. Construction goes through
// ParseTopic, which means an invalid name cannot exist at runtime.
type Topic struct {
	domain  string
	entity  string
	event   string
	version int
	full    string
}

// ParseTopic validates a topic name and returns its parsed form.
//
// It returns an error wrapping ErrInvalidTopic when the name does not match
// <domain>.<entity>.<event>.v<major>, so callers can test with errors.Is.
//
// The version suffix is mandatory from the first topic. Retrofitting versioning
// onto a live topic requires producing to both the old and new names until
// every consumer has migrated — weeks of dual-write for something that costs
// three characters to get right initially.
func ParseTopic(name string) (Topic, error) {
	if name == "" {
		return Topic{}, fmt.Errorf("%w: name is empty", ErrInvalidTopic)
	}
	if len(name) > maxTopicNameLength {
		return Topic{}, fmt.Errorf(
			"%w: %q is %d characters, Kafka permits at most %d",
			ErrInvalidTopic, name, len(name), maxTopicNameLength)
	}

	matches := topicPattern.FindStringSubmatch(name)
	if matches == nil {
		return Topic{}, fmt.Errorf(
			"%w: %q does not match <domain>.<entity>.<event>.v<major>, "+
				"for example telephony.call.answered.v1",
			ErrInvalidTopic, name)
	}

	// The pattern guarantees a non-empty run of digits with no leading zero, so
	// this conversion cannot fail. The error is checked regardless, because an
	// unchecked conversion is an invitation for the pattern and the parser to
	// drift apart during a later edit.
	version, err := strconv.Atoi(matches[4])
	if err != nil {
		return Topic{}, fmt.Errorf("%w: unparseable version in %q", ErrInvalidTopic, name)
	}

	return Topic{
		domain:  matches[1],
		entity:  matches[2],
		event:   matches[3],
		version: version,
		full:    name,
	}, nil
}

// MustParseTopic is ParseTopic for package-level topic constants.
//
// It panics on an invalid name. That is appropriate only at package
// initialisation, where the name is a compile-time literal and a failure is a
// programming error that must stop the process immediately rather than surface
// as a runtime error on the first publish.
func MustParseTopic(name string) Topic {
	topic, err := ParseTopic(name)
	if err != nil {
		panic(err)
	}
	return topic
}

// String returns the full topic name.
func (t Topic) String() string { return t.full }

// Domain returns the owning domain segment, for example "telephony".
func (t Topic) Domain() string { return t.domain }

// Entity returns the entity segment, for example "call".
func (t Topic) Entity() string { return t.entity }

// Event returns the event segment, for example "answered".
func (t Topic) Event() string { return t.event }

// Version returns the schema major version encoded in the name.
func (t Topic) Version() int { return t.version }

// IsZero reports whether the topic is the zero value, which no valid topic ever
// is. Useful for detecting a struct field that was never initialised.
func (t Topic) IsZero() bool { return t.full == "" }

// consumerGroupPattern matches the <service>.<purpose> convention from
// Phase 1 §5, for example "fraud-engine.scoring".
//
// Hyphens are permitted here, unlike in topic names, because a consumer group
// embeds a service name and service directories are kebab-case.
var consumerGroupPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*[a-z0-9]\.[a-z][a-z0-9_-]*[a-z0-9]$`)

// ErrInvalidConsumerGroup indicates a group id that violates the convention.
var ErrInvalidConsumerGroup = errors.New("eventbus: invalid consumer group")

// ValidateConsumerGroup checks a consumer group id against the convention.
//
// The group id determines offset ownership, so two services accidentally
// sharing one silently split a partition set between them and each processes
// only part of the stream. That failure is close to invisible — no error is
// raised, throughput simply halves — which is why the naming is validated
// rather than trusted.
func ValidateConsumerGroup(group string) error {
	if group == "" {
		return fmt.Errorf("%w: group is empty", ErrInvalidConsumerGroup)
	}
	if !consumerGroupPattern.MatchString(group) {
		return fmt.Errorf(
			"%w: %q does not match <service>.<purpose>, for example fraud-engine.scoring",
			ErrInvalidConsumerGroup, group)
	}
	return nil
}

// DeadLetterTopic returns the dead-letter topic for t.
//
// Every consumer needs somewhere to put a message it cannot process. Without a
// dead-letter path a poison message is retried forever, which halts the
// partition and stalls every subsequent message behind it — a single malformed
// record taking down a whole event stream.
//
// The suffix is appended to the full name, so the dead-letter topic for
// telephony.call.answered.v1 is telephony.call.answered.v1.dlq. It is
// deliberately NOT itself a valid Topic: a dead-letter topic must never be
// consumed by the normal pipeline, and making its name unparseable by
// ParseTopic enforces that structurally.
func (t Topic) DeadLetterTopic() string {
	return t.full + ".dlq"
}

// RetryTopic returns the delayed-retry topic for t at the given attempt.
//
// Retries are staged through separate topics rather than by re-queuing onto the
// source topic. Re-queuing preserves neither ordering nor the original offset,
// and it mixes first-attempt traffic with retry traffic so that a retry storm
// is indistinguishable from a genuine load increase on every dashboard.
//
// attempt must be positive; a non-positive value returns an empty string rather
// than a name that would silently collide with the source topic.
func (t Topic) RetryTopic(attempt int) string {
	if attempt < 1 {
		return ""
	}
	return t.full + ".retry-" + strconv.Itoa(attempt)
}

// NormaliseForMetrics converts a topic name into a form safe for use as a
// metric label value.
//
// Prometheus label values may contain dots, but many dashboard and alerting
// tools treat a dot as a path separator, so a raw topic name fragments queries
// in ways that are only discovered when an alert fails to fire.
func (t Topic) NormaliseForMetrics() string {
	return strings.ReplaceAll(t.full, ".", "_")
}
