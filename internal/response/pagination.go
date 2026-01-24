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
	"gorm.io/gorm/logger"
)

const (
	DefaultPageSize   = 15
	MaxPageSize       = 1000
	CountQueryTimeout = 10 * time.Second // Extended timeout for COUNT on large datasets with indexes
	DataQueryTimeout  = 20 * time.Second // Data fetch timeout - increased to handle lock contention during batch operations
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
// Strategy: Execute data fetch and COUNT in true parallel, use Limit+1 to detect end and avoid COUNT when possible.
// For indexed queries (e.g., WHERE group_id = ?), COUNT should be fast using index scans.
// During bulk imports, queries may timeout - we return stale/partial data to keep UI responsive.
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

	// 2. Start COUNT query in parallel (true parallelism with data fetch)
	// Use channel to safely communicate COUNT result between goroutines
	type countResult struct {
		totalItems int64
		totalPages int
	}
	countDone := make(chan countResult, 1) // buffered to prevent goroutine leak

	// Use request context to enable cancellation when client disconnects
	countCtx, countCancel := context.WithTimeout(c.Request.Context(), CountQueryTimeout)
	defer countCancel()

	go func() {
		var total int64 = -1
		// Use a silent logger for COUNT to avoid noisy logs when the context is intentionally cancelled
		countQuery := query.Session(&gorm.Session{
			NewDB:  true,
			Logger: logger.Default.LogMode(logger.Silent),
		})
		err := countQuery.WithContext(countCtx).Count(&total).Error

		result := countResult{totalItems: -1, totalPages: -1}
		if err != nil {
			// Distinguish intentional cancellation from real errors
			switch {
			case err == context.DeadlineExceeded || countCtx.Err() == context.DeadlineExceeded:
				logrus.Warn("Pagination COUNT query timed out - this may indicate missing indexes or very large dataset")
			case err == context.Canceled || countCtx.Err() == context.Canceled:
				// Expected when we cancel COUNT on last-page inference; no warning needed
			default:
				logrus.WithError(err).Warn("Pagination COUNT query failed")
			}
		} else {
			result.totalItems = total
			if total >= 0 {
				result.totalPages = int(math.Ceil(float64(total) / float64(pageSize)))
			}
		}
		countDone <- result
	}()

	// 3. Fetch data with Limit+1 strategy to detect if there are more pages
	// Use request context to enable proper cancellation when client disconnects
	dataCtx, dataCancel := context.WithTimeout(c.Request.Context(), DataQueryTimeout)
	defer dataCancel()

	dataQuery := query.Session(&gorm.Session{NewDB: true})
	// Fetch one extra row to detect if there are more pages
	fetchLimit := pageSize + 1
	err = dataQuery.WithContext(dataCtx).Limit(fetchLimit).Offset(offset).Find(dest).Error
	if err != nil {
		logrus.WithError(err).Error("Pagination data query failed")
		// Cancel COUNT query if data fetch fails
		countCancel()
		// Return error to caller for proper 5xx handling
		return nil, err
	}

	// 4. Determine actual row count from fetched data
	// Use reflection to get slice length since dest is interface{}
	actualCount := getSliceLen(dest)
	hasMore := actualCount > pageSize

	// Trim the extra row if we fetched pageSize+1
	if hasMore {
		trimSliceToLen(dest, pageSize)
		actualCount = pageSize
	}

	// 5. Smart total calculation based on available information
	// Only infer totals from offset+actualCount when we're certain we're on a valid last page
	var totalItems int64
	var totalPages int

	// Avoid incorrect inference for out-of-range pages (page > 1 with 0 results)
	// or when hasMore=true (not last page, need COUNT)
	if !hasMore && !(page > 1 && actualCount == 0) {
		// Last page detected: calculate exact total based on current page window
		totalItems = int64(offset + actualCount)
		// Normalize empty dataset semantics: totalItems=0 yields totalPages=0
		if totalItems == 0 {
			totalPages = 0
		} else {
			totalPages = page
		}
		// Cancel COUNT query as we don't need it
		countCancel()
	} else {
		// Wait for COUNT result - goroutine will always send result (success or timeout)
		// Buffered channel ensures no goroutine leak even if we don't wait
		result := <-countDone
		totalItems = result.totalItems
		totalPages = result.totalPages
		if totalItems < 0 {
			logrus.WithFields(logrus.Fields{
				"page":     page,
				"pageSize": pageSize,
				"hasMore":  hasMore,
			}).Warn("COUNT unavailable - returning data with unknown totals")
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
// dest must be a pointer to a slice for this to work correctly
func trimSliceToLen(dest any, length int) {
	val := reflect.ValueOf(dest)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	// Guard against non-slice types and non-settable values to prevent panic
	if val.Kind() != reflect.Slice || !val.CanSet() {
		return
	}
	if val.Len() > length {
		val.Set(val.Slice(0, length))
	}
}
