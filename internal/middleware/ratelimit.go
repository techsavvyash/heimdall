package middleware

import (
	"context"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/techsavvyash/heimdall/internal/config"
	"github.com/techsavvyash/heimdall/internal/database"
)

// RateLimitMiddleware implements rate limiting using Redis
func RateLimitMiddleware(cfg *config.Config) fiber.Handler {
	return func(c *fiber.Ctx) error {
		redis := database.GetRedis()
		if redis == nil {
			// If Redis is not available, skip rate limiting
			return c.Next()
		}

		// Get client identifier (IP address)
		clientIP := c.IP()
		key := fmt.Sprintf("ip:%s", clientIP)

		// Check rate limit
		ctx := context.Background()
		count, err := redis.IncrementRateLimit(ctx, key, time.Minute)
		if err != nil {
			// If rate limit check fails, allow the request
			return c.Next()
		}

		// Set rate limit headers
		c.Set("X-RateLimit-Limit", fmt.Sprintf("%d", cfg.Server.RateLimitPerMin))
		c.Set("X-RateLimit-Remaining", fmt.Sprintf("%d", max(0, int64(cfg.Server.RateLimitPerMin)-count)))

		// Check if limit exceeded
		if count > int64(cfg.Server.RateLimitPerMin) {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "Rate limit exceeded. Please try again later.",
					"code":    "RATE_LIMIT_EXCEEDED",
				},
			})
		}

		return c.Next()
	}
}

// RateLimitByUser implements per-user rate limiting
func RateLimitByUser(maxRequests int, window time.Duration) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID := GetUserID(c)
		if userID == "" {
			// If no user is authenticated, skip this middleware
			return c.Next()
		}

		redis := database.GetRedis()
		if redis == nil {
			return c.Next()
		}

		key := fmt.Sprintf("user:%s", userID)
		ctx := context.Background()

		count, err := redis.IncrementRateLimit(ctx, key, window)
		if err != nil {
			return c.Next()
		}

		if count > int64(maxRequests) {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "User rate limit exceeded",
					"code":    "USER_RATE_LIMIT_EXCEEDED",
				},
			})
		}

		return c.Next()
	}
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
