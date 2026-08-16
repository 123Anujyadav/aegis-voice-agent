// packages/go/eventbus - Kafka conventions shared by every producer and consumer.
//
// Admitted to packages/ under Phase 1 SS4 because Kafka correctness (ordering,
// idempotency, poison-message handling, the transactional outbox) is subtle and
// must not be reimplemented per service. Six slightly different implementations
// of exactly-once semantics is six different bugs.
//
// Like packages/go/platform this module is stdlib-only at Phase 2: it defines
// the topic-naming contract and the interfaces services code against. The
// concrete Kafka client is introduced with the first service that produces a
// real event, so that the driver choice is made with a real workload in view.
module github.com/callscreen/callscreen-platform/packages/go/eventbus

go 1.23.0