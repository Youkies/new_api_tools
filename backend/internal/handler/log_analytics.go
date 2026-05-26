package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/new-api-tools/backend/internal/models"
	"github.com/new-api-tools/backend/internal/service"
)

// RegisterLogAnalyticsRoutes registers /api/analytics endpoints
func RegisterLogAnalyticsRoutes(r *gin.RouterGroup) {
	g := r.Group("/analytics")
	{
		g.GET("/state", GetAnalyticsState)
		g.POST("/process", ProcessLogs)
		g.POST("/batch-process", BatchProcessLogs)
		g.POST("/batch", BatchProcessLogs)
		// Python-compatible routes: /ranking/* and /users/*
		g.GET("/ranking/requests", GetUserRequestRanking)
		g.GET("/ranking/quota", GetUserQuotaRanking)
		g.GET("/users/requests", GetUserRequestRanking)
		g.GET("/users/quota", GetUserQuotaRanking)
		g.GET("/models", GetModelStatistics)
		g.GET("/summary", GetAnalyticsSummary)
		g.GET("/export", ExportLogs)
		g.POST("/reset", ResetAnalytics)
		g.GET("/sync-status", GetSyncStatus)
		g.POST("/check-consistency", CheckDataConsistency)
	}
}

// GET /api/analytics/state
func GetAnalyticsState(c *gin.Context) {
	svc := service.NewLogAnalyticsService()
	state := svc.GetAnalyticsState()
	c.JSON(http.StatusOK, gin.H{"success": true, "data": state})
}

// POST /api/analytics/process
func ProcessLogs(c *gin.Context) {
	svc := service.NewLogAnalyticsService()
	result, err := svc.ProcessLogs()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("PROCESS_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, result)
}

// POST /api/analytics/batch-process or /api/analytics/batch
func BatchProcessLogs(c *gin.Context) {
	maxIter, _ := strconv.Atoi(c.DefaultQuery("max_iterations", "100"))
	maxIter = clampInt(maxIter, 1, 1000)
	svc := service.NewLogAnalyticsService()
	result, err := svc.BatchProcess(maxIter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("PROCESS_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, result)
}

// GET /api/analytics/ranking/requests or /api/analytics/users/requests
func GetUserRequestRanking(c *gin.Context) {
	limit := parseLimit(c, 10, 200)
	svc := service.NewLogAnalyticsService()
	data, err := svc.GetUserRequestRanking(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("QUERY_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// GET /api/analytics/ranking/quota or /api/analytics/users/quota
func GetUserQuotaRanking(c *gin.Context) {
	limit := parseLimit(c, 10, 200)
	svc := service.NewLogAnalyticsService()
	data, err := svc.GetUserQuotaRanking(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("QUERY_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// GET /api/analytics/models
func GetModelStatistics(c *gin.Context) {
	limit := parseLimit(c, 20, 200)
	svc := service.NewLogAnalyticsService()
	data, err := svc.GetModelStatistics(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("QUERY_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// GET /api/analytics/summary
func GetAnalyticsSummary(c *gin.Context) {
	svc := service.NewLogAnalyticsService()
	data, err := svc.GetSummary()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("QUERY_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// GET /api/analytics/export
func ExportLogs(c *gin.Context) {
	format := strings.ToLower(strings.TrimSpace(c.DefaultQuery("format", "csv")))
	if format != "json" {
		format = "csv"
	}

	startTime := parseOptionalInt64(c.Query("start_time"), c.Query("start_timestamp"))
	endTime := parseOptionalInt64(c.Query("end_time"), c.Query("end_timestamp"))
	if startTime <= 0 && endTime <= 0 {
		startTime, endTime = service.DefaultCostRange()
	}

	logType := 0
	if rawType := strings.TrimSpace(c.Query("type")); rawType != "" {
		if parsed, err := strconv.Atoi(rawType); err == nil && parsed > 0 {
			logType = parsed
		}
	}

	quotaPerUnit := parseOptionalInt64(c.Query("quota_per_unit"), "")
	if quotaPerUnit <= 0 {
		quotaPerUnit = 500000
	}

	maxRows := 0
	if rawMaxRows := strings.TrimSpace(c.Query("max_rows")); rawMaxRows != "" {
		parsed, _ := strconv.Atoi(rawMaxRows)
		if parsed > 0 {
			maxRows = clampInt(parsed, 1, 5000000)
		}
	}

	opts := service.LogExportOptions{
		StartTime:    startTime,
		EndTime:      endTime,
		Type:         logType,
		ModelName:    strings.TrimSpace(c.Query("model_name")),
		Username:     strings.TrimSpace(c.Query("username")),
		TokenName:    strings.TrimSpace(c.Query("token_name")),
		Channel:      strings.TrimSpace(firstNonEmpty(c.Query("channel"), c.Query("channel_id"))),
		Group:        strings.TrimSpace(c.Query("group")),
		RequestID:    strings.TrimSpace(c.Query("request_id")),
		QuotaPerUnit: quotaPerUnit,
		MaxRows:      maxRows,
	}

	svc := service.NewLogAnalyticsService()
	rows, err := svc.OpenLogExport(c.Request.Context(), opts)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("EXPORT_ERROR", err.Error(), ""))
		return
	}

	filename := fmt.Sprintf("newapi_logs_%s.%s", time.Now().Format("20060102_150405"), format)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Header("X-Accel-Buffering", "no")

	if format == "json" {
		c.Header("Content-Type", "application/json; charset=utf-8")
		_, err = svc.WriteLogExportJSON(rows, c.Writer, opts)
	} else {
		c.Header("Content-Type", "text/csv; charset=utf-8")
		_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
		_, err = svc.WriteLogExportCSV(rows, c.Writer, opts)
	}
	if err != nil {
		_ = c.Error(err)
		return
	}
}

// POST /api/analytics/reset
func ResetAnalytics(c *gin.Context) {
	svc := service.NewLogAnalyticsService()
	if err := svc.ResetAnalytics(); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("RESET_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "分析数据已重置",
	})
}

// GET /api/analytics/sync-status
func GetSyncStatus(c *gin.Context) {
	svc := service.NewLogAnalyticsService()
	data, err := svc.GetSyncStatus()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("QUERY_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// POST /api/analytics/check-consistency
func CheckDataConsistency(c *gin.Context) {
	autoReset := c.DefaultQuery("auto_reset", "false") == "true"
	svc := service.NewLogAnalyticsService()
	data, err := svc.CheckDataConsistency(autoReset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("CHECK_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

func parseOptionalInt64(primary, fallback string) int64 {
	raw := strings.TrimSpace(primary)
	if raw == "" {
		raw = strings.TrimSpace(fallback)
	}
	if raw == "" {
		return 0
	}
	value, _ := strconv.ParseInt(raw, 10, 64)
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
