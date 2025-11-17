package response

import (
	"context"
	"math"
	"reflect"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const (
	DefaultPageSize    = 15
	MaxPageSize        = 1000
	CountQueryTimeout  = 5 * time.Second // Extended timeout for COUNT on large datasets with indexes
	DataQueryTimeout   = 3 * time.Second // Data fetch timeout
)

// Pagination represents the pagination details in a response.
type Pagination struct {
	Page       int   `json:"page"`
	PageSize   int   `json:"page_size"`
	TotalItems int64 `json:"total_items"`
	TotalPages int   `json:"total_pages"`
}

// PaginatedResponse is the standard structure for all paginated API responses.
type PaginatedResponse struct {
	Items      any        `json:"items"`
	Pagination Pagination `json:"pagination"`
}

// Paginate performs optimized pagination on a GORM query and returns a standardized response.
// Strategy: Fetch data first (Limit+1 to detect end), then COUNT in parallel with intelligent fallback.
// For indexed queries (e.g., WHERE group_id = ?), COUNT should be fast using index scans.
func Paginate(c *gin.Context, query *gorm.DB, dest any) (*PaginatedResponse, error) {
	// 1. Parse pagination parameters from query string
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 {
		page = 1
	}

	pageSize, err := strconv.Atoi(c.DefaultQuery("page_size", strconv.Itoa(DefaultPageSize)))
	if err != nil || pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	if pageSize > MaxPageSize {
		pageSize = MaxPageSize
	}

	offset := (page - 1) * pageSize

	// 2. Fetch data with Limit+1 strategy to detect if there are more pages
	// This allows us to skip COUNT for last page and provides faster initial response
	dataCtx, dataCancel := context.WithTimeout(context.Background(), DataQueryTimeout)
	defer dataCancel()

	dataQuery := query.Session(&gorm.Session{NewDB: true})
	// Fetch one extra row to detect if there are more pages
	fetchLimit := pageSize + 1
	err = dataQuery.WithContext(dataCtx).Limit(fetchLimit).Offset(offset).Find(dest).Error
	if err != nil {
		logrus.WithError(err).Warn("Pagination data query failed")
		// Return empty result with unknown totals on data fetch failure
		return &PaginatedResponse{
			Items: dest,
			Pagination: Pagination{
				Page:       page,
				PageSize:   pageSize,
				TotalItems: -1,
				TotalPages: -1,
			},
		}, nil
	}

	// 3. Determine actual row count from fetched data
	// Use reflection to get slice length since dest is interface{}
	actualCount := getSliceLen(dest)
	hasMore := actualCount > pageSize

	// Trim the extra row if we fetched pageSize+1
	if hasMore {
		trimSliceToLen(dest, pageSize)
		actualCount = pageSize
	}

	// 4. Start parallel COUNT query for accurate totals
	// For indexed queries (group_id, status, etc.), this should be fast
	var totalItems int64 = -1
	var totalPages int = -1
	countDone := make(chan struct{})

	go func() {
		defer close(countDone)
		countCtx, countCancel := context.WithTimeout(context.Background(), CountQueryTimeout)
		defer countCancel()

		countQuery := query.Session(&gorm.Session{NewDB: true})
		err := countQuery.WithContext(countCtx).Count(&totalItems).Error
		if err != nil {
			if context.DeadlineExceeded == err || countCtx.Err() == context.DeadlineExceeded {
				logrus.Warn("Pagination COUNT query timed out - this may indicate missing indexes or very large dataset")
			} else {
				logrus.WithError(err).Warn("Pagination COUNT query failed")
			}
			totalItems = -1 // Mark as unknown
		}
	}()

	// 5. Smart total calculation based on available information
	// If we're on the last page (no more data), we can calculate exact total without waiting for COUNT
	if !hasMore && actualCount < pageSize {
		// Last page detected: calculate exact total
		totalItems = int64(offset + actualCount)
		totalPages = page
		// Cancel COUNT query as we don't need it
		dataCancel()
	} else if !hasMore && actualCount == pageSize && page == 1 {
		// Special case: exactly one page of data
		totalItems = int64(pageSize)
		totalPages = 1
	} else {
		// Wait for COUNT with timeout to avoid blocking UI
		select {
		case <-countDone:
			// COUNT completed successfully or failed
			if totalItems >= 0 {
				totalPages = int(math.Ceil(float64(totalItems) / float64(pageSize)))
			} else {
				// COUNT failed/timed out: estimate minimum pages based on current page
				// We know there are at least (page) pages since we fetched data
				totalItems = -1
				totalPages = -1
				logrus.WithFields(logrus.Fields{
					"page":     page,
					"pageSize": pageSize,
					"hasMore":  hasMore,
				}).Warn("COUNT unavailable - returning data with unknown totals")
			}
		case <-time.After(CountQueryTimeout + 200*time.Millisecond):
			// COUNT still running after extended timeout - return data with unknown totals
			totalItems = -1
			totalPages = -1
			logrus.Warn("COUNT query exceeded maximum wait time - returning data with unknown totals")
		}
	}

	// 6. Construct and return paginated response
	return &PaginatedResponse{
		Items: dest,
		Pagination: Pagination{
			Page:       page,
			PageSize:   pageSize,
			TotalItems: totalItems,
			TotalPages: totalPages,
		},
	}, nil
}

// getSliceLen returns the length of a slice using reflection
// Returns 0 if dest is not a slice
func getSliceLen(dest any) int {
	val := reflect.ValueOf(dest)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	if val.Kind() != reflect.Slice {
		return 0
	}
	return val.Len()
}

// trimSliceToLen trims a slice to the specified length using reflection
func trimSliceToLen(dest any, length int) {
	val := reflect.ValueOf(dest)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	if val.Kind() != reflect.Slice {
		return
	}
	if val.Len() > length {
		val.Set(val.Slice(0, length))
	}
}

// countWithTimeout performs a COUNT query with a timeout to prevent blocking during heavy import operations
func countWithTimeout(parentCtx context.Context, query *gorm.DB, count *int64) error {
	// Create a context with timeout
	ctx, cancel := context.WithTimeout(parentCtx, CountQueryTimeout)
	defer cancel()

	// Create a channel to receive the result
	type countResult struct {
		err error
	}
	resultChan := make(chan countResult, 1)

	// Run the count query in a goroutine
	go func() {
		// Use WithContext to make the query cancellable
		err := query.WithContext(ctx).Count(count).Error
		resultChan <- countResult{err: err}
	}()

	// Wait for either the query to complete or the timeout
	select {
	case <-ctx.Done():
		// Timeout occurred
		return ctx.Err()
	case result := <-resultChan:
		// Query completed
		return result.err
	}
}
