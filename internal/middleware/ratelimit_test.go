package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRateLimitByIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimitByIP(3, 50*time.Millisecond))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	hit := func() int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/x", nil)
		req.RemoteAddr = "1.2.3.4:1000"
		r.ServeHTTP(w, req)
		return w.Code
	}

	codes := make([]int, 0, 5)
	for i := 0; i < 5; i++ {
		codes = append(codes, hit())
	}
	for i, want := range []int{200, 200, 200, 429, 429} {
		if codes[i] != want {
			t.Fatalf("第 %d 次请求期望 %d，实际 %d", i+1, want, codes[i])
		}
	}

	// 窗口过期后重新计数
	time.Sleep(60 * time.Millisecond)
	if got := hit(); got != 200 {
		t.Fatalf("窗口过期后应放行，实际 %d", got)
	}
}
