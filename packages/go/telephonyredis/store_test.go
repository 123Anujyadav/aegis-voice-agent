package telephonyredis

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/telephony"
	"github.com/callscreen/callscreen-platform/packages/go/telephony/callback"
)

// Unit tests over the scripted fake server. They prove command construction,
// reply interpretation, error classification, timeouts and cancellation. They
// do NOT prove atomicity, leases or ownership against Redis — see
// redis_integration_test.go.

const secret = "s3cr3t-redis-password"

func clientCfg(addr string) ClientConfig {
	return ClientConfig{Addr: addr, DialTimeout: 2 * time.Second, OpTimeout: 2 * time.Second, PoolSize: 4}
}

func storeCfg() Config {
	return Config{KeyPrefix: "test:cb:", Lease: 30 * time.Second, Retention: time.Hour, MaxConflicts: 10}
}

func newTestStore(t *testing.T, f *fakeServer, mutate ...func(*ClientConfig)) *Store {
	t.Helper()
	cc := clientCfg(f.addr())
	for _, m := range mutate {
		m(&cc)
	}
	s, err := New(cc, storeCfg())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

var (
	key = callback.Key{Provider: "fake-carrier", EventID: "evt-1"}
	fp  = callback.Fingerprint{ProviderCallID: "pc-1", Kind: callback.KindRinging}
)

func noSecret(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), secret) {
		t.Errorf("error discloses the credential: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

func TestNew_RequiresEveryValueAndHasNoDefaults(t *testing.T) {
	t.Parallel()
	good := clientCfg("127.0.0.1:6379")
	for name, tc := range map[string]struct {
		cc  ClientConfig
		cfg Config
	}{
		"zero everything":       {ClientConfig{}, Config{}},
		"no address":            {ClientConfig{DialTimeout: 1, OpTimeout: 1, PoolSize: 1}, storeCfg()},
		"username w/o password": {func() ClientConfig { c := good; c.Username = "u"; return c }(), storeCfg()},
		"no op timeout":         {func() ClientConfig { c := good; c.OpTimeout = 0; return c }(), storeCfg()},
		"no pool":               {func() ClientConfig { c := good; c.PoolSize = 0; return c }(), storeCfg()},
		"no lease":              {good, func() Config { c := storeCfg(); c.Lease = 0; return c }()},
		"retention below lease": {good, func() Config { c := storeCfg(); c.Retention = c.Lease / 2; return c }()},
		"no max conflicts":      {good, func() Config { c := storeCfg(); c.MaxConflicts = 0; return c }()},
		"prefix without colon":  {good, func() Config { c := storeCfg(); c.KeyPrefix = "test"; return c }()},
		"prefix with a space":   {good, func() Config { c := storeCfg(); c.KeyPrefix = "te st:"; return c }()},
	} {
		if _, err := New(tc.cc, tc.cfg); err == nil {
			t.Errorf("%s: New succeeded", name)
		}
	}
	if _, err := New(good, storeCfg()); err != nil {
		t.Errorf("valid configuration refused: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Reserve
// ---------------------------------------------------------------------------

func TestReserve_SendsOneAtomicScriptWithTheLease(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, func([]string) string { return array(bulk("reserved")) })
	s := newTestStore(t, f)

	res, err := s.Reserve(context.Background(), key, fp)
	if err != nil || res.Status != callback.StatusReserved || len(res.Token) != 32 {
		t.Fatalf("Reserve = %+v, %v", res, err)
	}
	cmds := f.seen()
	if len(cmds) != 1 {
		t.Fatalf("%d commands, want 1", len(cmds))
	}
	c := cmds[0]
	want := []string{"EVAL", scriptReserve, "1", "test:cb:evt:fake-carrier:evt-1", "ringing|pc-1", res.Token, "30000"}
	if strings.Join(c, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("command = %q\nwant      %q", c, want)
	}
}

func TestReserve_InterpretsEveryReply(t *testing.T) {
	t.Parallel()
	prior, _ := json.Marshal(callback.Result{Outcome: callback.OutcomeApplied, Call: "call_x"})
	for _, tc := range []struct {
		name   string
		reply  string
		status callback.ReserveStatus
		err    error
	}{
		{"in flight", array(bulk("inflight")), callback.StatusInFlight, nil},
		{"mismatch", array(bulk("mismatch")), callback.StatusMismatch, nil},
		{"duplicate", array(bulk("duplicate"), bulk(string(prior))), callback.StatusDuplicate, nil},
		{"duplicate without a result", array(bulk("duplicate")), 0, ErrUnavailable},
		{"duplicate with a corrupt result", array(bulk("duplicate"), bulk("{")), 0, ErrUnavailable},
		{"unknown word", array(bulk("maybe")), 0, ErrUnavailable},
		{"not an array", bulk("reserved"), 0, ErrUnavailable},
		{"server error", "-ERR something with " + secret + "\r\n", 0, ErrUnavailable},
		{"garbage", "?what\r\n", 0, ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer(t, func([]string) string { return tc.reply })
			res, err := newTestStore(t, f).Reserve(context.Background(), key, fp)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("Reserve = %+v, %v; want %v", res, err, tc.err)
				}
				if res.Status == callback.StatusReserved {
					t.Error("a failed reserve reported a reservation")
				}
				noSecret(t, err)
				return
			}
			if err != nil || res.Status != tc.status || res.Token != "" {
				t.Fatalf("Reserve = %+v, %v; want status %d without a token", res, err, tc.status)
			}
			if tc.status == callback.StatusDuplicate && (res.Prior.Outcome != callback.OutcomeApplied || res.Prior.Call != "call_x") {
				t.Errorf("prior = %+v", res.Prior)
			}
		})
	}
}

func TestReserve_RefusesInvalidKeysWithoutTouchingRedis(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, func([]string) string { return array(bulk("reserved")) })
	s := newTestStore(t, f)
	for name, k := range map[string]callback.Key{
		"no provider":      {EventID: "e"},
		"bad provider":     {Provider: "Bad Carrier", EventID: "e"},
		"no event ID":      {Provider: "fake-carrier"},
		"event ID w/space": {Provider: "fake-carrier", EventID: "e 1"},
		"event ID w/brace": {Provider: "fake-carrier", EventID: "e{1}"},
		"long event ID":    {Provider: "fake-carrier", EventID: strings.Repeat("e", 129)},
	} {
		if _, err := s.Reserve(context.Background(), k, fp); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
	if n := len(f.seen()); n != 0 {
		t.Errorf("%d commands sent for invalid input", n)
	}
}

// ---------------------------------------------------------------------------
// Outages, timeouts, cancellation
// ---------------------------------------------------------------------------

func TestOutage_IsNeverANewEvent(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, func([]string) string { return array(bulk("reserved")) })
	addr := f.addr()
	_ = f.ln.Close() // nothing listens any more

	s, err := New(clientCfg(addr), storeCfg())
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Reserve(context.Background(), key, fp)
	if !errors.Is(err, ErrUnavailable) || res.Status == callback.StatusReserved {
		t.Fatalf("Reserve during an outage = %+v, %v; want ErrUnavailable", res, err)
	}
	if _, err := s.Terminal(context.Background(), "call_x"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Terminal during an outage = %v; must not report 'not terminal'", err)
	}
	if strings.Contains(err.Error(), addr) {
		t.Errorf("error discloses the address: %v", err)
	}
}

func TestTimeout_BoundsAnUnansweredCommand(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, func([]string) string { return replyHang })
	s := newTestStore(t, f, func(c *ClientConfig) { c.OpTimeout = 50 * time.Millisecond })

	start := time.Now()
	_, err := s.Reserve(context.Background(), key, fp)
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Reserve = %v, want ErrUnavailable wrapping DeadlineExceeded", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %s; OpTimeout did not bound it", d)
	}
}

func TestCancellation_InterruptsACommandInFlight(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, func([]string) string { return replyHang })
	s := newTestStore(t, f)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	start := time.Now()
	_, err := s.Reserve(ctx, key, fp)
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Reserve = %v, want ErrUnavailable wrapping Canceled", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("cancellation took %s", d)
	}

	done, stop := context.WithCancel(context.Background())
	stop()
	if _, err := s.Terminal(done, "call_x"); !errors.Is(err, context.Canceled) {
		t.Errorf("Terminal with a cancelled context = %v", err)
	}
}

func TestBrokenConnection_IsReplacedNotReused(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	f := newFakeServer(t, func([]string) string {
		if calls.Add(1) == 1 {
			return replyClose
		}
		return ":0\r\n"
	})
	s := newTestStore(t, f)
	if _, err := s.Terminal(context.Background(), "call_x"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("first Terminal = %v, want ErrUnavailable", err)
	}
	if ok, err := s.Terminal(context.Background(), "call_x"); err != nil || ok {
		t.Fatalf("second Terminal = %v, %v", ok, err)
	}
	if n := f.connections(); n != 2 {
		t.Errorf("%d connections, want 2 (the broken one replaced)", n)
	}
}

func TestAuth_SentOnConnectAndNeverDisclosed(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, func(args []string) string {
		if args[0] == "AUTH" {
			if len(args) == 3 && args[1] == "app" && args[2] == secret {
				return "+OK\r\n"
			}
			return "-WRONGPASS invalid username-password pair " + secret + "\r\n"
		}
		return ":1\r\n"
	})
	ok := newTestStore(t, f, func(c *ClientConfig) { c.Username, c.Password = "app", secret })
	if term, err := ok.Terminal(context.Background(), "call_x"); err != nil || !term {
		t.Fatalf("Terminal = %v, %v", term, err)
	}
	if first := f.seen()[0]; first[0] != "AUTH" {
		t.Errorf("first command = %q, want AUTH", first[0])
	}

	bad := newTestStore(t, f, func(c *ClientConfig) { c.Username, c.Password = "app", "wrong-"+secret })
	_, err := bad.Terminal(context.Background(), "call_x")
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, errAuth) {
		t.Fatalf("Terminal with a bad password = %v", err)
	}
	noSecret(t, err)
}

// ---------------------------------------------------------------------------
// Settle and Release: ownership
// ---------------------------------------------------------------------------

func TestSettleAndRelease_MapOwnership(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reply string
		want  error
	}{
		{bulk("ok"), nil},
		{bulk("notowner"), callback.ErrNotOwner},
		{bulk("gone"), callback.ErrNotOwner},
		{bulk("weird"), ErrUnavailable},
		{":1\r\n", ErrUnavailable},
		{"-ERR boom\r\n", ErrUnavailable},
	} {
		f := newFakeServer(t, func([]string) string { return tc.reply })
		s := newTestStore(t, f)
		r := callback.Result{Outcome: callback.OutcomeApplied, Call: "call_x", To: telephony.StateRinging}
		settleErr := s.Settle(context.Background(), key, "tok", r)
		releaseErr := s.Release(context.Background(), key, "tok")
		for name, err := range map[string]error{"settle": settleErr, "release": releaseErr} {
			if (tc.want == nil) != (err == nil) || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Errorf("%s with reply %q = %v, want %v", name, tc.reply, err, tc.want)
			}
		}
		cmds := f.seen()
		settle, release := cmds[0], cmds[1]
		if settle[0] != "EVAL" || settle[1] != scriptSettle || settle[4] != "tok" || settle[6] != "3600000" {
			t.Errorf("settle command = %q", settle)
		}
		var decoded callback.Result
		if err := json.Unmarshal([]byte(settle[5]), &decoded); err != nil || decoded != r {
			t.Errorf("settled result = %+v, %v", decoded, err)
		}
		if release[1] != scriptRelease || release[4] != "tok" {
			t.Errorf("release command = %q", release)
		}
	}
}

func TestSettleAndRelease_RefuseAnEmptyToken(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, func([]string) string { return bulk("ok") })
	s := newTestStore(t, f)
	if err := s.Settle(context.Background(), key, "", callback.Result{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("Settle = %v", err)
	}
	if err := s.Release(context.Background(), key, ""); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("Release = %v", err)
	}
	if len(f.seen()) != 0 {
		t.Error("commands sent with an empty token")
	}
}

// ---------------------------------------------------------------------------
// Terminal marks, conflicts and retention
// ---------------------------------------------------------------------------

func TestRetention_IsAppliedToEveryStoredRecord(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t, func(args []string) string {
		switch args[0] {
		case "SET":
			return "+OK\r\n"
		case "EXISTS":
			return ":1\r\n"
		case "LRANGE":
			return array(bulk(`{"Reason":"r1"}`))
		default:
			return bulk("ok")
		}
	})
	s := newTestStore(t, f)
	ctx := context.Background()
	if err := s.MarkTerminal(ctx, "call_x"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Terminal(ctx, "call_x"); err != nil || !ok {
		t.Fatalf("Terminal = %v, %v", ok, err)
	}
	if err := s.RecordConflict(ctx, callback.Conflict{Reason: "r1"}); err != nil {
		t.Fatal(err)
	}
	cs, err := s.Conflicts(ctx)
	if err != nil || len(cs) != 1 || cs[0].Reason != "r1" {
		t.Fatalf("Conflicts = %+v, %v", cs, err)
	}

	cmds := f.seen()
	if want := []string{"SET", "test:cb:term:call_x", "1", "PX", "3600000"}; strings.Join(cmds[0], " ") != strings.Join(want, " ") {
		t.Errorf("mark = %q, want %q", cmds[0], want)
	}
	if c := cmds[2]; c[1] != scriptConflict || c[3] != "test:cb:conflicts" || c[5] != "10" || c[6] != "3600000" {
		t.Errorf("conflict = %q", c)
	}
	if _, err := s.Terminal(ctx, "not-a-call"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("invalid call ID = %v", err)
	}
}

// ---------------------------------------------------------------------------
// The RESP parser on hostile input
// ---------------------------------------------------------------------------

func TestParser_RefusesOversizedAndMalformedReplies(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"bulk too large":    "$" + itoa(maxBulkBytes+1) + "\r\n",
		"negative bulk":     "$-2\r\n",
		"array too large":   "*" + itoa(maxArrayItems+1) + "\r\n",
		"bad integer":       ":12x\r\n",
		"unknown type":      "!oops\r\n",
		"unterminated":      "$3\r\nabcXY",
		"empty line":        "\r\n",
		"too deep":          "*1\r\n*1\r\n*1\r\n*1\r\n*1\r\n*1\r\n:1\r\n",
		"line too long":     "+" + strings.Repeat("a", maxLineBytes+1) + "\r\n",
		"truncated array":   "*2\r\n:1\r\n",
		"truncated bulk":    "$10\r\nabc",
		"bulk without CRLF": "$3\r\nabc\n\n",
	} {
		_, err := readValue(bufio.NewReader(strings.NewReader(raw)), 0)
		if err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	v, err := readValue(bufio.NewReader(strings.NewReader("*3\r\n+OK\r\n:-7\r\n$-1\r\n")), 0)
	if err != nil || len(v.arr) != 3 || v.arr[0].str != "OK" || v.arr[1].num != -7 || !v.arr[2].null {
		t.Errorf("valid reply = %+v, %v", v, err)
	}
}

// TestClusterRedirect_FailsClosed documents a limitation: the RESP client does
// not follow Redis Cluster MOVED/ASK redirections. A redirect is an error
// reply, so every operation fails as ErrUnavailable — never as "new event",
// "not terminal" or success — and the redirect target is not disclosed.
func TestClusterRedirect_FailsClosed(t *testing.T) {
	t.Parallel()
	for _, reply := range []string{
		"-MOVED 3999 10.9.8.7:6380\r\n",
		"-ASK 3999 10.9.8.7:6380\r\n",
	} {
		f := newFakeServer(t, func([]string) string { return reply })
		s := newTestStore(t, f)
		res, err := s.Reserve(context.Background(), key, fp)
		if !errors.Is(err, ErrUnavailable) || res.Status == callback.StatusReserved {
			t.Errorf("Reserve on %q = %+v, %v; want ErrUnavailable", reply, res, err)
		}
		if _, err := s.Terminal(context.Background(), "call_x"); !errors.Is(err, ErrUnavailable) {
			t.Errorf("Terminal on %q = %v; want ErrUnavailable", reply, err)
		}
		if err != nil && strings.Contains(err.Error(), "10.9.8.7") {
			t.Errorf("error discloses the redirect target: %v", err)
		}
	}
}
