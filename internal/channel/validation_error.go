package channel

import (
	"fmt"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/utils"
	"io"
	"net/http"
	"strings"
)

const maxValidationErrorBodySize int64 = 64 * 1024

func parseValidationErrorResponse(resp *http.Response) (string, error) {
	errorBody, err := io.ReadAll(io.LimitReader(resp.Body, maxValidationErrorBodySize+1))
	if err != nil {
		return "", err
	}
	if int64(len(errorBody)) > maxValidationErrorBodySize {
		errorBody = errorBody[:maxValidationErrorBodySize]
	}

	encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	errorBody, err = utils.DecompressResponseWithLimit(encoding, errorBody, maxValidationErrorBodySize)
	if err != nil {
		return "", err
	}

	return app_errors.ParseUpstreamError(errorBody), nil
}

func invalidValidationStatusError(resp *http.Response) error {
	parsedError, err := parseValidationErrorResponse(resp)
	if err != nil {
		return fmt.Errorf("key is invalid (status %d), but failed to read error body: %w", resp.StatusCode, err)
	}
	return fmt.Errorf("[status %d] %s", resp.StatusCode, parsedError)
}
