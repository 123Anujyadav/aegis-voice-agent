package telephonyredis

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// A minimal RESP2 client: exactly what [Store] needs (EVAL, SET, EXISTS,
// LRANGE, PING and AUTH), and nothing else.
//
// # Why this exists
//
// No Redis client module is present in the workspace or the module cache, and
// this programme runs with GOPROXY=off. Rather than fetch an unvetted
// dependency, the adapter speaks the protocol directly. RESP2 is a small,
// stable, documented framing; the risk lives in the parser, which is bounded
// on every dimension below and tested against malformed input.
//
// # What it does not do
//
// No pipelining, pub/sub, cluster redirection (MOVED/ASK), RESP3 or retries. A
// failed command is reported; the caller decides. Cluster mode would need
// redirection support: every key here is a single key per command, so the
// scripts are cluster-safe, but this client is not cluster-aware.

// Parser bounds. A reply larger than these is treated as a protocol failure.
const (
	maxLineBytes  = 64 << 10
	maxBulkBytes  = 1 << 20
	maxArrayItems = 4096
)

var (
	errProtocol = errors.New("redis protocol error")
	errAuth     = errors.New("redis authentication failed")
)

// ClientConfig configures the connection. Credentials come from the
// deployment's secret mechanism; they are never logged or returned in errors.
type ClientConfig struct {
	// Addr is host:port. Required.
	Addr string
	// Username and Password, if set, are sent with AUTH on every new
	// connection. Username requires Password.
	Username string
	Password string
	// TLS, if set, wraps every connection.
	TLS *tls.Config
	// DialTimeout bounds connection setup. Required.
	DialTimeout time.Duration
	// OpTimeout bounds every command, on top of the caller's context.
	// Required.
	OpTimeout time.Duration
	// PoolSize is the maximum number of concurrent connections. Required.
	PoolSize int
}

func (c ClientConfig) validate() []string {
	var p []string
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		p = append(p, "Addr must be host:port")
	}
	if c.Username != "" && c.Password == "" {
		p = append(p, "Username requires Password")
	}
	if c.DialTimeout <= 0 {
		p = append(p, "DialTimeout must be positive")
	}
	if c.OpTimeout <= 0 {
		p = append(p, "OpTimeout must be positive")
	}
	if c.PoolSize <= 0 {
		p = append(p, "PoolSize must be positive")
	}
	return p
}

// value is one RESP2 reply.
type value struct {
	kind byte // '+', '-', ':', '$', '*'
	str  string
	num  int64
	arr  []value
	null bool
}

// serverError is a Redis error reply, reduced to its leading code (ERR,
// NOSCRIPT, WRONGPASS, ...). The rest of the message is withheld: script
// errors can echo arguments.
type serverError struct{ code string }

func (e *serverError) Error() string { return "redis error reply " + e.code }

type conn struct {
	nc net.Conn
	r  *bufio.Reader
	w  *bufio.Writer
}

type client struct {
	cfg   ClientConfig
	slots chan struct{} // bounds concurrent connections
	idle  chan *conn
}

func newClient(cfg ClientConfig) *client {
	return &client{
		cfg:   cfg,
		slots: make(chan struct{}, cfg.PoolSize),
		idle:  make(chan *conn, cfg.PoolSize),
	}
}

// do runs one command and returns its reply. A Redis error reply is returned as
// a *serverError with the connection kept; any other failure closes the
// connection.
func (cl *client) do(ctx context.Context, args ...string) (value, error) {
	if err := ctx.Err(); err != nil {
		return value{}, err
	}
	select {
	case cl.slots <- struct{}{}:
	case <-ctx.Done():
		return value{}, ctx.Err()
	}
	defer func() { <-cl.slots }()

	c, err := cl.get(ctx)
	if err != nil {
		return value{}, err
	}
	v, err := cl.roundTrip(ctx, c, args)
	var se *serverError
	if err != nil && !errors.As(err, &se) {
		_ = c.nc.Close()
		return value{}, err
	}
	cl.put(c)
	return v, err
}

func (cl *client) get(ctx context.Context) (*conn, error) {
	select {
	case c := <-cl.idle:
		return c, nil
	default:
	}
	return cl.dial(ctx)
}

func (cl *client) put(c *conn) {
	select {
	case cl.idle <- c:
	default:
		_ = c.nc.Close()
	}
}

func (cl *client) dial(ctx context.Context) (*conn, error) {
	dctx, cancel := context.WithTimeout(ctx, cl.cfg.DialTimeout)
	defer cancel()
	var d net.Dialer
	nc, err := d.DialContext(dctx, "tcp", cl.cfg.Addr)
	if err != nil {
		return nil, describeNetErr("dial", err)
	}
	if cl.cfg.TLS != nil {
		tc := tls.Client(nc, cl.cfg.TLS.Clone())
		if err := tc.HandshakeContext(dctx); err != nil {
			_ = nc.Close()
			return nil, describeNetErr("tls handshake", err)
		}
		nc = tc
	}
	c := &conn{nc: nc, r: bufio.NewReader(nc), w: bufio.NewWriter(nc)}
	if cl.cfg.Password != "" {
		args := []string{"AUTH", cl.cfg.Password}
		if cl.cfg.Username != "" {
			args = []string{"AUTH", cl.cfg.Username, cl.cfg.Password}
		}
		v, err := cl.roundTrip(ctx, c, args)
		if err != nil || v.kind != '+' {
			_ = nc.Close()
			// Never wrap the reply or the arguments: they carry the secret.
			return nil, errAuth
		}
	}
	return c, nil
}

func (cl *client) roundTrip(ctx context.Context, c *conn, args []string) (value, error) {
	deadline := time.Now().Add(cl.cfg.OpTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.nc.SetDeadline(deadline); err != nil {
		return value{}, describeNetErr("set deadline", err)
	}
	// Cancellation interrupts a blocked read or write immediately.
	stop := context.AfterFunc(ctx, func() { _ = c.nc.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	if err := writeCommand(c.w, args); err != nil {
		return value{}, cl.ioErr(ctx, "write", err)
	}
	v, err := readValue(c.r, 0)
	if err != nil {
		return value{}, cl.ioErr(ctx, "read", err)
	}
	if v.kind == '-' {
		return v, &serverError{code: v.str}
	}
	return v, nil
}

func (cl *client) ioErr(ctx context.Context, op string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, errProtocol) {
		return err
	}
	return describeNetErr(op, err)
}

// close closes idle connections. In-flight commands finish on their own.
func (cl *client) close() {
	for {
		select {
		case c := <-cl.idle:
			_ = c.nc.Close()
		default:
			return
		}
	}
}

// describeNetErr keeps the kind of failure and drops addresses.
func describeNetErr(op string, err error) error {
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return fmt.Errorf("redis %s: %w", op, context.DeadlineExceeded)
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return fmt.Errorf("redis %s: connection closed", op)
	default:
		return fmt.Errorf("redis %s failed (details withheld)", op)
	}
}

func writeCommand(w *bufio.Writer, args []string) error {
	if _, err := w.WriteString("*" + strconv.Itoa(len(args)) + "\r\n"); err != nil {
		return err
	}
	for _, a := range args {
		if _, err := w.WriteString("$" + strconv.Itoa(len(a)) + "\r\n" + a + "\r\n"); err != nil {
			return err
		}
	}
	return w.Flush()
}

func readLine(r *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		frag, isPrefix, err := r.ReadLine()
		if err != nil {
			return "", err
		}
		if b.Len()+len(frag) > maxLineBytes {
			return "", fmt.Errorf("%w: line too long", errProtocol)
		}
		b.Write(frag)
		if !isPrefix {
			return b.String(), nil
		}
	}
}

func readValue(r *bufio.Reader, depth int) (value, error) {
	if depth > 4 {
		return value{}, fmt.Errorf("%w: nesting too deep", errProtocol)
	}
	line, err := readLine(r)
	if err != nil {
		return value{}, err
	}
	if line == "" {
		return value{}, fmt.Errorf("%w: empty line", errProtocol)
	}
	kind, rest := line[0], line[1:]
	switch kind {
	case '+':
		return value{kind: '+', str: rest}, nil
	case '-':
		code, _, _ := strings.Cut(rest, " ")
		return value{kind: '-', str: code}, nil
	case ':':
		n, err := strconv.ParseInt(rest, 10, 64)
		if err != nil {
			return value{}, fmt.Errorf("%w: bad integer", errProtocol)
		}
		return value{kind: ':', num: n}, nil
	case '$':
		n, err := strconv.Atoi(rest)
		if err != nil || n < -1 || n > maxBulkBytes {
			return value{}, fmt.Errorf("%w: bad bulk length", errProtocol)
		}
		if n == -1 {
			return value{kind: '$', null: true}, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return value{}, err
		}
		if buf[n] != '\r' || buf[n+1] != '\n' {
			return value{}, fmt.Errorf("%w: bulk not terminated", errProtocol)
		}
		return value{kind: '$', str: string(buf[:n])}, nil
	case '*':
		n, err := strconv.Atoi(rest)
		if err != nil || n < -1 || n > maxArrayItems {
			return value{}, fmt.Errorf("%w: bad array length", errProtocol)
		}
		if n == -1 {
			return value{kind: '*', null: true}, nil
		}
		arr := make([]value, 0, n)
		for i := 0; i < n; i++ {
			v, err := readValue(r, depth+1)
			if err != nil {
				return value{}, err
			}
			arr = append(arr, v)
		}
		return value{kind: '*', arr: arr}, nil
	default:
		return value{}, fmt.Errorf("%w: unknown reply type", errProtocol)
	}
}
