package proxy

import (
	"bytes"
	"gpt-load/internal/models"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// maxResponseCaptureBytes is the maximum size of response body to capture for logging
const maxResponseCaptureBytes = 65000

// shouldCaptureResponse checks if response body capturing is enabled for the request
func shouldCaptureResponse(c *gin.Context) bool {
	if groupVal, exists := c.Get("group"); exists {
		if group, ok := groupVal.(*models.Group); ok {
			return group.EffectiveConfig.EnableRequestBodyLogging
		}
	}
	return false
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
	shouldCapture := shouldCaptureResponse(c)

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

			// Capture response data if enabled (up to max capture limit)
			if responseCapture != nil && responseCapture.Len() < maxResponseCaptureBytes {
				toWrite := buf[:n]
				if responseCapture.Len()+n > maxResponseCaptureBytes {
					toWrite = buf[:maxResponseCaptureBytes-responseCapture.Len()]
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
	shouldCapture := shouldCaptureResponse(c)

	if shouldCapture {
		// Read response body and capture it
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			logUpstreamError("reading response body", err)
			return
		}

		// Store captured response (truncate to max capture limit)
		if len(body) > maxResponseCaptureBytes {
			c.Set("response_body", string(body[:maxResponseCaptureBytes]))
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
