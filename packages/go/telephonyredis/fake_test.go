package telephonyredis

import (
	"bufio"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeServer speaks RESP2 framing over loopback with SCRIPTED replies. It is
// not Redis: it does not run Lua or store keys. It proves what this package
// SENDS and how it INTERPRETS replies — never Redis semantics, which only the
// real-Redis suite can prove.

const (
	replyHang  = "\x00hang"  // read the command, never answer
	replyClose = "\x00close" // read the command, close the connection
)

type fakeServer struct {
	t       *testing.T
	ln      net.Listener
	handler func(args []string) string

	mu       sync.Mutex
	commands [][]string
	conns    int
	wg       sync.WaitGroup
}

func newFakeServer(t *testing.T, handler func(args []string) string) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeServer{t: t, ln: ln, handler: handler}
	f.wg.Add(1)
	go f.serve()
	t.Cleanup(func() {
		_ = ln.Close()
		f.wg.Wait()
	})
	return f
}

func (f *fakeServer) addr() string { return f.ln.Addr().String() }

func (f *fakeServer) serve() {
	defer f.wg.Done()
	for {
		nc, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns++
		f.mu.Unlock()
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			defer func() { _ = nc.Close() }()
			_ = nc.SetDeadline(time.Now().Add(30 * time.Second))
			r := bufio.NewReader(nc)
			for {
				v, err := readValue(r, 0)
				if err != nil {
					return
				}
				args := make([]string, 0, len(v.arr))
				for _, a := range v.arr {
					args = append(args, a.str)
				}
				f.mu.Lock()
				f.commands = append(f.commands, args)
				f.mu.Unlock()
				switch reply := f.handler(args); reply {
				case replyHang:
					time.Sleep(2 * time.Second)
					return
				case replyClose:
					return
				default:
					if _, err := nc.Write([]byte(reply)); err != nil {
						return
					}
				}
			}
		}()
	}
}

func (f *fakeServer) seen() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.commands...)
}

func (f *fakeServer) connections() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns
}

func bulk(s string) string { return "$" + itoa(len(s)) + "\r\n" + s + "\r\n" }

func array(items ...string) string {
	out := "*" + itoa(len(items)) + "\r\n"
	for _, it := range items {
		out += it
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
