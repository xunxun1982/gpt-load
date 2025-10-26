package proxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"
	"io"
	"net/http"

	"github.com/sirupsen/logrus"
)

func (ps *ProxyServer) applyParamOverrides(bodyBytes []byte, group *models.Group) ([]byte, error) {
	if len(group.ParamOverrides) == 0 || len(bodyBytes) == 0 {
		return bodyBytes, nil
	}

	var requestData map[string]any
	if err := json.Unmarshal(bodyBytes, &requestData); err != nil {
		logrus.Warnf("failed to unmarshal request body for param override, passing through: %v", err)
		return bodyBytes, nil
	}

	for key, value := range group.ParamOverrides {
		requestData[key] = value
	}

	return json.Marshal(requestData)
}

// applyModelMapping applies model name mapping based on group configuration.
// It modifies the request body to replace the model name if a mapping is configured.
func (ps *ProxyServer) applyModelMapping(bodyBytes []byte, group *models.Group) ([]byte, string, error) {
	originalModel := ""

	// Fast path: no model mapping configured
	if group.ModelMapping == "" && len(group.ModelMappingCache) == 0 {
		return bodyBytes, originalModel, nil
	}

	if len(bodyBytes) == 0 {
		return bodyBytes, originalModel, nil
	}

	var requestData map[string]any
	if err := json.Unmarshal(bodyBytes, &requestData); err != nil {
		logrus.WithError(err).Warn("Failed to unmarshal request body for model mapping, passing through")
		return bodyBytes, originalModel, nil
	}

	// Extract original model name
	modelValue, ok := requestData["model"]
	if !ok {
		return bodyBytes, originalModel, nil
	}

	originalModel, ok = modelValue.(string)
	if !ok || originalModel == "" {
		return bodyBytes, originalModel, nil
	}

	// Apply model mapping using cached map if available
	var mappedModel string
	var mapped bool
	var err error

	if len(group.ModelMappingCache) > 0 {
		mappedModel, mapped, err = utils.ApplyModelMappingFromMap(originalModel, group.ModelMappingCache)
	} else {
		mappedModel, mapped, err = utils.ApplyModelMapping(originalModel, group.ModelMapping)
	}

	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"group":          group.Name,
			"original_model": originalModel,
		}).Warn("Failed to apply model mapping, using original model")
		return bodyBytes, originalModel, nil
	}

	// If model was mapped, update the request body
	if mapped && mappedModel != originalModel {
		requestData["model"] = mappedModel
		modifiedBytes, err := json.Marshal(requestData)
		if err != nil {
			logrus.WithError(err).Warn("Failed to marshal request body after model mapping, using original")
			return bodyBytes, originalModel, nil
		}

		logrus.WithFields(logrus.Fields{
			"group":          group.Name,
			"original_model": originalModel,
			"mapped_model":   mappedModel,
		}).Debug("Applied model mapping")

		return modifiedBytes, originalModel, nil
	}

	return bodyBytes, originalModel, nil
}

// logUpstreamError provides a centralized way to log errors from upstream interactions.
func logUpstreamError(context string, err error) {
	if err == nil {
		return
	}
	if app_errors.IsIgnorableError(err) {
		logrus.Debugf("Ignorable upstream error in %s: %v", context, err)
	} else {
		logrus.Errorf("Upstream error in %s: %v", context, err)
	}
}

// handleGzipCompression checks for gzip encoding and decompresses the body if necessary.
// Note: This function name is kept for backward compatibility, but it now handles multiple compression formats.
func handleGzipCompression(resp *http.Response, bodyBytes []byte) []byte {
	encoding := resp.Header.Get("Content-Encoding")

	switch encoding {
	case "gzip":
		reader, gzipErr := gzip.NewReader(bytes.NewReader(bodyBytes))
		if gzipErr != nil {
			logrus.Warnf("Failed to create gzip reader: %v", gzipErr)
			return bodyBytes
		}
		defer reader.Close()

		decompressedBody, readAllErr := io.ReadAll(reader)
		if readAllErr != nil {
			logrus.Warnf("Failed to decompress gzip body: %v", readAllErr)
			return bodyBytes
		}
		return decompressedBody

	case "zstd":
		// For zstd, we need to use an external library
		// For now, log a warning and return the original body
		// The client will need to handle zstd decompression
		logrus.WithFields(logrus.Fields{
			"encoding": encoding,
			"size":     len(bodyBytes),
		}).Warn("zstd compression detected but not supported for response modification, returning compressed body")
		return bodyBytes

	default:
		return bodyBytes
	}
}
