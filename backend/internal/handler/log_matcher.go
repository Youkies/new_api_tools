package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
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
		g.GET("/uploads", ListLogMatchUploads)
		g.POST("/uploads", UploadLogMatchFiles)
		g.DELETE("/uploads/:id", DeleteLogMatchUpload)
	}
}

// POST /api/log-match/analyze
func AnalyzeLogMatches(c *gin.Context) {
	if err := c.Request.ParseMultipartForm(logMatchMaxMemoryBytes); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_UPLOAD", "Invalid multipart upload", err.Error()))
		return
	}

	files, ok := collectLogMatchMultipartFiles(c)
	if !ok {
		return
	}
	uploadedIDs := parseLogMatchUploadedIDs(c)
	if len(uploadedIDs) > 0 {
		storedFiles, err := service.NewLogMatchUploadStore().Load(uploadedIDs)
		if err != nil {
			c.JSON(http.StatusBadRequest, models.ErrorResp("LOAD_UPLOAD_ERROR", err.Error(), ""))
			return
		}
		files = append(files, storedFiles...)
	}
	if len(files) == 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResp("NO_FILES", "Please upload or select at least one upstream CSV file", ""))
		return
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

// GET /api/log-match/uploads
func ListLogMatchUploads(c *gin.Context) {
	items, err := service.NewLogMatchUploadStore().List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResp("LIST_UPLOADS_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"uploads": items}})
}

// POST /api/log-match/uploads
func UploadLogMatchFiles(c *gin.Context) {
	if err := c.Request.ParseMultipartForm(logMatchMaxMemoryBytes); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("INVALID_UPLOAD", "Invalid multipart upload", err.Error()))
		return
	}

	files, ok := collectLogMatchMultipartFiles(c)
	if !ok {
		return
	}
	if len(files) == 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResp("NO_FILES", "Please upload at least one upstream CSV file", ""))
		return
	}

	startTime := parseOptionalInt64(c.PostForm("start_time"), c.PostForm("start_timestamp"))
	endTime := parseOptionalInt64(c.PostForm("end_time"), c.PostForm("end_timestamp"))
	items, err := service.NewLogMatchUploadStore().Save(
		files,
		strings.TrimSpace(c.PostForm("source_url")),
		strings.TrimSpace(c.PostForm("source_name")),
		startTime,
		endTime,
	)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("SAVE_UPLOAD_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Log files uploaded", "data": gin.H{"uploads": items}})
}

// DELETE /api/log-match/uploads/:id
func DeleteLogMatchUpload(c *gin.Context) {
	if err := service.NewLogMatchUploadStore().Delete(c.Param("id")); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResp("DELETE_UPLOAD_ERROR", err.Error(), ""))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Uploaded log file deleted"})
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

func collectLogMatchMultipartFiles(c *gin.Context) ([]service.LogMatchUploadedFile, bool) {
	form := c.Request.MultipartForm
	if form == nil {
		return nil, true
	}
	uploadedHeaders := append([]*multipart.FileHeader{}, form.File["files"]...)
	uploadedHeaders = append(uploadedHeaders, form.File["file"]...)
	if len(uploadedHeaders) > logMatchMaxFiles {
		c.JSON(http.StatusBadRequest, models.ErrorResp("TOO_MANY_FILES", fmt.Sprintf("At most %d files are allowed", logMatchMaxFiles), ""))
		return nil, false
	}

	files := make([]service.LogMatchUploadedFile, 0, len(uploadedHeaders))
	for _, header := range uploadedHeaders {
		if header.Size > logMatchMaxFileBytes {
			c.JSON(http.StatusBadRequest, models.ErrorResp("FILE_TOO_LARGE", fmt.Sprintf("%s exceeds the %d MB limit", header.Filename, logMatchMaxFileBytes>>20), ""))
			return nil, false
		}

		file, err := header.Open()
		if err != nil {
			c.JSON(http.StatusBadRequest, models.ErrorResp("READ_UPLOAD_ERROR", err.Error(), ""))
			return nil, false
		}
		data, readErr := io.ReadAll(io.LimitReader(file, logMatchMaxFileBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			c.JSON(http.StatusBadRequest, models.ErrorResp("READ_UPLOAD_ERROR", readErr.Error(), ""))
			return nil, false
		}
		if closeErr != nil {
			c.JSON(http.StatusBadRequest, models.ErrorResp("READ_UPLOAD_ERROR", closeErr.Error(), ""))
			return nil, false
		}
		if len(data) > logMatchMaxFileBytes {
			c.JSON(http.StatusBadRequest, models.ErrorResp("FILE_TOO_LARGE", fmt.Sprintf("%s exceeds the %d MB limit", header.Filename, logMatchMaxFileBytes>>20), ""))
			return nil, false
		}
		files = append(files, service.LogMatchUploadedFile{Name: header.Filename, Data: data})
	}
	return files, true
}

func parseLogMatchUploadedIDs(c *gin.Context) []string {
	values := append([]string{}, c.PostFormArray("uploaded_ids")...)
	if raw := strings.TrimSpace(c.PostForm("uploaded_ids")); raw != "" {
		values = append(values, strings.Split(raw, ",")...)
	}
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		id := strings.TrimSpace(value)
		if id != "" && !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	return result
}
