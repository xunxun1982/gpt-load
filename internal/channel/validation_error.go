package channel

import (
	"encoding/json"
	"fmt"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/utils"
	"io"
	"net/http"
	"strings"
)

const maxValidationErrorBodySize int64 = 64 * 1024
const maxValidationSuccessBodySize int64 = 64 * 1024

func parseValidationErrorResponse(resp *http.Response) (string, error) {
	parsed, err := parseValidationErrorResponseWithBody(resp)
	return parsed.message, err
}

type validationErrorResponse struct {
	body    string
	message string
}

func parseValidationErrorResponseWithBody(resp *http.Response) (validationErrorResponse, error) {
	reader := resp.Body
	if encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))); encoding != "" {
		decompressed, err := utils.NewDecompressReader(encoding, resp.Body)
		if err != nil {
			return validationErrorResponse{}, err
		}
		reader = decompressed
		defer reader.Close()
	}

	errorBody, err := io.ReadAll(io.LimitReader(reader, maxValidationErrorBodySize+1))
	if err != nil {
		return validationErrorResponse{}, err
	}
	if int64(len(errorBody)) > maxValidationErrorBodySize {
		return validationErrorResponse{}, utils.ErrDecompressedTooLarge
	}

	return validationErrorResponse{
		body:    string(errorBody),
		message: app_errors.ParseUpstreamError(errorBody),
	}, nil
}

func invalidValidationStatusError(resp *http.Response) error {
	parsedError, err := parseValidationErrorResponse(resp)
	if err != nil {
		return fmt.Errorf("key is invalid (status %d), but failed to read error body: %w", resp.StatusCode, err)
	}
	return fmt.Errorf("[status %d] %s", resp.StatusCode, parsedError)
}

func validateKeyResponseStatus(resp *http.Response) (bool, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, invalidValidationStatusError(resp)
	}
	body, err := parseValidationSuccessResponse(resp)
	if err != nil {
		return false, fmt.Errorf("validation response is not readable: %w", err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return false, fmt.Errorf("validation response is empty")
	}
	if !json.Valid(body) && !looksLikeSSEValidationResponse(body) {
		return false, fmt.Errorf("validation response is not valid JSON or SSE")
	}
	return true, nil
}

func parseValidationSuccessResponse(resp *http.Response) ([]byte, error) {
	reader := resp.Body
	if encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))); encoding != "" {
		decompressed, err := utils.NewDecompressReader(encoding, resp.Body)
		if err != nil {
			return nil, err
		}
		reader = decompressed
		defer reader.Close()
	}

	body, err := io.ReadAll(io.LimitReader(reader, maxValidationSuccessBodySize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxValidationSuccessBodySize {
		return nil, utils.ErrDecompressedTooLarge
	}
	if !app_errors.IsReadableUpstreamBody(body) {
		return nil, fmt.Errorf("unreadable binary body")
	}
	return body, nil
}

func looksLikeSSEValidationResponse(body []byte) bool {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			return true
		}
	}
	return false
}
