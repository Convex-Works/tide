package media

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
)

// busTick is how often the bus advances its keys' TTLs. miniredis was made for
// tests and expires keys only when told time has passed, so each tick tells
// it how much has: the wall-clock time since the last one.
const busTick = time.Second

// livekitUnlockScript is the one Lua script anything on the bus runs:
// LiveKit's RedisStore releases a room lock with it (redisstore.go,
// byte for byte; a LiveKit upgrade that changes it fails the bus tests).
const livekitUnlockScript = "if redis.call(\"get\", KEYS[1]) == ARGV[1] then\n" +
	"\t\t\t\t\t\treturn redis.call(\"del\", KEYS[1])\n" +
	"\t\t\t\t\t else return 0\n" +
	"\t\t\t\t\t end"

// busScripts are the scripts the bus runs, by SHA-1. miniredis gives a
// script Lua's dofile, loadfile, require and debug, so any other script from
// a connection that has the password could read tide's files.
var busScripts = map[string]bool{scriptSHA(livekitUnlockScript): true}

func scriptSHA(script string) string {
	sum := sha1.Sum([]byte(script))
	return hex.EncodeToString(sum[:])
}

// refuseUnknownScripts answers EVAL, EVALSHA and SCRIPT LOAD (and their
// read-only forms) with an error unless they name a script in busScripts.
// It runs before every command; returning false lets the command run as
// usual, authentication included.
func refuseUnknownScripts(peer *server.Peer, cmd string, args ...string) bool {
	switch cmd {
	case "EVAL", "EVAL_RO":
		if len(args) > 0 && busScripts[scriptSHA(args[0])] {
			return false
		}
	case "EVALSHA", "EVALSHA_RO":
		if len(args) > 0 && busScripts[strings.ToLower(args[0])] {
			return false
		}
	case "SCRIPT":
		if len(args) == 0 || !strings.EqualFold(args[0], "LOAD") {
			return false
		}
		if len(args) > 1 && busScripts[scriptSHA(args[1])] {
			return false
		}
	default:
		return false
	}
	peer.WriteError("ERR this bus runs only the media server's own scripts")
	return true
}

// Bus is the Redis-protocol endpoint tide serves for the SFU and the
// recorder, in memory. It requires password on every connection, expires
// keys on the wall clock, and runs no Lua script but LiveKit's own.
type Bus struct {
	kv       *miniredis.Miniredis
	password string
	stop     chan struct{}
	ticking  sync.WaitGroup

	clock sync.Mutex
	last  time.Time // when the TTLs were last advanced

	closeOnce sync.Once
}

// StartBus listens on addr (host:port; ":6379" means every interface) and
// serves until Close.
func StartBus(addr, password string) (*Bus, error) {
	if password == "" {
		return nil, errors.New("media: the bus needs a password")
	}
	kv := miniredis.NewMiniRedis()
	kv.RequireAuth(password)
	if err := kv.StartAddr(addr); err != nil {
		return nil, fmt.Errorf("media: bus: %w", err)
	}
	// miniredis makes its server in StartAddr, so the hook goes on as it
	// starts listening; a client would need the password and to beat this
	// line to run a script before it.
	kv.Server().SetPreHook(refuseUnknownScripts)
	b := &Bus{kv: kv, password: password, stop: make(chan struct{}), last: time.Now()}
	b.ticking.Add(1)
	go b.tick()
	return b, nil
}

// tick advances every TTL each busTick by the time that has passed, so a
// key set to expire in 1.5s is gone between one and two ticks later, as on a
// real Redis give or take a second.
func (b *Bus) tick() {
	defer b.ticking.Done()
	ticker := time.NewTicker(busTick)
	defer ticker.Stop()
	for {
		select {
		case <-b.stop:
			return
		case <-ticker.C:
			b.advance(time.Now())
		}
	}
}

// advance moves every TTL on by the wall-clock time since the last advance.
// A ticker drops the ticks a stalled process misses (a long GC, CPU
// throttling, a paused VM), so counting ticks would stretch every TTL by
// the stall; measuring the clock doesn't.
func (b *Bus) advance(now time.Time) {
	b.clock.Lock()
	defer b.clock.Unlock()
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.kv.FastForward(elapsed)
		b.last = now
	}
}

// Addr is the address the bus listens on, with the port it was given (or
// chose, for port 0).
func (b *Bus) Addr() string { return b.kv.Addr() }

// Password is the password every connection must AUTH with.
func (b *Bus) Password() string { return b.password }

// dialAddr is the address the media server, in this process, connects to:
// Addr, with an unspecified host (every interface) replaced by 127.0.0.1.
func (b *Bus) dialAddr() string {
	host, port, err := net.SplitHostPort(b.Addr())
	if err != nil {
		return b.Addr()
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// Close stops serving and drops every key.
func (b *Bus) Close() error {
	b.closeOnce.Do(func() {
		close(b.stop)
		b.ticking.Wait()
		b.kv.Close()
		b.kv.FlushAll()
	})
	return nil
}
