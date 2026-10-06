package media

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// testRedisPassword is the test Redis's requirepass.
const testRedisPassword = "redis-password-0123456789-0123456789"

// startRedis starts a stand-in for the Redis a recording deployment runs
// beside the recorder. miniredis was made for tests, and these need only
// what LiveKit asks of Redis; it is never part of tide itself.
func startRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	kv := miniredis.NewMiniRedis()
	kv.RequireAuth(testRedisPassword)
	if err := kv.StartAddr("127.0.0.1:0"); err != nil {
		t.Fatalf("start the test Redis: %v", err)
	}
	t.Cleanup(kv.Close)
	return kv
}

// withRedis points opts at kv.
func withRedis(opts Options, kv *miniredis.Miniredis) Options {
	opts.RedisAddr = kv.Addr()
	opts.RedisPassword = testRedisPassword
	return opts
}

func redisClient(t *testing.T, kv *miniredis.Miniredis) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: kv.Addr(), Password: testRedisPassword})
	t.Cleanup(func() { _ = client.Close() })
	return client
}
