package handler

import (
	"context"
	"fmt"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/response"
	"io"
	"log"
	"mime/multipart"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// multipartCleanupReader wraps a multipart.File and ensures temporary files
// are cleaned up only after the file is closed by the async goroutine.
// This prevents premature deletion of temp files by net/http's automatic cleanup.
type multipartCleanupReader struct {
	multipart.File
	cleanup func() error
}

// Close closes the underlying file and then removes multipart temporary files.
func (r *multipartCleanupReader) Close() error {
	err := r.File.Close()
	if r.cleanup != nil {
		if cerr := r.cleanup(); err == nil {
			err = cerr
		}
	}
	return err
}

// validateGroupIDFromQuery validates and parses group ID from a query parameter.
// Returns 0 and false if validation fails (error is already sent to client)
func validateGroupIDFromQuery(c *gin.Context) (uint, bool) {
	groupIDStr := c.Query("group_id")
	if groupIDStr == "" {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.group_id_required")
		return 0, false
	}

	groupID, err := strconv.Atoi(groupIDStr)
	if err != nil || groupID <= 0 {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id_format")
		return 0, false
	}

	return uint(groupID), true
}

// validateKeysText validates the keys text input
// Returns false if validation fails (error is already sent to client)
func validateKeysText(c *gin.Context, keysText string) bool {
	if strings.TrimSpace(keysText) == "" {
		response.ErrorI18nFromAPIError(c, app_errors.ErrValidation, "validation.keys_text_empty")
		return false
	}

	return true
}

// findGroupByID is a helper function to find a group by its ID.
func (s *Server) findGroupByID(c *gin.Context, groupID uint) (*models.Group, bool) {
	// 1) Try cache first (fast path, avoids DB during heavy writes)
	if cached, err := s.GroupManager.GetGroupByID(groupID); err == nil && cached != nil {
		logrus.WithField("group_id", groupID).Debug("findGroupByID: Found in cache")
		return cached, true
	}

	// 2) Short DB lookup with small timeout, then fallback to cache again if needed
	var group models.Group
	// Make timeout configurable with sane lower bound, default to 5000ms (increased from 1200ms)
	timeoutMs := utils.ParseInteger(utils.GetEnvOrDefault("DB_LOOKUP_TIMEOUT_MS", "5000"), 5000)
	if timeoutMs < 50 {
		timeoutMs = 50
	} else if timeoutMs > 10000 {
		timeoutMs = 10000
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	logrus.WithFields(logrus.Fields{
		"group_id":   groupID,
		"timeout_ms": timeoutMs,
	}).Debug("findGroupByID: Querying database")

	if err := s.DB.WithContext(ctx).Where("id = ?", groupID).Limit(1).Find(&group).Error; err != nil {
		logrus.WithFields(logrus.Fields{
			"group_id": groupID,
			"error":    err,
		}).Warn("findGroupByID: Database query failed, trying cache fallback")

		// DB busy - try cache fallback again, otherwise return error
		if cached, err2 := s.GroupManager.GetGroupByID(groupID); err2 == nil {
			logrus.WithField("group_id", groupID).Info("findGroupByID: Using cache fallback after DB error")
			return cached, true
		}
		response.Error(c, app_errors.ParseDBError(err))
		return nil, false
	}
	if group.ID == 0 {
		logrus.WithField("group_id", groupID).Warn("findGroupByID: Group not found in database")
		response.Error(c, app_errors.ErrResourceNotFound)
		return nil, false
	}

	logrus.WithField("group_id", groupID).Debug("findGroupByID: Found in database")
	return &group, true
}

// KeyTextRequest defines a generic payload for operations requiring a group ID and a text block of keys.
type KeyTextRequest struct {
	GroupID  uint   `json:"group_id" binding:"required"`
	KeysText string `json:"keys_text" binding:"required"`
}

// GroupIDRequest defines a generic payload for operations requiring only a group ID.
type GroupIDRequest struct {
	GroupID uint `json:"group_id" binding:"required"`
}

// ValidateGroupKeysRequest defines the payload for validating keys in a group.
type ValidateGroupKeysRequest struct {
	GroupID uint   `json:"group_id" binding:"required"`
	Status  string `json:"status,omitempty"`
}

// AddMultipleKeys handles creating new keys from a text block within a specific group.
func (s *Server) AddMultipleKeys(c *gin.Context) {
	var req KeyTextRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	if _, ok := s.findGroupByID(c, req.GroupID); !ok {
		return
	}

	if !validateKeysText(c, req.KeysText) {
		return
	}

	result, err := s.KeyService.AddMultipleKeys(req.GroupID, req.KeysText)
	if err != nil {
		if strings.Contains(err.Error(), "batch size exceeds the limit") {
			response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation, err.Error()))
		} else if err.Error() == "no valid keys found in the input text" {
			response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation, err.Error()))
		} else {
			response.Error(c, app_errors.ParseDBError(err))
		}
		return
	}

	response.Success(c, result)
}

// AddMultipleKeysAsync handles creating new keys from a text block within a specific group.
func (s *Server) AddMultipleKeysAsync(c *gin.Context) {
	var req KeyTextRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logrus.WithError(err).Error("AddMultipleKeysAsync: Failed to bind JSON")
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	logrus.WithFields(logrus.Fields{
		"group_id":      req.GroupID,
		"keys_text_len": len(req.KeysText),
	}).Info("AddMultipleKeysAsync: Starting async key import")

	group, ok := s.findGroupByID(c, req.GroupID)
	if !ok {
		logrus.WithField("group_id", req.GroupID).Error("AddMultipleKeysAsync: Group not found")
		return
	}

	if !validateKeysText(c, req.KeysText) {
		logrus.WithField("group_id", req.GroupID).Error("AddMultipleKeysAsync: Invalid keys text")
		return
	}

	taskStatus, err := s.KeyImportService.StartImportTask(group, req.KeysText)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"group_id": req.GroupID,
			"error":    err,
		}).Error("AddMultipleKeysAsync: Failed to start import task")
		response.Error(c, app_errors.NewAPIError(app_errors.ErrTaskInProgress, err.Error()))
		return
	}

	logrus.WithFields(logrus.Fields{
		"group_id":   req.GroupID,
		"group_name": group.Name,
		"total_keys": taskStatus.Total,
	}).Info("AddMultipleKeysAsync: Task started successfully")

	response.Success(c, taskStatus)
}

// AddMultipleKeysAsyncStream handles creating new keys from a file upload using streaming.
// This method is optimized for large files (>10MB) and processes keys in batches while reading.
// Memory usage is constant regardless of file size.
//
// Note on streaming behavior:
// The current implementation uses c.Request.FormFile() which internally calls ParseMultipartForm.
// According to Go's net/http implementation (as of Go 1.25.6):
// - ParseMultipartForm reads and parses the entire multipart stream before returning
// - Files larger than ~32MB are spilled to temporary files on disk
// - However, the multipart structure itself must be fully parsed first
// This means true concurrent upload/processing is not possible with FormFile.
//
// For true streaming (concurrent upload and processing), consider:
// 1. Require group_id in query parameter (not form data)
// 2. Use c.Request.MultipartReader() to read parts on-demand
// 3. Process file content as it arrives without waiting for full upload
//
// Current implementation is kept for backward compatibility and simplicity.
// The "streaming" refers to batch processing of the file content, not the upload itself.
func (s *Server) AddMultipleKeysAsyncStream(c *gin.Context) {
	logrus.Debug("AddMultipleKeysAsyncStream: Handler called")

	// Get group_id from query parameter first (most reliable for multipart requests)
	groupIDStr := c.Query("group_id")

	// If not in query, try to get from multipart form
	// Note: We must get form values before calling FormFile to avoid parsing issues
	if groupIDStr == "" {
		// For multipart/form-data, we need to access the raw multipart reader
		// to get form values without buffering the entire file
		if c.Request.MultipartForm == nil {
			// Parse only the form fields (not files) with a small memory limit
			// This allows us to read group_id without buffering the file
			err := c.Request.ParseMultipartForm(1 << 20) // 1MB limit for form fields only
			if err != nil {
				logrus.WithError(err).Warn("AddMultipleKeysAsyncStream: Failed to parse multipart form")
			}
		}
		if c.Request.MultipartForm != nil && c.Request.MultipartForm.Value != nil {
			if values, ok := c.Request.MultipartForm.Value["group_id"]; ok && len(values) > 0 {
				groupIDStr = values[0]
			}
		}
	}

	if groupIDStr == "" {
		logrus.Warn("AddMultipleKeysAsyncStream: group_id not provided in query or form")
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.group_id_required")
		return
	}

	groupID, err := strconv.Atoi(groupIDStr)
	if err != nil || groupID <= 0 {
		logrus.WithFields(logrus.Fields{
			"group_id_str": groupIDStr,
			"error":        err,
		}).Warn("AddMultipleKeysAsyncStream: Invalid group_id format")
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id_format")
		return
	}

	logrus.WithField("group_id", groupID).Debug("AddMultipleKeysAsyncStream: Parsed group_id")

	group, ok := s.findGroupByID(c, uint(groupID))
	if !ok {
		logrus.WithField("group_id", groupID).Warn("AddMultipleKeysAsyncStream: Group not found")
		return
	}

	logrus.WithFields(logrus.Fields{
		"group_id":   groupID,
		"group_name": group.Name,
	}).Debug("AddMultipleKeysAsyncStream: Group found, getting uploaded file")

	// Get uploaded file
	// Note: FormFile internally calls ParseMultipartForm, which reads and parses
	// the entire multipart stream before returning. While files >32MB are spilled
	// to temp files, the multipart structure must be fully parsed first.
	// This prevents true concurrent upload/processing but simplifies implementation.
	//
	// Do NOT defer file.Close() here because the file is passed to a goroutine.
	// The goroutine (via runStreamingImport) will be responsible for closing the file.
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		logrus.WithError(err).Error("AddMultipleKeysAsyncStream: Failed to get uploaded file")
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, "failed to get uploaded file"))
		return
	}

	logrus.WithFields(logrus.Fields{
		"group_id":  groupID,
		"filename":  header.Filename,
		"file_size": header.Size,
	}).Info("AddMultipleKeysAsyncStream: File received, starting streaming import")

	// Wrap file with cleanup reader to defer multipart temp file removal
	// until the async goroutine finishes processing.
	// Without this wrapper, net/http would automatically call MultipartForm.RemoveAll()
	// when the handler returns, deleting temp files while the goroutine is still reading.
	reader := io.Reader(file)
	if mf := c.Request.MultipartForm; mf != nil {
		reader = &multipartCleanupReader{File: file, cleanup: mf.RemoveAll}
		// Prevent net/http from auto-cleaning temp files on handler return
		c.Request.MultipartForm = nil
	}

	// Start streaming import task
	taskStatus, err := s.KeyImportService.StartStreamingImportTask(group, reader, header.Size)
	if err != nil {
		// Close reader (and cleanup temp files) if task start fails
		if rc, ok := reader.(io.Closer); ok {
			_ = rc.Close()
		}
		logrus.WithFields(logrus.Fields{
			"group_id": groupID,
			"error":    err,
		}).Error("AddMultipleKeysAsyncStream: Failed to start streaming import task")
		response.Error(c, app_errors.NewAPIError(app_errors.ErrTaskInProgress, err.Error()))
		return
	}

	logrus.WithFields(logrus.Fields{
		"group_id":       groupID,
		"group_name":     group.Name,
		"file_size":      header.Size,
		"estimated_keys": taskStatus.Total,
	}).Info("AddMultipleKeysAsyncStream: Streaming task started successfully")

	response.Success(c, taskStatus)
}

// ListKeysInGroup handles listing all keys within a specific group with pagination.
func (s *Server) ListKeysInGroup(c *gin.Context) {
	groupID, ok := validateGroupIDFromQuery(c)
	if !ok {
		return
	}

	group, ok := s.findGroupByID(c, groupID)
	if !ok {
		return
	}

	statusFilter := c.Query("status")
	if statusFilter != "" && statusFilter != models.KeyStatusActive && statusFilter != models.KeyStatusInvalid {
		response.ErrorI18nFromAPIError(c, app_errors.ErrValidation, "validation.invalid_status_filter")
		return
	}

	searchKeyword := c.Query("key_value")
	searchHash := ""
	if searchKeyword != "" {
		searchHash = s.EncryptionSvc.Hash(searchKeyword)
	}

	// Prepare cache key for potential fallback and storage
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", strconv.Itoa(response.DefaultPageSize)))
	cacheKey := s.KeyService.BuildPageCacheKey(groupID, statusFilter, searchHash, page, pageSize)

	// During heavy write tasks (import/delete), skip DB reads and return cached/empty to avoid lock contention.
	if s.shouldDegradeReadDuringTask(group.Name) {
		if cached, ok := s.KeyService.GetCachedPage(cacheKey); ok {
			response.Success(c, &response.PaginatedResponse{
				Items:      cached,
				Pagination: response.Pagination{Page: page, PageSize: pageSize, TotalItems: -1, TotalPages: -1},
			})
			return
		}
		response.Success(c, &response.PaginatedResponse{
			Items:      []models.APIKey{},
			Pagination: response.Pagination{Page: page, PageSize: pageSize, TotalItems: -1, TotalPages: -1},
		})
		return
	}

	query := s.KeyService.ListKeysInGroupQuery(groupID, statusFilter, searchHash)

	var keys []models.APIKey
	paginatedResult, err := response.Paginate(c, query, &keys)
	if err != nil {
		response.Error(c, app_errors.ParseDBError(err))
		return
	}

	// If data degraded (unknown totals + empty), try cache
	if len(keys) == 0 && paginatedResult.Pagination.TotalItems == -1 {
		if cached, ok := s.KeyService.GetCachedPage(cacheKey); ok {
			paginatedResult.Items = cached
			response.Success(c, paginatedResult)
			return
		}
	}

	// Decrypt all keys for display
	for i := range keys {
		decryptedValue, err := s.EncryptionSvc.Decrypt(keys[i].KeyValue)
		if err != nil {
			logrus.WithError(err).WithField("key_id", keys[i].ID).Error("Failed to decrypt key value for listing")
			keys[i].KeyValue = "failed-to-decrypt"
		} else {
			keys[i].KeyValue = decryptedValue
		}
	}
	paginatedResult.Items = keys

	// Cache the page for quick fallback under load (only small pages)
	if page == 1 && pageSize <= 50 {
		s.KeyService.SetCachedPage(cacheKey, keys)
	}

	response.Success(c, paginatedResult)
}

// DeleteMultipleKeys handles deleting keys from a text block within a specific group.
func (s *Server) DeleteMultipleKeys(c *gin.Context) {
	var req KeyTextRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	if _, ok := s.findGroupByID(c, req.GroupID); !ok {
		return
	}

	if !validateKeysText(c, req.KeysText) {
		return
	}

	result, err := s.KeyService.DeleteMultipleKeys(req.GroupID, req.KeysText)
	if err != nil {
		if strings.Contains(err.Error(), "batch size exceeds the limit") {
			response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation, err.Error()))
		} else if err.Error() == "no valid keys found in the input text" {
			response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation, err.Error()))
		} else {
			response.Error(c, app_errors.ParseDBError(err))
		}
		return
	}

	response.Success(c, result)
}

// DeleteMultipleKeysAsync handles deleting keys from a text block within a specific group using async task.
func (s *Server) DeleteMultipleKeysAsync(c *gin.Context) {
	var req KeyTextRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	group, ok := s.findGroupByID(c, req.GroupID)
	if !ok {
		return
	}

	if !validateKeysText(c, req.KeysText) {
		return
	}

	taskStatus, err := s.KeyDeleteService.StartDeleteTask(group, req.KeysText)
	if err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrTaskInProgress, err.Error()))
		return
	}

	response.Success(c, taskStatus)
}

// RestoreMultipleKeys handles restoring keys from a text block within a specific group.
func (s *Server) RestoreMultipleKeys(c *gin.Context) {
	var req KeyTextRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	if _, ok := s.findGroupByID(c, req.GroupID); !ok {
		return
	}

	if !validateKeysText(c, req.KeysText) {
		return
	}

	result, err := s.KeyService.RestoreMultipleKeys(req.GroupID, req.KeysText)
	if err != nil {
		if strings.Contains(err.Error(), "batch size exceeds the limit") {
			response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation, err.Error()))
		} else if err.Error() == "no valid keys found in the input text" {
			response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation, err.Error()))
		} else {
			response.Error(c, app_errors.ParseDBError(err))
		}
		return
	}

	response.Success(c, result)
}

// TestMultipleKeys handles a one-off validation test for multiple keys.
func (s *Server) TestMultipleKeys(c *gin.Context) {
	var req KeyTextRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	groupDB, ok := s.findGroupByID(c, req.GroupID)
	if !ok {
		return
	}

	// Check if group is enabled
	if !groupDB.Enabled {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.group_disabled")
		return
	}

	group, err := s.GroupManager.GetGroupByName(groupDB.Name)
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrResourceNotFound, "validation.group_not_found")
		return
	}

	if !validateKeysText(c, req.KeysText) {
		return
	}

	start := time.Now()
	results, err := s.KeyService.TestMultipleKeys(group, req.KeysText)
	duration := time.Since(start).Milliseconds()
	if err != nil {
		if strings.Contains(err.Error(), "batch size exceeds the limit") {
			response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation, err.Error()))
		} else if err.Error() == "no valid keys found in the input text" {
			response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation, err.Error()))
		} else {
			response.Error(c, app_errors.ParseDBError(err))
		}
		return
	}

	response.Success(c, gin.H{
		"results":        results,
		"total_duration": duration,
	})
}

// ValidateGroupKeys initiates a manual validation task for all keys in a group.
func (s *Server) ValidateGroupKeys(c *gin.Context) {
	var req ValidateGroupKeysRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	// Validate status if provided
	if req.Status != "" && req.Status != models.KeyStatusActive && req.Status != models.KeyStatusInvalid {
		response.ErrorI18nFromAPIError(c, app_errors.ErrValidation, "validation.invalid_status_value")
		return
	}

	groupDB, ok := s.findGroupByID(c, req.GroupID)
	if !ok {
		return
	}

	// Check if group is enabled
	if !groupDB.Enabled {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.group_disabled")
		return
	}

	group, err := s.GroupManager.GetGroupByName(groupDB.Name)
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrResourceNotFound, "validation.group_not_found")
		return
	}

	taskStatus, err := s.KeyManualValidationService.StartValidationTask(group, req.Status)
	if err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrTaskInProgress, err.Error()))
		return
	}

	response.Success(c, taskStatus)
}

// RestoreAllInvalidKeys sets the status of all 'inactive' keys in a group to 'active'.
func (s *Server) RestoreAllInvalidKeys(c *gin.Context) {
	var req GroupIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	if _, ok := s.findGroupByID(c, req.GroupID); !ok {
		return
	}

	rowsAffected, err := s.KeyService.RestoreAllInvalidKeys(req.GroupID)
	if err != nil {
		response.Error(c, app_errors.ParseDBError(err))
		return
	}

	response.SuccessI18n(c, "success.keys_restored", nil, map[string]any{"count": rowsAffected})
}

// ClearAllInvalidKeys deletes all 'inactive' keys from a group.
func (s *Server) ClearAllInvalidKeys(c *gin.Context) {
	var req GroupIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	if _, ok := s.findGroupByID(c, req.GroupID); !ok {
		return
	}

	rowsAffected, err := s.KeyService.ClearAllInvalidKeys(req.GroupID)
	if err != nil {
		response.Error(c, app_errors.ParseDBError(err))
		return
	}

	response.SuccessI18n(c, "success.invalid_keys_cleared", nil, map[string]any{"count": rowsAffected})
}

// ClearAllKeys deletes all keys from a group.
func (s *Server) ClearAllKeys(c *gin.Context) {
	var req GroupIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	if _, ok := s.findGroupByID(c, req.GroupID); !ok {
		return
	}

	rowsAffected, err := s.KeyService.ClearAllKeys(c.Request.Context(), req.GroupID)
	if err != nil {
		response.Error(c, app_errors.ParseDBError(err))
		return
	}

	response.SuccessI18n(c, "success.all_keys_cleared", nil, map[string]any{"count": rowsAffected})
}

// ExportKeys handles exporting keys to a text file.
func (s *Server) ExportKeys(c *gin.Context) {
	groupID, ok := validateGroupIDFromQuery(c)
	if !ok {
		return
	}

	statusFilter := c.Query("status")
	if statusFilter == "" {
		statusFilter = "all"
	}

	switch statusFilter {
	case "all", models.KeyStatusActive, models.KeyStatusInvalid:
	default:
		response.ErrorI18nFromAPIError(c, app_errors.ErrValidation, "validation.invalid_status_filter")
		return
	}

	group, ok := s.findGroupByID(c, groupID)
	if !ok {
		return
	}

	filename := fmt.Sprintf("keys-%s-%s.txt", group.Name, statusFilter)
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", "text/plain; charset=utf-8")

	if err := s.KeyService.StreamKeysToWriter(groupID, statusFilter, c.Writer); err != nil {
		log.Printf("Failed to stream keys: %v", err)
	}
}

// UpdateKeyNotesRequest defines the payload for updating a key's notes.
type UpdateKeyNotesRequest struct {
	Notes string `json:"notes"`
}

// UpdateKeyNotes handles updating the notes of a specific API key.
func (s *Server) UpdateKeyNotes(c *gin.Context) {
	keyIDStr := c.Param("id")
	keyID, err := strconv.Atoi(keyIDStr)
	if err != nil || keyID <= 0 {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, "invalid key ID format"))
		return
	}

	var req UpdateKeyNotesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	// Normalize and enforce length explicitly
	req.Notes = strings.TrimSpace(req.Notes)
	if utf8.RuneCountInString(req.Notes) > 255 {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation, "notes length must be <= 255 characters"))
		return
	}

	// Update notes (RowsAffected check below handles non-existent keys)
	result := s.DB.Model(&models.APIKey{}).Where("id = ?", uint(keyID)).Update("notes", req.Notes)
	if result.Error != nil {
		response.Error(c, app_errors.ParseDBError(result.Error))
		return
	}
	if result.RowsAffected == 0 {
		response.Error(c, app_errors.ErrResourceNotFound)
		return
	}

	response.Success(c, nil)
}
