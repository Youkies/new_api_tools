package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/new-api-tools/backend/internal/database"
	"github.com/new-api-tools/backend/internal/service"
)

// RegisterSystemRoutes registers /api/system endpoints
func RegisterSystemRoutes(r *gin.RouterGroup) {
	g := r.Group("/system")
	{
		g.GET("/scale", GetSystemScale)
		g.POST("/scale/refresh", RefreshSystemScale)
		g.GET("/warmup-status", GetWarmupStatus)
		g.GET("/indexes", GetIndexStatus)
		g.POST("/indexes/ensure", EnsureIndexes)
	}
}

// GET /api/system/scale
func GetSystemScale(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    service.GetSystemScale(false),
	})
}

// POST /api/system/scale/refresh
func RefreshSystemScale(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    service.GetSystemScale(true),
	})
}

// GET /api/system/warmup-status
func GetWarmupStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    service.GetWarmupStatus(),
	})
}

// GET /api/system/indexes
func GetIndexStatus(c *gin.Context) {
	db := database.Get()

	var indexResults []gin.H
	total := 0
	existing := 0

	for _, idx := range database.RecommendedIndexes {
		total++
		exists, _ := db.IndexExists(idx.Name, idx.Table)
		if exists {
			existing++
		}
		indexResults = append(indexResults, gin.H{
			"name":    idx.Name,
			"table":   idx.Table,
			"columns": idx.Columns,
			"exists":  exists,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"indexes":   indexResults,
			"total":     total,
			"existing":  existing,
			"missing":   total - existing,
			"all_ready": existing == total,
		},
	})
}

// POST /api/system/indexes/ensure
func EnsureIndexes(c *gin.Context) {
	db := database.Get()

	// Run index creation
	db.EnsureIndexes(true, 500*time.Millisecond)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"message": "Index creation completed",
		},
	})
}
