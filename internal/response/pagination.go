package response

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const (
	DefaultPageSize    = 15
	MaxPageSize        = 1000
	CountQueryTimeout  = 3 * time.Second // Increased for reliability during heavy operations
	DataQueryTimeout   = 3 * time.Second // Increased for reliability during heavy operations
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

// Paginate performs pagination on a GORM query and returns a standardized response.
// It takes a Gin context, a GORM query builder, and a destination slice for the results.
func Paginate(c *gin.Context, query *gorm.DB, dest any) (*PaginatedResponse, error) {
	// 1. Get page and page size from query parameters
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

// 2. Calculate offset now
offset := (page - 1) * pageSize

// 3. Run count and data queries concurrently, bound overall by data timeout
var (
	totalItems int64 = -1
	totalPages int   = -1
)

countDone := make(chan error, 1)
dataDone := make(chan error, 1)

// Clone queries to avoid shared state
countQuery := query.Session(&gorm.Session{NewDB: true})
dataQuery := query.Session(&gorm.Session{NewDB: true})

// Start COUNT
go func() {
	// Use a detached context to avoid cancellation from client
	countCtx, cancel := context.WithTimeout(context.Background(), CountQueryTimeout)
	defer cancel()
	err := countQuery.WithContext(countCtx).Count(&totalItems).Error
	if err != nil {
		countDone <- err
		return
	}
	countDone <- nil
}()

// Start DATA
go func() {
	// Use a detached context to avoid cancellation from client
	dataCtx, cancel := context.WithTimeout(context.Background(), DataQueryTimeout)
	defer cancel()
	err := dataQuery.WithContext(dataCtx).Limit(pageSize).Offset(offset).Find(dest).Error
	dataDone <- err
}()

// Wait primarily on data; do not block on count
var dataErr error
select {
case dataErr = <-dataDone:
	// proceed
case <-time.After(DataQueryTimeout + 100*time.Millisecond):
	dataErr = context.DeadlineExceeded
}

if dataErr != nil {
	// Degrade to empty items
	logrus.WithError(dataErr).Warn("Data page query timed out/failed, returning empty items")
	// Best-effort to compute totals if count finished
	select {
	case err := <-countDone:
		if err == nil && totalItems >= 0 {
			totalPages = int(math.Ceil(float64(totalItems) / float64(pageSize)))
		}
	default:
		// count not ready; keep unknown
	}
	paginatedData := &PaginatedResponse{
		Items: dest,
		Pagination: Pagination{
			Page:       page,
			PageSize:   pageSize,
			TotalItems: totalItems,
			TotalPages: totalPages,
		},
	}
	return paginatedData, nil
}

// If data succeeded, compute totalPages if count returned
select {
case err := <-countDone:
	if err == nil && totalItems >= 0 {
		totalPages = int(math.Ceil(float64(totalItems) / float64(pageSize)))
	}
default:
	// count still pending; leave unknown
}

// 5. Construct the paginated response
paginatedData := &PaginatedResponse{
	Items: dest,
	Pagination: Pagination{
		Page:       page,
		PageSize:   pageSize,
		TotalItems: totalItems,
		TotalPages: totalPages,
	},
}

return paginatedData, nil
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
