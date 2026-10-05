package media

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/redis/go-redis/v9"
)

const testBusPassword = "bus-password-0123456789-0123456789"

func startBus(t *testing.T, addr string) *Bus {
	t.Helper()
	bus, err := StartBus(addr, testBusPassword)
	if err != nil {
		t.Fatalf("start the bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

func busClient(t *testing.T, bus *Bus, password string) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{
		Addr: bus.dialAddr(), Password: password, MaxRetries: -1,
		// A refused AUTH must stay refused, not fall back to RESP2.
		Protocol: 2,
	})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestBusRequiresThePassword(t *testing.T) {
	bus := startBus(t, "127.0.0.1:0")
	ctx := context.Background()
	if bus.Password() != testBusPassword {
		t.Fatalf("Password() = %q", bus.Password())
	}

	err := busClient(t, bus, "").Set(ctx, "k", "v", 0).Err()
	if err == nil || !strings.Contains(err.Error(), "NOAUTH") {
		t.Fatalf("SET without AUTH: err = %v, want NOAUTH", err)
	}
	err = busClient(t, bus, "wrong-password").Set(ctx, "k", "v", 0).Err()
	if err == nil || !strings.Contains(strings.ToUpper(err.Error()), "WRONGPASS") {
		t.Fatalf("SET with a wrong password: err = %v, want WRONGPASS", err)
	}
	if err := busClient(t, bus, testBusPassword).Set(ctx, "k", "v", 0).Err(); err != nil {
		t.Fatalf("SET with the password: %v", err)
	}
}

func TestBusRefusesNoPassword(t *testing.T) {
	if bus, err := StartBus("127.0.0.1:0", ""); err == nil {
		_ = bus.Close()
		t.Fatal("a bus without a password started")
	}
}

func TestBusExpiresKeysOnTheWallClock(t *testing.T) {
	bus := startBus(t, "127.0.0.1:0")
	client := busClient(t, bus, testBusPassword)
	ctx := context.Background()

	// psrpc claims messages with SET NX PX; its locks must go away on time.
	set, err := client.SetNX(ctx, "lock", "1", 1500*time.Millisecond).Result()
	if err != nil || !set {
		t.Fatalf("SET NX PX = %v, %v", set, err)
	}
	if again, _ := client.SetNX(ctx, "lock", "2", time.Minute).Result(); again {
		t.Fatal("SET NX took a held lock")
	}
	start := time.Now()
	eventually(t, 5*time.Second, "the lock to expire", func() bool {
		return client.Exists(ctx, "lock").Val() == 0
	})
	if waited := time.Since(start); waited < 500*time.Millisecond {
		t.Fatalf("the 1.5s lock expired after %s", waited)
	}
	// A key without a TTL stays.
	if err := client.Set(ctx, "node", "up", 0).Err(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * busTick)
	if client.Exists(ctx, "node").Val() != 1 {
		t.Fatal("a key without a TTL expired")
	}
}

func TestBusCarriesPubSub(t *testing.T) {
	bus := startBus(t, "127.0.0.1:0")
	ctx := context.Background()
	subscriber := busClient(t, bus, testBusPassword).Subscribe(ctx, "egress")
	t.Cleanup(func() { _ = subscriber.Close() })
	if _, err := subscriber.Receive(ctx); err != nil { // the subscription confirmation
		t.Fatal(err)
	}
	if err := busClient(t, bus, testBusPassword).Publish(ctx, "egress", "start").Err(); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-subscriber.Channel():
		if message.Payload != "start" {
			t.Fatalf("payload %q", message.Payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the published message didn't arrive")
	}
}

func TestMediaServerKeepsItsStateOnTheBus(t *testing.T) {
	// Every interface: the media server must still reach it over loopback.
	bus := startBus(t, ":0")
	if host := bus.dialAddr(); !strings.HasPrefix(host, "127.0.0.1:") {
		t.Fatalf("dialAddr() = %q for a bus on every interface", host)
	}
	opts := testOptions(t)
	opts.Bus = bus
	s := startServer(t, opts)

	ctx := context.Background()
	rooms := lksdk.NewRoomServiceClient(s.URL(), testKey, testSecret)
	if _, err := rooms.CreateRoom(ctx, &livekit.CreateRoomRequest{Name: "on-the-bus"}); err != nil {
		t.Fatalf("create room: %v", err)
	}
	client := busClient(t, bus, testBusPassword)
	// Creating a room takes LiveKit's room lock and releases it with its Lua
	// script, which the bus must still run.
	if n := client.Exists(ctx, "room_lock:on-the-bus").Val(); n != 0 {
		t.Fatal("the room lock outlived the room's creation: the bus refused LiveKit's unlock script")
	}
	if !client.HExists(ctx, "rooms", "on-the-bus").Val() {
		keys, _ := client.Keys(ctx, "*").Result()
		t.Fatalf("the room isn't on the bus; keys: %v", keys)
	}
	if n := client.HLen(ctx, "nodes").Val(); n != 1 {
		t.Fatalf("%d nodes registered on the bus, want 1", n)
	}
}

func TestBusRunsOnlyTheMediaServersScript(t *testing.T) {
	bus := startBus(t, "127.0.0.1:0")
	client := busClient(t, bus, testBusPassword)
	ctx := context.Background()

	for _, script := range []string{
		"return 1",
		"return dofile('/etc/hosts')",
		"return loadfile('/etc/hosts')",
	} {
		if err := client.Eval(ctx, script, nil).Err(); err == nil || !strings.Contains(err.Error(), "only the media server's own scripts") {
			t.Errorf("EVAL %q: err = %v, want refused", script, err)
		}
		if err := client.ScriptLoad(ctx, script).Err(); err == nil {
			t.Errorf("SCRIPT LOAD %q succeeded", script)
		}
	}

	// LiveKit's unlock script runs, by body and by hash.
	unlock := redis.NewScript(livekitUnlockScript)
	if err := client.Set(ctx, "room_lock:r", "token", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if got, err := unlock.Run(ctx, client, []string{"room_lock:r"}, "other").Int(); err != nil || got != 0 {
		t.Fatalf("unlock with the wrong token = %d, %v; want 0", got, err)
	}
	if got, err := unlock.Run(ctx, client, []string{"room_lock:r"}, "token").Int(); err != nil || got != 1 {
		t.Fatalf("unlock with the token = %d, %v; want 1", got, err)
	}
	if client.Exists(ctx, "room_lock:r").Val() != 0 {
		t.Fatal("the unlock script left the lock")
	}
	if err := client.ScriptLoad(ctx, livekitUnlockScript).Err(); err != nil {
		t.Fatalf("SCRIPT LOAD of the unlock script: %v", err)
	}
	// Unknown hashes are refused before the script cache is asked.
	if err := client.EvalSha(ctx, scriptSHA("return 1"), nil).Err(); err == nil || !strings.Contains(err.Error(), "only the media server's own scripts") {
		t.Errorf("EVALSHA of an unknown script: err = %v, want refused", err)
	}
	// The allowlist doesn't let a script past authentication.
	if err := busClient(t, bus, "").Eval(ctx, livekitUnlockScript, []string{"k"}, "v").Err(); err == nil || !strings.Contains(err.Error(), "NOAUTH") {
		t.Errorf("EVAL of the unlock script without AUTH: err = %v, want NOAUTH", err)
	}
}

func TestBusCloseIsIdempotent(t *testing.T) {
	bus, err := StartBus("127.0.0.1:0", testBusPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bus.Close(); err != nil {
		t.Fatal(err)
	}
}
