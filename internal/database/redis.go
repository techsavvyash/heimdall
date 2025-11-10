package database

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/techsavvyash/heimdall/internal/config"
)

// RedisClient wraps the Redis client
type RedisClient struct {
	client *redis.Client
}

var redisClient *RedisClient

// ConnectRedis establishes a connection to Redis
func ConnectRedis(cfg *config.Config) error {
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.GetRedisAddr(),
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("failed to connect to Redis: %w", err)
	}

	redisClient = &RedisClient{client: client}
	log.Println("Redis connection established successfully")
	return nil
}

// GetRedis returns the Redis client instance
func GetRedis() *RedisClient {
	return redisClient
}

// CloseRedis closes the Redis connection
func CloseRedis() error {
	if redisClient != nil {
		return redisClient.client.Close()
	}
	return nil
}

// Set stores a value with expiration
func (r *RedisClient) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error {
	return r.client.Set(ctx, key, value, expiration).Err()
}

// Get retrieves a value
func (r *RedisClient) Get(ctx context.Context, key string) (string, error) {
	return r.client.Get(ctx, key).Result()
}

// Del deletes a key
func (r *RedisClient) Del(ctx context.Context, keys ...string) error {
	return r.client.Del(ctx, keys...).Err()
}

// Exists checks if a key exists
func (r *RedisClient) Exists(ctx context.Context, keys ...string) (int64, error) {
	return r.client.Exists(ctx, keys...).Result()
}

// SetJSON stores a JSON-encoded value
func (r *RedisClient) SetJSON(ctx context.Context, key string, value interface{}, expiration time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	return r.Set(ctx, key, data, expiration)
}

// GetJSON retrieves and decodes a JSON value
func (r *RedisClient) GetJSON(ctx context.Context, key string, dest interface{}) error {
	data, err := r.Get(ctx, key)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(data), dest)
}

// --- Token Management ---

// BlacklistToken adds a token to the blacklist
func (r *RedisClient) BlacklistToken(ctx context.Context, tokenID string, expiration time.Duration) error {
	key := fmt.Sprintf("token:blacklist:%s", tokenID)
	return r.Set(ctx, key, "1", expiration)
}

// IsTokenBlacklisted checks if a token is blacklisted
func (r *RedisClient) IsTokenBlacklisted(ctx context.Context, tokenID string) (bool, error) {
	key := fmt.Sprintf("token:blacklist:%s", tokenID)
	count, err := r.Exists(ctx, key)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// StoreRefreshToken stores a refresh token
func (r *RedisClient) StoreRefreshToken(ctx context.Context, userID, tokenID string, expiration time.Duration) error {
	key := fmt.Sprintf("refresh_token:%s:%s", userID, tokenID)
	return r.Set(ctx, key, tokenID, expiration)
}

// ValidateRefreshToken checks if a refresh token exists
func (r *RedisClient) ValidateRefreshToken(ctx context.Context, userID, tokenID string) (bool, error) {
	key := fmt.Sprintf("refresh_token:%s:%s", userID, tokenID)
	count, err := r.Exists(ctx, key)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// RevokeRefreshToken removes a refresh token
func (r *RedisClient) RevokeRefreshToken(ctx context.Context, userID, tokenID string) error {
	key := fmt.Sprintf("refresh_token:%s:%s", userID, tokenID)
	return r.Del(ctx, key)
}

// RevokeAllUserTokens revokes all refresh tokens for a user
func (r *RedisClient) RevokeAllUserTokens(ctx context.Context, userID string) error {
	pattern := fmt.Sprintf("refresh_token:%s:*", userID)
	iter := r.client.Scan(ctx, 0, pattern, 0).Iterator()

	var keys []string
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}

	if err := iter.Err(); err != nil {
		return fmt.Errorf("failed to scan keys: %w", err)
	}

	if len(keys) > 0 {
		return r.Del(ctx, keys...)
	}

	return nil
}

// --- Session Management ---

// StoreSession stores user session data
func (r *RedisClient) StoreSession(ctx context.Context, sessionID string, data interface{}, expiration time.Duration) error {
	key := fmt.Sprintf("session:%s", sessionID)
	return r.SetJSON(ctx, key, data, expiration)
}

// GetSession retrieves user session data
func (r *RedisClient) GetSession(ctx context.Context, sessionID string, dest interface{}) error {
	key := fmt.Sprintf("session:%s", sessionID)
	return r.GetJSON(ctx, key, dest)
}

// DeleteSession removes a session
func (r *RedisClient) DeleteSession(ctx context.Context, sessionID string) error {
	key := fmt.Sprintf("session:%s", sessionID)
	return r.Del(ctx, key)
}

// --- Caching ---

// CacheUserPermissions caches user permissions
func (r *RedisClient) CacheUserPermissions(ctx context.Context, userID string, permissions []string, expiration time.Duration) error {
	key := fmt.Sprintf("user:permissions:%s", userID)
	return r.SetJSON(ctx, key, permissions, expiration)
}

// GetCachedUserPermissions retrieves cached user permissions
func (r *RedisClient) GetCachedUserPermissions(ctx context.Context, userID string) ([]string, error) {
	key := fmt.Sprintf("user:permissions:%s", userID)
	var permissions []string
	err := r.GetJSON(ctx, key, &permissions)
	if err != nil {
		return nil, err
	}
	return permissions, nil
}

// InvalidateUserPermissions removes cached permissions
func (r *RedisClient) InvalidateUserPermissions(ctx context.Context, userID string) error {
	key := fmt.Sprintf("user:permissions:%s", userID)
	return r.Del(ctx, key)
}

// --- Rate Limiting ---

// IncrementRateLimit increments the rate limit counter
func (r *RedisClient) IncrementRateLimit(ctx context.Context, key string, window time.Duration) (int64, error) {
	rateLimitKey := fmt.Sprintf("ratelimit:%s", key)

	// Increment counter
	count, err := r.client.Incr(ctx, rateLimitKey).Result()
	if err != nil {
		return 0, err
	}

	// Set expiration on first increment
	if count == 1 {
		r.client.Expire(ctx, rateLimitKey, window)
	}

	return count, nil
}

// GetRateLimitCount gets the current rate limit count
func (r *RedisClient) GetRateLimitCount(ctx context.Context, key string) (int64, error) {
	rateLimitKey := fmt.Sprintf("ratelimit:%s", key)
	count, err := r.client.Get(ctx, rateLimitKey).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return count, err
}

// DeletePattern deletes all keys matching a pattern
func (r *RedisClient) DeletePattern(ctx context.Context, pattern string) error {
	iter := r.client.Scan(ctx, 0, pattern, 0).Iterator()

	var keys []string
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}

	if err := iter.Err(); err != nil {
		return fmt.Errorf("failed to scan keys: %w", err)
	}

	if len(keys) > 0 {
		return r.Del(ctx, keys...)
	}

	return nil
}
