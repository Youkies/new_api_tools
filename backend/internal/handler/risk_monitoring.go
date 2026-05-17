package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/new-api-tools/backend/internal/models"
	"github.com/new-api-tools/backend/internal/service"
)

// RegisterRiskMonitoringRoutes registers /api/risk endpoints
func RegisterRiskMonitoringRoutes(r *gin.RouterGroup) {
	g := r.Group("/risk")
	{
		g.GET("/leaderboards", GetLeaderboards)
		g.GET("/queue", GetRiskQueue)
		g.GET("/alt-account/cases", GetAltAccountCases)
		g.GET("/alt-account/cases/:case_id", GetAltAccountCase)
		g.POST("/alt-account/cases/:case_id/assess", AssessAltAccountCase)
		g.POST("/actions/batches", ExecuteRiskActionBatch)
		g.POST("/actions/batches/:batch_id/revert", RevertRiskActionBatch)
		g.GET("/users/:user_id/analysis", GetUserRiskAnalysis)
		g.GET("/ban-records", ListBanRecords)
		g.GET("/token-rotation", GetTokenRotationUsers)
		g.GET("/affiliated-accounts", GetAffiliatedAccounts)
		g.GET("/same-ip-registrations", GetSameIPRegistrations)
	}
}

// GET /api/risk/alt-account/cases
func GetAltAccountCases(c *gin.Context) {
	caseType := c.DefaultQuery("case_type", "all")
	window := c.DefaultQuery("window", "30d")
	if !validWindow(window) {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid window value", ""))
		return
	}
	limit := parseLimit(c, 50, 200)
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 {
		offset = 0
	}
	useCache := c.DefaultQuery("no_cache", "false") != "true"

	svc := service.NewAltAccountRiskService()
	data, err := svc.GetAltAccountCases(caseType, window, limit, offset, useCache)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("QUERY_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// GET /api/risk/alt-account/cases/:case_id
func GetAltAccountCase(c *gin.Context) {
	caseID := c.Param("case_id")
	window := c.DefaultQuery("window", "30d")
	if !validWindow(window) {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid window value", ""))
		return
	}
	svc := service.NewAltAccountRiskService()
	data, err := svc.GetAltAccountCase(caseID, window)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResp("NOT_FOUND", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// POST /api/risk/alt-account/cases/:case_id/assess
func AssessAltAccountCase(c *gin.Context) {
	caseID := c.Param("case_id")
	var req struct {
		Window  string `json:"window"`
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		Model   string `json:"model"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid request", err.Error()))
		return
	}
	if req.Window == "" {
		req.Window = c.DefaultQuery("window", "30d")
	}
	if !validWindow(req.Window) {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid window value", ""))
		return
	}

	svc := service.NewAltAccountRiskService()
	data := svc.AssessAltAccountCase(caseID, req.Window, req.BaseURL, req.APIKey, req.Model)
	if success, _ := data["success"].(bool); !success {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "data": data, "message": data["message"]})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// GET /api/risk/leaderboards
func GetLeaderboards(c *gin.Context) {
	windowsStr := c.DefaultQuery("windows", "1h,3h,6h,12h,24h")
	windows := strings.Split(windowsStr, ",")
	limit := parseLimit(c, 10, 100)
	sortBy := c.DefaultQuery("sort_by", "requests")
	useCache := c.DefaultQuery("no_cache", "false") != "true"

	if sortBy != "risk_score" && sortBy != "requests" && sortBy != "quota" && sortBy != "failure_rate" {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid sort_by: "+sortBy, ""))
		return
	}

	svc := service.NewRiskMonitoringService()
	data, err := svc.GetLeaderboards(windows, limit, sortBy, useCache)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("QUERY_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// GET /api/risk/queue
func GetRiskQueue(c *gin.Context) {
	window := c.DefaultQuery("window", "24h")
	if !validWindow(window) {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid window value", ""))
		return
	}
	page := parsePage(c)
	pageSize := parsePageSize(c, 50, 200)
	sortBy := c.DefaultQuery("sort", "risk_score")
	useCache := c.DefaultQuery("no_cache", "false") != "true"

	if sortBy != "risk_score" && sortBy != "requests" && sortBy != "quota" && sortBy != "failure_rate" {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid sort: "+sortBy, ""))
		return
	}

	svc := service.NewRiskMonitoringService()
	data, err := svc.GetRiskQueue(window, page, pageSize, sortBy, useCache)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("QUERY_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// POST /api/risk/actions/batches
func ExecuteRiskActionBatch(c *gin.Context) {
	var req service.RiskActionBatchRequest
	req.Action = "ban"
	req.DryRun = true
	req.DisableTokens = true
	req.ExcludeProtectedRoles = true
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid request body", err.Error()))
		return
	}
	if req.Action != "ban" && req.Action != "unban" {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid action", ""))
		return
	}

	svc := service.NewRiskMonitoringService()
	data, err := svc.ExecuteRiskActionBatch(req)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("BATCH_ACTION_ERROR", err.Error(), ""))
		return
	}
	message, _ := data["message"].(string)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": message, "data": data})
}

// POST /api/risk/actions/batches/:batch_id/revert
func RevertRiskActionBatch(c *gin.Context) {
	batchID := c.Param("batch_id")
	svc := service.NewRiskMonitoringService()
	data, err := svc.RevertRiskActionBatch(batchID)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("BATCH_REVERT_ERROR", err.Error(), ""))
		return
	}
	message, _ := data["message"].(string)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": message, "data": data})
}

// GET /api/risk/users/:user_id/analysis
func GetUserRiskAnalysis(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid user ID", ""))
		return
	}
	window := c.DefaultQuery("window", "24h")
	seconds, ok := service.WindowSeconds[window]
	if !ok {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid window: "+window, ""))
		return
	}

	var endTime *int64
	if et := c.Query("end_time"); et != "" {
		v, err := strconv.ParseInt(et, 10, 64)
		if err == nil {
			endTime = &v
		}
	}

	svc := service.NewRiskMonitoringService()
	data, err := svc.GetUserAnalysis(userID, seconds, endTime)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("QUERY_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// GET /api/risk/ban-records
func ListBanRecords(c *gin.Context) {
	page := parsePage(c)
	pageSize := parsePageSize(c, 50, 200)
	action := c.Query("action")

	var userID *int64
	if uid := c.Query("user_id"); uid != "" {
		v, err := strconv.ParseInt(uid, 10, 64)
		if err == nil {
			userID = &v
		}
	}

	svc := service.NewRiskMonitoringService()
	data := svc.ListBanRecords(page, pageSize, action, userID)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// GET /api/risk/token-rotation
func GetTokenRotationUsers(c *gin.Context) {
	window := c.DefaultQuery("window", "24h")
	if !validWindow(window) {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid window value", ""))
		return
	}
	minTokens, _ := strconv.Atoi(c.DefaultQuery("min_tokens", "5"))
	maxReqPerToken, _ := strconv.Atoi(c.DefaultQuery("max_requests_per_token", "10"))
	limit := parseLimit(c, 50, 500)

	svc := service.NewRiskMonitoringService()
	data, err := svc.GetTokenRotationUsers(window, minTokens, maxReqPerToken, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("QUERY_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// GET /api/risk/affiliated-accounts
func GetAffiliatedAccounts(c *gin.Context) {
	minInvited, _ := strconv.Atoi(c.DefaultQuery("min_invited", "3"))
	limit := parseLimit(c, 50, 500)

	svc := service.NewRiskMonitoringService()
	data, err := svc.GetAffiliatedAccounts(minInvited, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("QUERY_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// GET /api/risk/same-ip-registrations
func GetSameIPRegistrations(c *gin.Context) {
	window := c.DefaultQuery("window", "7d")
	if !validWindow(window) {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid window value", ""))
		return
	}
	minUsers, _ := strconv.Atoi(c.DefaultQuery("min_users", "3"))
	limit := parseLimit(c, 50, 500)

	svc := service.NewRiskMonitoringService()
	data, err := svc.GetSameIPRegistrations(window, minUsers, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("QUERY_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}
