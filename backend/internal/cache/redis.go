package cache

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
)

var Client *redis.Client
var Ctx = context.Background()

// Init initializes the Redis client and verifies the connection.
func Init() {

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}

	Client = redis.NewClient(&redis.Options{
		Addr: addr,

		PoolSize:     20,
		MinIdleConns: 5,
		MaxRetries:   3,

		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,

		PoolTimeout:     4 * time.Second,
		ConnMaxIdleTime: 5 * time.Minute,
	})

	// Enable OpenTelemetry tracing (Jaeger)
	if err := redisotel.InstrumentTracing(Client); err != nil {
		log.Fatalf("redis tracing error: %v", err)
	}

	// Enable OpenTelemetry metrics (Prometheus)
	if err := redisotel.InstrumentMetrics(Client); err != nil {
		log.Fatalf("redis metrics error: %v", err)
	}

	// Retry connection (important for Docker startup)
	var err error

	for i := 1; i <= 10; i++ {

		_, err = Client.Ping(Ctx).Result()

		if err == nil {
			log.Println("✅ Connected to Redis")
			return
		}

		log.Printf("⏳ Redis not ready (attempt %d/10)...", i)

		time.Sleep(2 * time.Second)
	}

	log.Fatalf("❌ Redis connection failed: %v", err)
}

//
// ─────────────────────────────────────────────
// Cache Helpers
// ─────────────────────────────────────────────
//

// ClearShopCache removes all cached shop list keys.
// This is used after create/update/delete operations.
func ClearShopCache() {

	var cursor uint64

	for {

		keys, next, err := Client.Scan(Ctx, cursor, "shops:list:*", 100).Result()
		if err != nil {
			log.Printf("cache scan error: %v", err)
			return
		}

		if len(keys) > 0 {

			if err := Client.Del(Ctx, keys...).Err(); err != nil {
				log.Printf("cache delete error: %v", err)
				return
			}

			log.Printf("🧹 cleared %d shop cache keys", len(keys))
		}

		cursor = next

		if cursor == 0 {
			break
		}
	}
}

// DeleteKey removes a single cache key.
func DeleteKey(key string) {

	if err := Client.Del(Ctx, key).Err(); err != nil {
		log.Printf("cache delete error: %v", err)
	}
}

// Set stores a value in Redis with expiration.
func Set(key string, value interface{}, ttl time.Duration) {

	if err := Client.Set(Ctx, key, value, ttl).Err(); err != nil {
		log.Printf("cache set error: %v", err)
	}
}

// Get retrieves a value from Redis.
func Get(key string) (string, error) {
	return Client.Get(Ctx, key).Result()
}
