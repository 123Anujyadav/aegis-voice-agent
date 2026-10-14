// Package telephonyredis is the Redis implementation of callback.Store: durable,
// replica-shared deduplication of carrier callbacks (ADR-0009 §5 — idempotency
// keys are Redis ephemeral state).
//
// # Atomicity
//
// Every state change for an event key is ONE Lua script, so it is atomic on
// the Redis server no matter how many replicas race:
//
//	reserve  absent            → in flight (owner token, lease = PX Lease)   "reserved"
//	         in flight, same fp → "inflight";   settled, same fp → "duplicate" + result
//	         any, other fp      → "mismatch"
//	settle   in flight, token matches → settled (PX Retention)               "ok"
//	release  in flight, token matches → deleted                              "ok"
//	         otherwise                → "notowner" / "gone" (no change)
//
// # Leases
//
// A reservation is a key with a TTL of Config.Lease. A holder that crashes
// never settles or releases; the key expires and the event can be reserved
// again — with a NEW token. The old holder can then neither settle nor release
// the new reservation: both check the token inside the script.
//
// The lease must exceed the longest time one event takes to process. If it
// does not, a second worker may process the same event while the first is
// still working; the lifecycle's state-based judgement (P1-4) then refuses a
// second transition, but the first worker's Settle fails with ErrNotOwner.
//
// # Fail closed
//
// Any failure to reach Redis, an unexpected reply, a timeout or a
// cancellation is returned as an error wrapping [ErrUnavailable]. It is never
// read as "new event", "not terminal" or "settled".
//
// # Retention
//
// Settled keys, terminal marks and the conflict list expire after
// Config.Retention: Redis TTLs are the pruning mechanism. No production value
// is chosen here; see the P1-11 report.
package telephonyredis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
	"github.com/callscreen/callscreen-platform/packages/go/telephony/callback"
)

// Errors.
var (
	// ErrUnavailable: Redis could not give a definite answer. The operation
	// did not happen, or its outcome is unknown; either way the caller must
	// not proceed as if it had succeeded.
	ErrUnavailable = fmt.Errorf("telephonyredis: deduplication store unavailable: %w", callback.ErrStoreUnavailable)

	// ErrInvalidInput: a key, call ID or token is empty or malformed. Nothing
	// was sent to Redis.
	ErrInvalidInput = errors.New("telephonyredis: invalid input")
)

// Config configures deduplication. Every field is required; there are no
// defaults, because the right values are operational decisions.
type Config struct {
	// KeyPrefix namespaces every key, e.g. per environment. It must end in
	// ':' and contain only [A-Za-z0-9:_.-].
	KeyPrefix string
	// Lease is how long a reservation is held before it can be taken over.
	Lease time.Duration
	// Retention is how long settled keys, terminal marks and conflicts are
	// kept. It bounds the window in which a redelivery is recognised as a
	// duplicate. Must be at least Lease.
	Retention time.Duration
	// MaxConflicts bounds the conflict list.
	MaxConflicts int
}

func (c Config) validate() []string {
	var p []string
	if c.KeyPrefix == "" || !strings.HasSuffix(c.KeyPrefix, ":") || !keySafe(c.KeyPrefix) {
		p = append(p, "KeyPrefix must be non-empty, end in ':' and use only [A-Za-z0-9:_.-]")
	}
	if c.Lease < time.Millisecond {
		p = append(p, "Lease must be at least 1ms")
	}
	if c.Retention < c.Lease || c.Retention <= 0 {
		p = append(p, "Retention must be positive and at least Lease")
	}
	if c.MaxConflicts <= 0 {
		p = append(p, "MaxConflicts must be positive")
	}
	return p
}

func keySafe(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == ':', r == '_', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

// Store implements callback.Store on Redis.
type Store struct {
	cl  *client
	cfg Config
}

var _ callback.Store = (*Store)(nil)

// New validates both configurations. It does not connect: the first command
// does, and [Store.Ping] checks reachability explicitly.
func New(cc ClientConfig, cfg Config) (*Store, error) {
	problems := append(cc.validate(), cfg.validate()...)
	if len(problems) > 0 {
		return nil, fmt.Errorf("telephonyredis: invalid configuration: %s", strings.Join(problems, "; "))
	}
	return &Store{cl: newClient(cc), cfg: cfg}, nil
}

// Close releases idle connections.
func (s *Store) Close() error {
	s.cl.close()
	return nil
}

// Ping checks that Redis answers.
func (s *Store) Ping(ctx context.Context) error {
	v, err := s.cl.do(ctx, "PING")
	if err != nil {
		return unavailable("ping", err)
	}
	if v.kind != '+' || v.str != "PONG" {
		return unavailable("ping", errProtocol)
	}
	return nil
}

func unavailable(op string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrUnavailable, op, err)
}

func ms(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }

func (s *Store) eventKey(k callback.Key) (string, error) {
	if !k.Provider.Valid() || k.EventID == "" || len(k.EventID) > 128 || !keySafe(k.EventID) {
		return "", fmt.Errorf("%w: event key", ErrInvalidInput)
	}
	return s.cfg.KeyPrefix + "evt:" + string(k.Provider) + ":" + k.EventID, nil
}

func (s *Store) terminalKey(id telephony.CallID) (string, error) {
	if !id.Valid() || len(id) > 128 || !keySafe(string(id)) {
		return "", fmt.Errorf("%w: call ID", ErrInvalidInput)
	}
	return s.cfg.KeyPrefix + "term:" + string(id), nil
}

func (s *Store) conflictKey() string { return s.cfg.KeyPrefix + "conflicts" }

// fingerprint encodes fp unambiguously: neither a kind nor a carrier call ID
// can contain '|'.
func fingerprint(fp callback.Fingerprint) string {
	return string(fp.Kind) + "|" + fp.ProviderCallID
}

// The scripts. KEYS[1] is always the event key.
const (
	scriptReserve = `
local s = redis.call('HGET', KEYS[1], 's')
if not s then
  redis.call('HSET', KEYS[1], 's', 'i', 't', ARGV[2], 'f', ARGV[1])
  redis.call('PEXPIRE', KEYS[1], ARGV[3])
  return {'reserved'}
end
if redis.call('HGET', KEYS[1], 'f') ~= ARGV[1] then
  return {'mismatch'}
end
if s == 's' then
  return {'duplicate', redis.call('HGET', KEYS[1], 'r')}
end
return {'inflight'}`

	scriptSettle = `
local s = redis.call('HGET', KEYS[1], 's')
if not s then return 'gone' end
if s ~= 'i' or redis.call('HGET', KEYS[1], 't') ~= ARGV[1] then return 'notowner' end
redis.call('HSET', KEYS[1], 's', 's', 'r', ARGV[2])
redis.call('HDEL', KEYS[1], 't')
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return 'ok'`

	scriptRelease = `
local s = redis.call('HGET', KEYS[1], 's')
if not s then return 'gone' end
if s ~= 'i' or redis.call('HGET', KEYS[1], 't') ~= ARGV[1] then return 'notowner' end
redis.call('DEL', KEYS[1])
return 'ok'`

	scriptConflict = `
redis.call('LPUSH', KEYS[1], ARGV[1])
redis.call('LTRIM', KEYS[1], 0, tonumber(ARGV[2]) - 1)
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return 'ok'`
)

func (s *Store) eval(ctx context.Context, script, key string, args ...string) (value, error) {
	cmd := append([]string{"EVAL", script, "1", key}, args...)
	return s.cl.do(ctx, cmd...)
}

// Reserve implements callback.Store.
func (s *Store) Reserve(ctx context.Context, k callback.Key, fp callback.Fingerprint) (callback.Reservation, error) {
	key, err := s.eventKey(k)
	if err != nil {
		return callback.Reservation{}, err
	}
	token, err := callback.NewToken()
	if err != nil {
		return callback.Reservation{}, err
	}
	v, err := s.eval(ctx, scriptReserve, key, fingerprint(fp), token, ms(s.cfg.Lease))
	if err != nil {
		return callback.Reservation{}, unavailable("reserve", err)
	}
	if v.kind != '*' || len(v.arr) == 0 || v.arr[0].kind != '$' {
		return callback.Reservation{}, unavailable("reserve", errProtocol)
	}
	switch v.arr[0].str {
	case "reserved":
		return callback.Reservation{Status: callback.StatusReserved, Token: token}, nil
	case "inflight":
		return callback.Reservation{Status: callback.StatusInFlight}, nil
	case "mismatch":
		return callback.Reservation{Status: callback.StatusMismatch}, nil
	case "duplicate":
		if len(v.arr) != 2 || v.arr[1].kind != '$' || v.arr[1].null {
			return callback.Reservation{}, unavailable("reserve", errProtocol)
		}
		var prior callback.Result
		if err := json.Unmarshal([]byte(v.arr[1].str), &prior); err != nil {
			return callback.Reservation{}, unavailable("reserve", errProtocol)
		}
		return callback.Reservation{Status: callback.StatusDuplicate, Prior: prior}, nil
	default:
		return callback.Reservation{}, unavailable("reserve", errProtocol)
	}
}

func (s *Store) owned(op string, v value, err error) error {
	if err != nil {
		return unavailable(op, err)
	}
	if v.kind != '$' {
		return unavailable(op, errProtocol)
	}
	switch v.str {
	case "ok":
		return nil
	case "notowner":
		return fmt.Errorf("%w: %s refused", callback.ErrNotOwner, op)
	case "gone":
		return fmt.Errorf("%w: %s refused, the reservation expired", callback.ErrNotOwner, op)
	default:
		return unavailable(op, errProtocol)
	}
}

// Settle implements callback.Store.
func (s *Store) Settle(ctx context.Context, k callback.Key, token string, r callback.Result) error {
	key, err := s.eventKey(k)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("%w: empty token", ErrInvalidInput)
	}
	body, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("telephonyredis: encoding result: %w", err)
	}
	v, err := s.eval(ctx, scriptSettle, key, token, string(body), ms(s.cfg.Retention))
	return s.owned("settle", v, err)
}

// Release implements callback.Store.
func (s *Store) Release(ctx context.Context, k callback.Key, token string) error {
	key, err := s.eventKey(k)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("%w: empty token", ErrInvalidInput)
	}
	v, err := s.eval(ctx, scriptRelease, key, token)
	return s.owned("release", v, err)
}

// MarkTerminal implements callback.Store. The mark expires after Retention.
func (s *Store) MarkTerminal(ctx context.Context, id telephony.CallID) error {
	key, err := s.terminalKey(id)
	if err != nil {
		return err
	}
	v, err := s.cl.do(ctx, "SET", key, "1", "PX", ms(s.cfg.Retention))
	if err != nil {
		return unavailable("mark terminal", err)
	}
	if v.kind != '+' || v.str != "OK" {
		return unavailable("mark terminal", errProtocol)
	}
	return nil
}

// Terminal implements callback.Store.
func (s *Store) Terminal(ctx context.Context, id telephony.CallID) (bool, error) {
	key, err := s.terminalKey(id)
	if err != nil {
		return false, err
	}
	v, err := s.cl.do(ctx, "EXISTS", key)
	if err != nil {
		return false, unavailable("terminal", err)
	}
	if v.kind != ':' || v.num < 0 || v.num > 1 {
		return false, unavailable("terminal", errProtocol)
	}
	return v.num == 1, nil
}

// RecordConflict implements callback.Store. The list holds at most
// MaxConflicts entries, newest first, and expires after Retention.
func (s *Store) RecordConflict(ctx context.Context, c callback.Conflict) error {
	body, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("telephonyredis: encoding conflict: %w", err)
	}
	v, err := s.eval(ctx, scriptConflict, s.conflictKey(), string(body),
		strconv.Itoa(s.cfg.MaxConflicts), ms(s.cfg.Retention))
	if err != nil {
		return unavailable("record conflict", err)
	}
	if v.kind != '$' || v.str != "ok" {
		return unavailable("record conflict", errProtocol)
	}
	return nil
}

// Conflicts returns the recorded conflicts, newest first.
func (s *Store) Conflicts(ctx context.Context) ([]callback.Conflict, error) {
	v, err := s.cl.do(ctx, "LRANGE", s.conflictKey(), "0", "-1")
	if err != nil {
		return nil, unavailable("conflicts", err)
	}
	if v.kind != '*' {
		return nil, unavailable("conflicts", errProtocol)
	}
	out := make([]callback.Conflict, 0, len(v.arr))
	for _, item := range v.arr {
		var c callback.Conflict
		if item.kind != '$' || json.Unmarshal([]byte(item.str), &c) != nil {
			return nil, unavailable("conflicts", errProtocol)
		}
		out = append(out, c)
	}
	return out, nil
}
