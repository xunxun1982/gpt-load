package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"gpt-load/internal/models"
	"gpt-load/internal/utils"

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


// handleCodexForcedStreamResponse handles Codex streaming response and converts to non-streaming format.
// This is used when client requests non-streaming but Codex API requires streaming internally.
// Per CLIProxyAPI implementation: collect stream events until response.completed, then return non-streaming response.
func (ps *ProxyServer) handleCodexForcedStreamResponse(c *gin.Context, resp *http.Response) {
	logrus.WithFields(logrus.Fields{
		"content_type":     resp.Header.Get("Content-Type"),
		"content_encoding": resp.Header.Get("Content-Encoding"),
		"status_code":      resp.StatusCode,
	}).Debug("Codex forced stream: collecting stream response for non-stream client")

	// Collect stream events and build CodexResponse
	codexResp, err := collectCodexStreamToResponse(resp)
	if err != nil {
		logrus.WithError(err).Error("Codex forced stream: failed to collect stream response")
		// Do not expose internal error details to client for security
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"message": "Failed to collect stream response",
				"type":    "server_error",
			},
		})
		return
	}

	// Check for Codex error in response
	if codexResp.Error != nil {
		logrus.WithFields(logrus.Fields{
			"error_type":    codexResp.Error.Type,
			"error_message": utils.TruncateString(codexResp.Error.Message, 200),
		}).Warn("Codex forced stream: upstream returned error")
		c.JSON(resp.StatusCode, codexResp)
		return
	}

	logrus.WithFields(logrus.Fields{
		"response_id": codexResp.ID,
		"model":       codexResp.Model,
		"status":      codexResp.Status,
		"output_len":  len(codexResp.Output),
	}).Debug("Codex forced stream: converted stream to non-stream response")

	// Marshal and return response
	responseBody, err := json.Marshal(codexResp)
	if err != nil {
		logrus.WithError(err).Error("Codex forced stream: failed to marshal response")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"message": "Failed to marshal response",
				"type":    "server_error",
			},
		})
		return
	}

	// Store response for logging if enabled
	if shouldCaptureResponse(c) {
		if len(responseBody) > maxResponseCaptureBytes {
			c.Set("response_body", string(responseBody[:maxResponseCaptureBytes]))
		} else {
			c.Set("response_body", string(responseBody))
		}
	}

	// c.Data already sets Content-Type, no need for redundant c.Header call
	c.Data(resp.StatusCode, "application/json", responseBody)
}

// codexStreamResponse represents a Codex streaming response structure for collection.
type codexStreamResponse struct {
	ID        string                   `json:"id"`
	Object    string                   `json:"object"`
	CreatedAt int64                    `json:"created_at,omitempty"`
	Status    string                   `json:"status"`
	Model     string                   `json:"model"`
	Output    []codexStreamOutputItem  `json:"output"`
	Usage     *codexStreamUsage        `json:"usage,omitempty"`
	Error     *codexStreamError        `json:"error,omitempty"`
}

type codexStreamOutputItem struct {
	Type      string                    `json:"type"`
	ID        string                    `json:"id,omitempty"`
	Status    string                    `json:"status,omitempty"`
	Role      string                    `json:"role,omitempty"`
	Content   []codexStreamContentBlock `json:"content,omitempty"`
	CallID    string                    `json:"call_id,omitempty"`
	Name      string                    `json:"name,omitempty"`
	Arguments string                    `json:"arguments,omitempty"`
}

type codexStreamContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type codexStreamUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type codexStreamError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// codexStreamEvent represents a single event in Codex streaming response.
type codexStreamEvent struct {
	Type      string               `json:"type"`
	Response  *codexStreamResponse `json:"response,omitempty"`
	ItemID    string               `json:"item_id,omitempty"`
	OutputIdx int                  `json:"output_index,omitempty"`
	Delta     string               `json:"delta,omitempty"`
	Item      *codexStreamItem     `json:"item,omitempty"`
}

type codexStreamItem struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Status    string `json:"status,omitempty"`
}

// collectCodexStreamToResponse reads streaming response and builds a complete CodexResponse.
// Waits for response.completed event to get the final response state.
// Note: Caller is responsible for closing resp.Body (typically via defer in server.go).
// Note: Usage field is populated from response.completed event; fallback path has no usage data.
func collectCodexStreamToResponse(resp *http.Response) (*codexStreamResponse, error) {
	reader := bufio.NewReader(resp.Body)

	var finalResp *codexStreamResponse
	var currentEventType string
	var parseErrorCount int // Track JSON parse errors for debugging

	// Collectors for building response from stream events
	var outputItems []codexStreamOutputItem
	var currentTextContent strings.Builder
	var currentToolArgs strings.Builder // Use strings.Builder for efficient concatenation in loop
	var currentToolID, currentToolName string
	var model string
	var responseID string

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				logrus.Debug("Codex forced stream: stream ended with EOF")
				break
			}
			return nil, err
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Parse SSE format
		if strings.HasPrefix(line, "event: ") {
			currentEventType = strings.TrimPrefix(line, "event: ")
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}

			var event codexStreamEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				parseErrorCount++
				logrus.WithError(err).Debug("Codex forced stream: failed to parse stream event")
				continue
			}

			if currentEventType != "" && event.Type == "" {
				event.Type = currentEventType
			}
			currentEventType = ""

			// Process events to build response
			switch event.Type {
			case "response.created":
				if event.Response != nil {
					model = event.Response.Model
					responseID = event.Response.ID
				}

			case "response.output_item.added":
				if event.Item != nil && event.Item.Type == "function_call" {
					currentToolID = event.Item.CallID
					currentToolName = event.Item.Name
					currentToolArgs.Reset()
				}

			case "response.output_text.delta":
				if event.Delta != "" {
					currentTextContent.WriteString(event.Delta)
				}

			case "response.function_call_arguments.delta":
				if event.Delta != "" {
					currentToolArgs.WriteString(event.Delta)
				}

			case "response.output_item.done":
				if event.Item != nil {
					switch event.Item.Type {
					case "message":
						// Message complete - add text content if any
						if currentTextContent.Len() > 0 {
							outputItems = append(outputItems, codexStreamOutputItem{
								Type:   "message",
								Role:   "assistant",
								Status: "completed",
								Content: []codexStreamContentBlock{
									{Type: "output_text", Text: currentTextContent.String()},
								},
							})
							currentTextContent.Reset()
						}
					case "function_call":
						// Function call complete
						toolID := event.Item.CallID
						if toolID == "" {
							toolID = currentToolID
						}
						toolName := event.Item.Name
						if toolName == "" {
							toolName = currentToolName
						}
						args := event.Item.Arguments
						if args == "" {
							args = currentToolArgs.String()
						}
						outputItems = append(outputItems, codexStreamOutputItem{
							Type:      "function_call",
							CallID:    toolID,
							Name:      toolName,
							Arguments: args,
						})
						currentToolID = ""
						currentToolName = ""
						currentToolArgs.Reset()
					}
				}

			case "response.completed", "response.done":
				// Final response - use the complete response if available (includes Usage)
				if event.Response != nil {
					finalResp = event.Response
				}
			}
		}
	}

	// Log warning if multiple parse errors occurred (may indicate upstream issues)
	if parseErrorCount > 0 {
		logrus.WithField("error_count", parseErrorCount).Warn("Codex forced stream: multiple JSON parse errors during stream collection")
	}

	// If we didn't get a complete response from response.completed, build one from collected data
	if finalResp == nil {
		logrus.Warn("Codex forced stream: stream ended without response.completed event, building response from collected data")

		// Add any remaining text content
		if currentTextContent.Len() > 0 {
			outputItems = append(outputItems, codexStreamOutputItem{
				Type:   "message",
				Role:   "assistant",
				Status: "completed",
				Content: []codexStreamContentBlock{
					{Type: "output_text", Text: currentTextContent.String()},
				},
			})
		}

		// Log warning if partial function call data exists but not included
		// Note: We intentionally do NOT include incomplete function calls as they may cause
		// client-side parsing errors. The client should handle missing tool calls gracefully.
		if currentToolID != "" || currentToolName != "" || currentToolArgs.Len() > 0 {
			logrus.WithFields(logrus.Fields{
				"tool_id":   currentToolID,
				"tool_name": currentToolName,
				"args_len":  currentToolArgs.Len(),
			}).Warn("Codex forced stream: partial function call data lost due to stream interruption")
		}

		finalResp = &codexStreamResponse{
			ID:     responseID,
			Object: "response",
			Status: "completed",
			Model:  model,
			Output: outputItems,
			// Note: Usage is nil in fallback path as it's only available from response.completed event
		}
	}

	return finalResp, nil
}
