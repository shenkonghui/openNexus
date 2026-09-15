package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ipRateLimiter 是简单的每 IP 固定窗口限流器（内存态，重启清零）。
// 用于登录/注册等凭证端点，防公网暴露时被在线爆破；不追求精确，够用即可。
type ipRateLimiter struct {
	mu   sync.Mutex
	n    int
	win  time.Duration
	hits map[string]ipHit
}

type ipHit struct {
	count int
	reset time.Time
}

// RateLimitByIP 返回每 IP 每 window 最多 n 次请求的限流中间件，超出返回 429。
func RateLimitByIP(n int, window time.Duration) gin.HandlerFunc {
	l := &ipRateLimiter{n: n, win: window, hits: make(map[string]ipHit)}
	return func(c *gin.Context) {
		ip := c.ClientIP()
		now := time.Now()
		l.mu.Lock()
		h := l.hits[ip]
		if now.After(h.reset) {
			h = ipHit{reset: now.Add(l.win)}
		}
		h.count++
		l.hits[ip] = h
		// 懒人清理：map 过大时全量扫一遍过期窗口，防长期运行被伪造源 IP 撑大。
		if len(l.hits) > 10000 {
			for k, v := range l.hits {
				if now.After(v.reset) {
					delete(l.hits, k)
				}
			}
		}
		l.mu.Unlock()

		if h.count > l.n {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": gin.H{"code": "RATE_LIMITED", "message": "请求过于频繁，请稍后再试"}})
			return
		}
		c.Next()
	}
}
