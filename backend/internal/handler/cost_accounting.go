package handler

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/new-api-tools/backend/internal/models"
	"github.com/new-api-tools/backend/internal/service"
)

// RegisterCostAccountingRoutes registers /api/cost endpoints.
func RegisterCostAccountingRoutes(r *gin.RouterGroup) {
	g := r.Group("/cost")
	{
		g.GET("/summary", GetCostSummary)
		g.GET("/rules", GetCostRules)
		g.POST("/rules", SaveCostRules)
		g.GET("/upstream-sync/config", GetUpstreamLogSyncConfig)
		g.POST("/upstream-sync/config", SaveUpstreamLogSyncConfig)
		g.POST("/upstream-sync/run", RunUpstreamLogSync)
		g.POST("/upstream-sync/upload", UploadUpstreamLogs)
	}
}

// GET /api/cost/summary
func GetCostSummary(c *gin.Context) {
	defaultStart, defaultEnd := service.DefaultCostRange()
	startTime := parseInt64Query(c, "start_time", defaultStart)
	endTime := parseInt64Query(c, "end_time", defaultEnd)
	if endTime < startTime {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_RANGE", "end_time must be greater than or equal to start_time", ""))
		return
	}

	var channelID *int64
	if raw := c.Query("channel_id"); raw != "" && raw != "all" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 0 {
			c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_CHANNEL", "channel_id must be a positive integer", ""))
			return
		}
		channelID = &id
	}

	svc := service.NewCostAccountingService()
	data, err := svc.GetSummary(startTime, endTime, channelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("QUERY_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// GET /api/cost/rules
func GetCostRules(c *gin.Context) {
	svc := service.NewCostAccountingService()
	data, err := svc.GetRulesPayload()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("QUERY_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// POST /api/cost/rules
func SaveCostRules(c *gin.Context) {
	var req struct {
		Rules []service.ChannelCostRule `json:"rules"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid request body", err.Error()))
		return
	}

	svc := service.NewCostAccountingService()
	rules, err := svc.SaveRules(req.Rules)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("SAVE_ERROR", err.Error(), ""))
		return
	}

	channels, _ := svc.ListChannels()
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Cost rules saved",
		"data": gin.H{
			"rules":    rules,
			"channels": channels,
		},
	})
}

// GET /api/cost/upstream-sync/config
func GetUpstreamLogSyncConfig(c *gin.Context) {
	svc := service.NewUpstreamLogSyncService()
	data, err := svc.GetConfig(false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("QUERY_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// POST /api/cost/upstream-sync/config
func SaveUpstreamLogSyncConfig(c *gin.Context) {
	var req service.UpstreamLogSyncConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid request body", err.Error()))
		return
	}

	svc := service.NewUpstreamLogSyncService()
	data, err := svc.SaveConfig(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("SAVE_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Upstream log sync config saved", "data": data})
}

// POST /api/cost/upstream-sync/run
func RunUpstreamLogSync(c *gin.Context) {
	var req service.UpstreamLogSyncRunOptions
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid request body", err.Error()))
		return
	}

	svc := service.NewUpstreamLogSyncService()
	data, err := svc.RunSync(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("SYNC_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Upstream logs synced", "data": data})
}

// POST /api/cost/upstream-sync/upload
func UploadUpstreamLogs(c *gin.Context) {
	var req service.UpstreamLogUploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid request body", err.Error()))
		return
	}

	svc := service.NewUpstreamLogSyncService()
	data, err := svc.UploadLogs(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("UPLOAD_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Upstream logs uploaded", "data": data})
}

func parseInt64Query(c *gin.Context, key string, defaultVal int64) int64 {
	raw := c.Query(key)
	if raw == "" {
		return defaultVal
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return defaultVal
	}
	return value
}
