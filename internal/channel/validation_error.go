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
	reader := resp.Body
	if encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))); encoding != "" {
		decompressed, err := utils.NewDecompressReader(encoding, resp.Body)
		if err != nil {
			return "", err
		}
		reader = decompressed
		defer reader.Close()
	}

	errorBody, err := io.ReadAll(io.LimitReader(reader, maxValidationErrorBodySize+1))
	if err != nil {
		return "", err
	}
	if int64(len(errorBody)) > maxValidationErrorBodySize {
		return "", utils.ErrDecompressedTooLarge
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
