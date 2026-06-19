package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
)

func init() {
	Register("openai", newOpenAIChannel)
}

type OpenAIChannel struct {
	*BaseChannel
}

func newOpenAIChannel(f *Factory, group *models.Group) (ChannelProxy, error) {
	base, err := f.newBaseChannel("openai", group)
	if err != nil {
		return nil, err
	}

	return &OpenAIChannel{
		BaseChannel: base,
	}, nil
}

// ModifyRequest sets the Authorization header for the OpenAI service.
func (ch *OpenAIChannel) ModifyRequest(req *http.Request, apiKey *models.APIKey, group *models.Group) {
	req.Header.Set("Authorization", "Bearer "+apiKey.KeyValue)
}

// ValidateKey checks if the given API key is valid by making a chat completion request.
func (ch *OpenAIChannel) ValidateKey(ctx context.Context, apiKey *models.APIKey, group *models.Group) (bool, error) {
	// Parse validation endpoint to extract path and query parameters
	endpointURL, err := url.Parse(ch.ValidationEndpoint)
	if err != nil {
		return false, fmt.Errorf("failed to parse validation endpoint: %w", err)
	}

	// Select upstream with dedicated client using the unified helper
	selection, err := ch.SelectValidationUpstream(group, endpointURL.Path, endpointURL.RawQuery)
	if err != nil {
		return false, fmt.Errorf("failed to select validation upstream: %w", err)
	}
	if selection == nil || selection.URL == "" {
		return false, fmt.Errorf("failed to select validation upstream: empty result")
	}
	reqURL := selection.URL

	// Use a minimal, low-cost payload for validation.
	payload := gin.H{
		"model": ch.TestModel,
		"messages": []gin.H{
			{"role": "user", "content": validationPromptForGroup(group)},
		},
	}
	if validationStreamEnabled(group) {
		payload["stream"] = true
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("failed to marshal validation payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, bytes.NewBuffer(body))
	if err != nil {
		return false, fmt.Errorf("failed to create validation request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey.KeyValue)
	req.Header.Set("Content-Type", "application/json")

	// Apply custom header rules if available
	if len(group.HeaderRuleList) > 0 {
		headerCtx := utils.NewHeaderVariableContext(group, apiKey)
		utils.ApplyHeaderRules(req, group.HeaderRuleList, headerCtx)
	}
	ApplySimulatedClientHeaders(req, group, validationStreamEnabled(group))

	client := selection.HTTPClient
	if client == nil {
		client = ch.HTTPClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to send validation request: %w", err)
	}
	defer resp.Body.Close()

	// Any 2xx status code indicates the key is valid.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil
	}

	return false, invalidValidationStatusError(resp)
}
