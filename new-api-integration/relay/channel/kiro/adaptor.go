// Package kiro implements a new-api channel adaptor for AWS CodeWhisperer (Kiro) API.
//
// This adaptor enables new-api to use Kiro OAuth credentials to access Claude models
// through AWS CodeWhisperer's generateAssistantResponse / SendMessageStreaming APIs.
//
// It supports:
//   - OpenAI-compatible chat completions → CodeWhisperer conversion
//   - Claude Messages API → CodeWhisperer conversion
//   - AWS Event Stream binary protocol parsing (streaming)
//   - OAuth token auto-refresh with debounce
//   - Tool call mapping (Claude Code → Kiro tools)
//   - Extended thinking via prompt injection
//   - Image/vision content support
//
// Usage in new-api:
//  1. Register ChannelTypeKiro in constant/channel.go
//  2. Register APITypeKiro in constant/api_type.go
//  3. Add `case constant.APITypeKiro: return &kiro.Adaptor{}` in relay/relay_adaptor.go
//  4. Channel key field stores JSON: {"accessToken":"...","refreshToken":"...","clientId":"...","clientSecret":"...","region":"us-east-1","authMethod":"IdC"}
package kiro

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"

	// These imports reference new-api packages. When integrating, adjust the module path
	// to match the actual new-api module (e.g., github.com/QuantumNous/new-api).
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

// Adaptor implements the channel.Adaptor interface for Kiro (AWS CodeWhisperer).
type Adaptor struct {
	info         *relaycommon.RelayInfo
	creds        *KiroCredentials
	tokenManager *TokenManager
	requestURL   string
	httpClient   *http.Client
}

// Init initializes the adaptor with relay info.
func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
	a.info = info
	a.httpClient = &http.Client{
		Timeout: AxiosTimeout,
	}
}

// GetRequestURL returns the CodeWhisperer API endpoint URL.
func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	// Parse credentials from channel key
	creds, err := ParseCredentials(info.ApiKey)
	if err != nil {
		return "", fmt.Errorf("invalid Kiro credentials: %w", err)
	}
	a.creds = creds
	a.tokenManager = NewTokenManager(creds)

	region := creds.Region
	if region == "" {
		region = DefaultRegion
	}

	// Use AmazonQ URL for amazonq models, standard URL otherwise
	model := info.UpstreamModelName
	if strings.HasPrefix(model, "amazonq") {
		a.requestURL = fmt.Sprintf(AmazonQURL, region)
	} else {
		a.requestURL = fmt.Sprintf(BaseURL, region)
	}

	return a.requestURL, nil
}

// SetupRequestHeader sets Kiro-specific headers on the outgoing request.
func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	// Get fresh access token (auto-refreshes if needed)
	token, err := a.tokenManager.GetAccessToken()
	if err != nil {
		return fmt.Errorf("failed to get access token: %w", err)
	}

	// Generate randomized device fingerprint
	macHash := generateRandomMACSHA256()
	kiroVersion := KiroVersion
	sdkVersion := "3.758.0"
	nodeVersion := fmt.Sprintf("v%d.%d.%d", 22+randInt(3), randInt(10), randInt(10))
	winVersion := fmt.Sprintf("10.0.%d", 19041+randInt(5000))

	userAgent := fmt.Sprintf(
		"aws-sdk-js/%s ua/2.1 os/Windows#%s lang/js md/nodejs#%s api/codewhispererstreaming#%s m/N,E KiroIDE-%s-%s",
		sdkVersion, winVersion, nodeVersion, sdkVersion, kiroVersion, macHash,
	)
	amzUserAgent := fmt.Sprintf("aws-sdk-js/%s KiroIDE-%s-%s", sdkVersion, kiroVersion, macHash)

	req.Set("Content-Type", "application/json")
	req.Set("Accept", "application/json")
	req.Set("Authorization", "Bearer "+token)
	req.Set("amz-sdk-invocation-id", uuid.New().String())
	req.Set("amz-sdk-request", "attempt=1; max=3")
	req.Set("x-amzn-kiro-agent-mode", "vibe")
	req.Set("x-amz-user-agent", amzUserAgent)
	req.Set("user-agent", userAgent)

	return nil
}

// ConvertOpenAIRequest converts an OpenAI GeneralOpenAIRequest to CodeWhisperer format.
func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	// Serialize the OpenAI request to JSON for conversion
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal OpenAI request: %w", err)
	}

	// Map model name
	model := MapModelName(info.UpstreamModelName)

	// Check if thinking is enabled
	enableThinking := false
	if request.StreamOptions != nil {
		enableThinking = true // Heuristic: stream options often accompany thinking requests
	}

	// Check for explicit thinking configuration in the request JSON
	parsed := gjson.ParseBytes(requestJSON)
	if parsed.Get("thinking.type").String() == "enabled" || parsed.Get("extended_thinking").Bool() {
		enableThinking = true
	}

	profileArn := ""
	if a.creds != nil {
		profileArn = a.creds.ProfileArn
	}

	convReq, err := ConvertOpenAIToKiro(requestJSON, model, enableThinking, profileArn)
	if err != nil {
		return nil, fmt.Errorf("failed to convert OpenAI request to Kiro: %w", err)
	}

	return convReq, nil
}

// ConvertClaudeRequest converts a Claude Messages request to CodeWhisperer format.
func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Claude request: %w", err)
	}

	model := MapModelName(info.UpstreamModelName)

	// Check for thinking in Claude request
	enableThinking := false
	if strings.Contains(string(requestJSON), `"thinking"`) {
		enableThinking = true
	}

	profileArn := ""
	if a.creds != nil {
		profileArn = a.creds.ProfileArn
	}

	convReq, err := ConvertClaudeToKiro(requestJSON, model, enableThinking, profileArn)
	if err != nil {
		return nil, fmt.Errorf("failed to convert Claude request to Kiro: %w", err)
	}

	return convReq, nil
}

// ConvertRerankRequest — Kiro does not support reranking.
func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, fmt.Errorf("Kiro channel does not support rerank requests")
}

// ConvertEmbeddingRequest — Kiro does not support embeddings.
func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return nil, fmt.Errorf("Kiro channel does not support embedding requests")
}

// ConvertAudioRequest — Kiro does not support audio.
func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, fmt.Errorf("Kiro channel does not support audio requests")
}

// ConvertImageRequest — Kiro does not support image generation.
func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	return nil, fmt.Errorf("Kiro channel does not support image generation requests")
}

// ConvertOpenAIResponsesRequest converts an OpenAI Responses request to CodeWhisperer format.
func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	// Convert OpenAI Responses to standard messages format first, then to Kiro
	// For now, use the same conversion path as regular chat completions
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Responses request: %w", err)
	}

	model := MapModelName(info.UpstreamModelName)
	profileArn := ""
	if a.creds != nil {
		profileArn = a.creds.ProfileArn
	}

	convReq, err := ConvertOpenAIToKiro(requestJSON, model, false, profileArn)
	if err != nil {
		return nil, fmt.Errorf("failed to convert Responses request to Kiro: %w", err)
	}

	return convReq, nil
}

// ConvertGeminiRequest converts a Gemini request to CodeWhisperer format.
func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	// Convert Gemini format to OpenAI-compatible first, then to Kiro
	// This is a simplified conversion — full Gemini support would need more work
	return nil, fmt.Errorf("Gemini-to-Kiro conversion not yet implemented; use OpenAI or Claude format")
}

// DoRequest sends the converted request to CodeWhisperer.
func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	// Determine timeout based on model
	timeout := AxiosTimeout
	if IsSlowModel(info.UpstreamModelName) {
		timeout = time.Duration(float64(timeout) * SlowModelTimeoutMultiplier)
	}

	client := &http.Client{Timeout: timeout}

	req, err := http.NewRequest("POST", a.requestURL, requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kiro request: %w", err)
	}

	// Copy headers from the relay info
	for key, values := range c.Request.Header {
		for _, value := range values {
			// Only copy Kiro-specific headers we set
			if strings.HasPrefix(strings.ToLower(key), "x-amz") ||
				strings.ToLower(key) == "authorization" ||
				strings.ToLower(key) == "user-agent" ||
				strings.ToLower(key) == "content-type" ||
				strings.ToLower(key) == "accept" {
				req.Header.Set(key, value)
			}
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Kiro API request failed: %w", err)
	}

	return resp, nil
}

// DoResponse processes the CodeWhisperer response and converts it back to OpenAI/Claude format.
// For streaming responses, it parses the AWS Event Stream binary protocol.
// For non-streaming, it reads the full response and converts.
func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	if resp == nil {
		return nil, types.NewError(fmt.Errorf("nil response from Kiro"), types.ErrorCodeDoRequestFailed)
	}
	defer resp.Body.Close()

	model := info.UpstreamModelName
	isStream := info.IsStream

	if isStream {
		return a.doStreamResponse(c, resp, info, model)
	}
	return a.doNonStreamResponse(c, resp, info, model)
}

// doStreamResponse handles streaming responses by parsing AWS Event Stream format
// and emitting SSE events in OpenAI format.
func (a *Adaptor) doStreamResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, model string) (usage any, apiErr *types.NewAPIError) {
	state := NewKiroStreamState(model)
	w := c.Writer

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Flush helper
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, types.NewError(fmt.Errorf("streaming not supported"), types.ErrorCodeDoRequestFailed)
	}

	var pendingBuffer []byte
	buf := make([]byte, 32*1024) // 32KB read buffer

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if len(pendingBuffer) > 0 {
				pendingBuffer = append(pendingBuffer, buf[:n]...)
			} else {
				pendingBuffer = make([]byte, n)
				copy(pendingBuffer, buf[:n])
			}

			result := ParseEventStreamBuffer(pendingBuffer)
			pendingBuffer = result.Remaining

			for _, event := range result.Events {
				sseData := a.convertEventToSSE(event, state, info)
				for _, line := range sseData {
					if line != "" {
						fmt.Fprintf(w, "data: %s\n\n", line)
						flusher.Flush()
					}
				}
			}
		}

		if readErr != nil {
			if readErr != io.EOF {
				// Stream error
				return nil, types.NewError(fmt.Errorf("stream read error: %w", readErr), types.ErrorCodeDoRequestFailed)
			}
			break
		}
	}

	// Send final stop event
	stopData := a.buildStopSSE(state, info)
	for _, line := range stopData {
		if line != "" {
			fmt.Fprintf(w, "data: %s\n\n", line)
		}
	}
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()

	return &dto.Usage{
		PromptTokens:     state.InputTokens,
		CompletionTokens: state.OutputTokens,
		TotalTokens:      state.InputTokens + state.OutputTokens,
	}, nil
}

// doNonStreamResponse handles non-streaming responses.
func (a *Adaptor) doNonStreamResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, model string) (usage any, apiErr *types.NewAPIError) {
	// Read the full response body (Event Stream format)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewError(fmt.Errorf("failed to read Kiro response: %w", err), types.ErrorCodeDoRequestFailed)
	}

	// Parse all events from the buffer
	result := ParseEventStreamBuffer(body)

	state := NewKiroStreamState(model)
	var totalContent string
	var thinkingContent string

	for _, event := range result.Events {
		switch e := event.Data.(type) {
		case ContentEvent:
			totalContent += e.Content
		case ThinkingEvent:
			thinkingContent += e.Thinking
		case MeteringEvent:
			state.OutputTokens = e.Usage
		case ToolUseEvent:
			if !state.SeenToolUseIDs[e.ToolUseID] {
				state.SeenToolUseIDs[e.ToolUseID] = true
				state.ToolCalls = append(state.ToolCalls, ToolCallResult{
					ToolUseID: e.ToolUseID,
					Name:      e.Name,
					Input:     e.Input,
				})
			} else if state.CurrentToolCall != nil {
				state.CurrentToolCall.Input += e.Input
			}
		}
	}

	// Build response in OpenAI format
	response := buildOpenAIResponseJSON(state, totalContent, thinkingContent, model)

	c.JSON(http.StatusOK, response)

	return &dto.Usage{
		PromptTokens:     state.InputTokens,
		CompletionTokens: state.OutputTokens,
		TotalTokens:      state.InputTokens + state.OutputTokens,
	}, nil
}

// convertEventToSSE converts a single Kiro event to SSE data lines.
func (a *Adaptor) convertEventToSSE(event EventStreamEvent, state *KiroStreamState, info *relaycommon.RelayInfo) []string {
	var lines []string

	switch e := event.Data.(type) {
	case ContentEvent:
		state.TotalContent += e.Content
		if !state.TextBlockStarted {
			state.TextBlockStarted = true
		}
		// OpenAI streaming chunk
		chunk := dto.ChatCompletionsStreamResponse{
			ID:      "chatcmpl-" + uuid.New().String()[:24],
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   state.Model,
			Choices: []dto.ChatCompletionsStreamResponseChoice{
				{
					Index: 0,
					Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
						Content: &e.Content,
					},
				},
			},
		}
		data, _ := json.Marshal(chunk)
		lines = append(lines, string(data))

	case ThinkingEvent:
		state.ThinkingContent += e.Thinking
		// Note: OpenAI doesn't have a standard thinking event.
		// In the Claude-compatible format, this would be content_block_delta with thinking_delta.
		// For OpenAI format, we include it as reasoning_content in the delta.

	case MeteringEvent:
		state.OutputTokens = e.Usage

	case ToolUseEvent:
		// Handle tool calls in streaming
		if !state.SeenToolUseIDs[e.ToolUseID] {
			state.SeenToolUseIDs[e.ToolUseID] = true
			state.ToolCalls = append(state.ToolCalls, ToolCallResult{
				ToolUseID: e.ToolUseID,
				Name:      e.Name,
			})
			state.CurrentToolCall = &state.ToolCalls[len(state.ToolCalls)-1]
		}
		if state.CurrentToolCall != nil && e.Input != "" {
			state.CurrentToolCall.Input += e.Input
		}
	}

	return lines
}

// buildStopSSE builds the final SSE events for stream completion.
func (a *Adaptor) buildStopSSE(state *KiroStreamState, info *relaycommon.RelayInfo) []string {
	finishReason := "stop"
	if len(state.ToolCalls) > 0 {
		finishReason = "tool_calls"
	}

	chunk := dto.ChatCompletionsStreamResponse{
		ID:      "chatcmpl-" + uuid.New().String()[:24],
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   state.Model,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Index:        0,
				FinishReason: &finishReason,
			},
		},
		Usage: &dto.Usage{
			PromptTokens:     state.InputTokens,
			CompletionTokens: state.OutputTokens,
			TotalTokens:      state.InputTokens + state.OutputTokens,
		},
	}

	data, _ := json.Marshal(chunk)
	return []string{string(data)}
}

// buildOpenAIResponseJSON builds a complete OpenAI ChatCompletion response as a map.
// We use a map instead of dto types to avoid complex Message content handling.
func buildOpenAIResponseJSON(state *KiroStreamState, content, thinkingContent, model string) map[string]interface{} {
	finishReason := "stop"
	if len(state.ToolCalls) > 0 {
		finishReason = "tool_calls"
	}

	message := map[string]interface{}{
		"role":    "assistant",
		"content": content,
	}

	if thinkingContent != "" {
		message["reasoning_content"] = thinkingContent
	}

	// Add tool calls if present
	if len(state.ToolCalls) > 0 {
		var toolCalls []map[string]interface{}
		for i, tc := range state.ToolCalls {
			toolCalls = append(toolCalls, map[string]interface{}{
				"index": i,
				"id":    tc.ToolUseID,
				"type":  "function",
				"function": map[string]interface{}{
					"name":      tc.Name,
					"arguments": tc.Input,
				},
			})
		}
		message["tool_calls"] = toolCalls
	}

	return map[string]interface{}{
		"id":      "chatcmpl-" + uuid.New().String()[:24],
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     state.InputTokens,
			"completion_tokens": state.OutputTokens,
			"total_tokens":      state.InputTokens + state.OutputTokens,
		},
	}
}

// GetModelList returns the list of models supported by the Kiro channel.
func (a *Adaptor) GetModelList() []string {
	return KiroModels
}

// GetChannelName returns the display name for this channel type.
func (a *Adaptor) GetChannelName() string {
	return "Kiro"
}

// ============================================================================
// Utility Functions
// ============================================================================

// generateRandomMACSHA256 generates a random MAC-like SHA256 hash for device fingerprinting.
func generateRandomMACSHA256() string {
	mac := make([]byte, 6)
	_, _ = rand.Read(mac)
	macStr := fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", mac[0], mac[1], mac[2], mac[3], mac[4], mac[5])
	hash := sha256.Sum256([]byte(macStr))
	return hex.EncodeToString(hash[:])[:16]
}

// randInt returns a random integer in [0, max).
func randInt(max int) int {
	b := make([]byte, 1)
	_, _ = rand.Read(b)
	return int(b[0]) % max
}

// SerializeRequest marshals a ConversationRequest to an io.Reader for DoRequest.
func SerializeRequest(req *ConversationRequest) (io.Reader, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}
