package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/new-api-tools/backend/internal/models"
	"github.com/new-api-tools/backend/internal/service"
)

// RegisterOperationsRoutes registers /api/operations endpoints.
func RegisterOperationsRoutes(r *gin.RouterGroup) {
	g := r.Group("/operations")
	{
		g.GET("/alerts", GetOperationsAlerts)
		g.GET("/users/:user_id/detail", GetOperationsUserDetail)
	}
}

// GET /api/operations/alerts
func GetOperationsAlerts(c *gin.Context) {
	window := c.DefaultQuery("window", "30d")
	if !validWindow(window) {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid window value", ""))
		return
	}
	alertType := c.DefaultQuery("type", "all")
	severity := c.DefaultQuery("severity", "all")
	limit := parseLimit(c, 100, 300)
	useCache := c.DefaultQuery("no_cache", "false") != "true"

	svc := service.NewOperationsService()
	data, err := svc.GetOperationsAlerts(window, alertType, severity, limit, useCache)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("QUERY_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// GET /api/operations/users/:user_id/detail
func GetOperationsUserDetail(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid user ID", ""))
		return
	}
	window := c.DefaultQuery("window", "30d")
	if !validWindow(window) {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_PARAMS", "Invalid window value", ""))
		return
	}

	svc := service.NewOperationsService()
	data, err := svc.GetOperationsUserDetail(userID, window)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResp("NOT_FOUND", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}
