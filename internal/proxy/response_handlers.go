package proxy

import (
	"bytes"
	"gpt-load/internal/models"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// capturedResponseWriter wraps gin.ResponseWriter to capture response body
type capturedResponseWriter struct {
	gin.ResponseWriter
	body       *bytes.Buffer
	maxCapture int // Maximum bytes to capture (0 = no limit)
	captured   int // Bytes captured so far
}

// Write captures data while writing to underlying writer
func (w *capturedResponseWriter) Write(data []byte) (int, error) {
	// Write to client first
	n, err := w.ResponseWriter.Write(data)

	// Capture data if within limits
	if w.maxCapture == 0 || w.captured < w.maxCapture {
		toCapture := data[:n]
		if w.maxCapture > 0 && w.captured+n > w.maxCapture {
			toCapture = data[:w.maxCapture-w.captured]
		}
		w.body.Write(toCapture)
		w.captured += len(toCapture)
	}

	return n, err
}

// newCapturedWriter creates a new response writer that captures data
func newCapturedWriter(w gin.ResponseWriter, maxCapture int) *capturedResponseWriter {
	return &capturedResponseWriter{
		ResponseWriter: w,
		body:           bytes.NewBuffer(make([]byte, 0, 4096)),
		maxCapture:     maxCapture,
	}
}

func (ps *ProxyServer) handleStreamingResponse(c *gin.Context, resp *http.Response) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		logrus.Error("Streaming unsupported by the writer, falling back to normal response")
		ps.handleNormalResponse(c, resp)
		return
	}

	// Check if response body capturing is enabled
	shouldCapture := false
	if groupVal, exists := c.Get("group"); exists {
		if group, ok := groupVal.(*models.Group); ok {
			shouldCapture = group.EffectiveConfig.EnableRequestBodyLogging
		}
	}

	var responseCapture *bytes.Buffer
	if shouldCapture {
		responseCapture = bytes.NewBuffer(make([]byte, 0, 4096))
	}

	buf := make([]byte, 4*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := c.Writer.Write(buf[:n]); writeErr != nil {
				logUpstreamError("writing stream to client", writeErr)
				return
			}

			// Capture response data if enabled (up to 65KB)
			if responseCapture != nil && responseCapture.Len() < 65000 {
				toWrite := buf[:n]
				if responseCapture.Len()+n > 65000 {
					toWrite = buf[:65000-responseCapture.Len()]
				}
				responseCapture.Write(toWrite)
			}

			flusher.Flush()
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			logUpstreamError("reading from upstream", err)
			return
		}
	}

	// Store captured response in context for logging
	if responseCapture != nil && responseCapture.Len() > 0 {
		c.Set("response_body", responseCapture.String())
	}
}

func (ps *ProxyServer) handleNormalResponse(c *gin.Context, resp *http.Response) {
	// Check if response body capturing is enabled
	shouldCapture := false
	if groupVal, exists := c.Get("group"); exists {
		if group, ok := groupVal.(*models.Group); ok {
			shouldCapture = group.EffectiveConfig.EnableRequestBodyLogging
		}
	}

	if shouldCapture {
		// Read response body and capture it
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			logUpstreamError("reading response body", err)
			return
		}

		// Store captured response (truncate to 65KB)
		if len(body) > 65000 {
			c.Set("response_body", string(body[:65000]))
		} else {
			c.Set("response_body", string(body))
		}

		// Write to client
		if _, err := c.Writer.Write(body); err != nil {
			logUpstreamError("writing response body", err)
		}
	} else {
		// Fast path: direct copy without capturing
		if _, err := io.Copy(c.Writer, resp.Body); err != nil {
			logUpstreamError("copying response body", err)
		}
	}
}
