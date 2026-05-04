package handler

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/new-api-tools/backend/internal/config"
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
		g.GET("/tools-access", GetToolsAccessConfig)
		g.POST("/tools-access", SaveToolsAccessConfig)
		g.GET("/upstream-sync/config", GetUpstreamLogSyncConfig)
		g.GET("/upstream-sync/configs", ListUpstreamLogSyncConfigs)
		g.POST("/upstream-sync/config", SaveUpstreamLogSyncConfig)
		g.POST("/upstream-sync/register", RegisterUpstreamLogSyncConfig)
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

// GET /api/cost/tools-access
func GetToolsAccessConfig(c *gin.Context) {
	info := config.GetAPIKeyInfo()
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"tools_url":   inferRequestBaseURL(c),
			"api_key":     info.APIKey,
			"source":      info.Source,
			"config_path": info.ConfigPath,
			"updated_at":  info.UpdatedAt,
		},
	})
}

// POST /api/cost/tools-access
func SaveToolsAccessConfig(c *gin.Context) {
	var req struct {
		APIKey string `json:"api_key"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid request body", err.Error()))
		return
	}

	info, err := config.SetRuntimeAPIKey(req.APIKey)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_API_KEY", err.Error(), ""))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Tools access config saved",
		"data": gin.H{
			"tools_url":   inferRequestBaseURL(c),
			"api_key":     info.APIKey,
			"source":      info.Source,
			"config_path": info.ConfigPath,
			"updated_at":  info.UpdatedAt,
		},
	})
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

// GET /api/cost/upstream-sync/configs
func ListUpstreamLogSyncConfigs(c *gin.Context) {
	svc := service.NewUpstreamLogSyncService()
	data, err := svc.ListConfigs(false)
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

// POST /api/cost/upstream-sync/register
func RegisterUpstreamLogSyncConfig(c *gin.Context) {
	var req service.UpstreamLogSyncConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid request body", err.Error()))
		return
	}

	svc := service.NewUpstreamLogSyncService()
	data, err := svc.RegisterConfig(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("SAVE_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Upstream log sync config registered", "data": data})
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

func inferRequestBaseURL(c *gin.Context) string {
	proto := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto"))
	if proto == "" {
		proto = "http"
		if c.Request.TLS != nil {
			proto = "https"
		}
	}
	host := strings.TrimSpace(c.GetHeader("X-Forwarded-Host"))
	if host == "" {
		host = c.Request.Host
	}
	if host == "" {
		return ""
	}
	return strings.TrimRight(proto+"://"+host, "/")
}
