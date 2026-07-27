package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"opennexus/internal/repository"
)

// ToolCallHandler 提供工具调用历史查询（「工具调用记录」页面）。
type ToolCallHandler struct {
	repo *repository.ToolCallRecordRepository
}

func NewToolCallHandler(repo *repository.ToolCallRecordRepository) *ToolCallHandler {
	return &ToolCallHandler{repo: repo}
}

// List 分页查询当前用户的工具调用记录。
// GET /api/v1/tool-calls?kind=execute&session_id=1&limit=50&offset=0
func (h *ToolCallHandler) List(c *gin.Context) {
	userID := c.GetUint("user_id")
	opts := repository.ToolCallListOptions{
		Kind: c.Query("kind"),
	}
	if v, err := strconv.ParseUint(c.Query("session_id"), 10, 64); err == nil {
		opts.DBSessionID = uint(v)
	}
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 {
		if v > 200 {
			v = 200
		}
		opts.Limit = v
	}
	if v, err := strconv.Atoi(c.Query("offset")); err == nil && v > 0 {
		opts.Offset = v
	}
	items, total, err := h.repo.ListByUser(userID, opts)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "LIST_TOOL_CALLS_FAILED", err.Error())
		return
	}
	Success(c, http.StatusOK, gin.H{"items": items, "total": total})
}
