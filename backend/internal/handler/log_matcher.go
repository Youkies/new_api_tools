package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/new-api-tools/backend/internal/models"
	"github.com/new-api-tools/backend/internal/service"
)

const (
	logMatchMaxFiles       = 20
	logMatchMaxFileBytes   = 25 << 20
	logMatchMaxMemoryBytes = 32 << 20
)

// RegisterLogMatcherRoutes registers /api/log-match endpoints.
func RegisterLogMatcherRoutes(r *gin.RouterGroup) {
	g := r.Group("/log-match")
	{
		g.POST("/analyze", AnalyzeLogMatches)
	}
}

// POST /api/log-match/analyze
func AnalyzeLogMatches(c *gin.Context) {
	if err := c.Request.ParseMultipartForm(logMatchMaxMemoryBytes); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_UPLOAD", "Invalid multipart upload", err.Error()))
		return
	}

	form := c.Request.MultipartForm
	if form == nil || len(form.File["files"]) == 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResp("NO_FILES", "Please upload at least one upstream CSV file", ""))
		return
	}

	uploadedHeaders := form.File["files"]
	if len(uploadedHeaders) > logMatchMaxFiles {
		c.JSON(http.StatusBadRequest, models.ErrorResp("TOO_MANY_FILES", fmt.Sprintf("At most %d files are allowed", logMatchMaxFiles), ""))
		return
	}

	files := make([]service.LogMatchUploadedFile, 0, len(uploadedHeaders))
	for _, header := range uploadedHeaders {
		if header.Size > logMatchMaxFileBytes {
			c.JSON(http.StatusBadRequest, models.ErrorResp("FILE_TOO_LARGE", fmt.Sprintf("%s exceeds the %d MB limit", header.Filename, logMatchMaxFileBytes>>20), ""))
			return
		}

		file, err := header.Open()
		if err != nil {
			c.JSON(http.StatusBadRequest, models.ErrorResp("READ_UPLOAD_ERROR", err.Error(), ""))
			return
		}
		data, readErr := io.ReadAll(io.LimitReader(file, logMatchMaxFileBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			c.JSON(http.StatusBadRequest, models.ErrorResp("READ_UPLOAD_ERROR", readErr.Error(), ""))
			return
		}
		if closeErr != nil {
			c.JSON(http.StatusBadRequest, models.ErrorResp("READ_UPLOAD_ERROR", closeErr.Error(), ""))
			return
		}
		if len(data) > logMatchMaxFileBytes {
			c.JSON(http.StatusBadRequest, models.ErrorResp("FILE_TOO_LARGE", fmt.Sprintf("%s exceeds the %d MB limit", header.Filename, logMatchMaxFileBytes>>20), ""))
			return
		}
		files = append(files, service.LogMatchUploadedFile{
			Name: header.Filename,
			Data: data,
		})
	}

	startTime := parseOptionalInt64(c.PostForm("start_time"), c.PostForm("start_timestamp"))
	endTime := parseOptionalInt64(c.PostForm("end_time"), c.PostForm("end_timestamp"))
	timeWindow := parsePositiveFormInt(c, "time_window_seconds", 0)
	maxRows := parsePositiveFormInt(c, "max_rows", 50000)
	if maxRows > 0 {
		maxRows = clampInt(maxRows, 1, 5000000)
	}

	configJSON := strings.TrimSpace(c.PostForm("config_json"))
	if timeWindow > 0 {
		patch := fmt.Sprintf(`{"time_window_seconds":%d}`, timeWindow)
		if configJSON != "" {
			patch = mergeLogMatchConfigJSON(configJSON, timeWindow)
		}
		configJSON = patch
	}

	svc := service.NewLogMatcherService()
	result, err := svc.Analyze(c.Request.Context(), files, service.LogMatchAnalyzeOptions{
		StartTime:  startTime,
		EndTime:    endTime,
		MaxRows:    maxRows,
		ConfigJSON: configJSON,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("MATCH_ERROR", err.Error(), ""))
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

func parsePositiveFormInt(c *gin.Context, key string, defaultValue int) int {
	raw := strings.TrimSpace(c.PostForm(key))
	if raw == "" {
		return defaultValue
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return defaultValue
	}
	return value
}

func mergeLogMatchConfigJSON(raw string, timeWindow int) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Sprintf(`{"time_window_seconds":%d}`, timeWindow)
	}

	var patch map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &patch); err != nil {
		return trimmed
	}
	patch["time_window_seconds"] = timeWindow
	next, err := json.Marshal(patch)
	if err != nil {
		return trimmed
	}
	return string(next)
}
