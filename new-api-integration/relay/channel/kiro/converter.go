package kiro

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// ============================================================================
// Request Conversion: OpenAI/Claude → CodeWhisperer conversationState
// ============================================================================

// ConversationRequest is the top-level request sent to CodeWhisperer.
type ConversationRequest struct {
	ConversationState ConversationState `json:"conversationState"`
	ProfileArn        string            `json:"profileArn,omitempty"`
}

// ConversationState is the main state structure.
type ConversationState struct {
	ChatTriggerType     string            `json:"chatTriggerType"`
	ConversationID      string            `json:"conversationId"`
	CurrentMessage      CurrentMessage    `json:"currentMessage"`
	History             []HistoryEntry    `json:"history,omitempty"`
	AgentContinuationID string            `json:"agentContinuationId,omitempty"`
	AgentTaskType       string            `json:"agentTaskType,omitempty"`
}

// CurrentMessage wraps the current user input.
type CurrentMessage struct {
	UserInputMessage UserInputMessage `json:"userInputMessage"`
}

// UserInputMessage is the user's message to CodeWhisperer.
type UserInputMessage struct {
	Content                 string                   `json:"content"`
	ModelID                 string                   `json:"modelId"`
	Origin                  string                   `json:"origin"`
	Images                  []ImageContent           `json:"images,omitempty"`
	UserInputMessageContext *UserInputMessageContext  `json:"userInputMessageContext,omitempty"`
}

// UserInputMessageContext carries tool-related context.
type UserInputMessageContext struct {
	ToolResults          []ToolResult           `json:"toolResults,omitempty"`
	Tools                []ToolSpec             `json:"tools,omitempty"`
	SupplementalContexts []SupplementalContext  `json:"supplementalContexts,omitempty"`
}

// ToolResult is a result from a previous tool execution.
type ToolResult struct {
	ToolUseID string              `json:"toolUseId"`
	Content   []ToolResultContent `json:"content"`
	Status    string              `json:"status"`
}

// ToolResultContent holds the text content of a tool result.
type ToolResultContent struct {
	Text string `json:"text"`
}

// ToolSpec defines a tool available for the model to use.
type ToolSpec struct {
	ToolSpecification ToolSpecification `json:"toolSpecification"`
}

// ToolSpecification is the inner tool definition.
type ToolSpecification struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// SupplementalContext provides additional file context.
type SupplementalContext struct {
	FilePath string `json:"filePath"`
	Content  string `json:"content"`
}

// ImageContent represents an image in the request.
type ImageContent struct {
	Format string      `json:"format"`
	Source ImageSource `json:"source"`
}

// ImageSource holds the image data.
type ImageSource struct {
	Bytes string `json:"bytes"` // base64-encoded
}

// HistoryEntry is a single turn in the conversation history.
// Only one of UserInputMessage or AssistantResponseMessage should be set.
type HistoryEntry struct {
	UserInputMessage         *HistoryUserInput         `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *HistoryAssistantResponse `json:"assistantResponseMessage,omitempty"`
}

// HistoryUserInput is a user message in the history.
type HistoryUserInput struct {
	Content                 string                   `json:"content"`
	UserInputMessageContext *UserInputMessageContext  `json:"userInputMessageContext,omitempty"`
}

// HistoryAssistantResponse is an assistant message in the history.
type HistoryAssistantResponse struct {
	Content  string             `json:"content"`
	ToolUses []HistoryToolUse   `json:"toolUses,omitempty"`
}

// HistoryToolUse records a tool invocation in the history.
type HistoryToolUse struct {
	Input     json.RawMessage `json:"input"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"toolUseId"`
}

// ============================================================================
// OpenAI Request Conversion
// ============================================================================

// ConvertOpenAIToKiro converts an OpenAI ChatCompletion request body (as JSON)
// to a CodeWhisperer ConversationRequest.
//
// The requestJSON should be the raw GeneralOpenAIRequest JSON from new-api's dto.
// Model should already be mapped via MapModelName.
func ConvertOpenAIToKiro(requestJSON []byte, mappedModel string, enableThinking bool, profileArn string) (*ConversationRequest, error) {
	result := gjson.ParseBytes(requestJSON)
	messages := result.Get("messages")
	tools := result.Get("tools")
	systemPrompt := result.Get("system").String()

	if !messages.Exists() {
		return nil, fmt.Errorf("no messages in request")
	}

	// Build conversation history and current message
	var history []HistoryEntry
	var currentContent string
	var currentImages []ImageContent
	var toolResults []ToolResult
	var toolSpecs []ToolSpec

	// Extract system prompt from messages if not provided separately
	if systemPrompt == "" {
		for _, msg := range messages.Array() {
			if msg.Get("role").String() == "system" {
				content := msg.Get("content")
				if content.Type == gjson.String {
					systemPrompt = content.String()
				} else if content.IsArray() {
					for _, part := range content.Array() {
						if part.Get("type").String() == "text" {
							systemPrompt += part.Get("text").String()
						}
					}
				}
			}
		}
	}

	// Add thinking prompt injection if enabled
	if enableThinking {
		thinkingPrompt := "\n\nBefore replying, perform deep analysis inside <thinking>...</thinking> tags:\n- Break complex tasks into clear steps\n- Consider edge cases and potential issues\n- Ensure tool parameters fully meet requirements\nThen provide a well-thought-out response."
		systemPrompt += thinkingPrompt
	}

	// Convert tools to Kiro format
	if tools.Exists() {
		for _, tool := range tools.Array() {
			fn := tool.Get("function")
			if !fn.Exists() {
				continue
			}

			name := fn.Get("name").String()
			desc := fn.Get("description").String()
			params := fn.Get("parameters").Raw

			// Map CC tool names to Kiro tool names
			if mapping, ok := CCToKiroToolMapping[name]; ok {
				if mapping.Remove {
					continue // Skip unsupported tools
				}
				name = mapping.KiroTool
				if desc == "" {
					desc = mapping.Description
				}
			}

			spec := ToolSpec{
				ToolSpecification: ToolSpecification{
					Name:        name,
					Description: desc,
					InputSchema: json.RawMessage(params),
				},
			}
			toolSpecs = append(toolSpecs, spec)
		}
	}

	// Process messages into history + current
	msgArray := messages.Array()
	for i, msg := range msgArray {
		role := msg.Get("role").String()
		isLast := i == len(msgArray)-1

		switch role {
		case "system":
			// Already extracted above
			continue

		case "user":
			content := extractMessageContent(msg)
			images := extractImages(msg)

			if isLast {
				// This is the current message
				currentContent = content
				currentImages = images
			} else {
				// Add to history
				history = append(history, HistoryEntry{
					UserInputMessage: &HistoryUserInput{
						Content: content,
					},
				})
			}

		case "assistant":
			content := extractMessageContent(msg)
			var histToolUses []HistoryToolUse

			// Check for tool_calls
			toolCalls := msg.Get("tool_calls")
			if toolCalls.Exists() {
				for _, tc := range toolCalls.Array() {
					fn := tc.Get("function")
					toolName := fn.Get("name").String()
					argsStr := fn.Get("arguments").String()

					// Map tool name
					if mapping, ok := CCToKiroToolMapping[toolName]; ok && !mapping.Remove {
						toolName = mapping.KiroTool
						// Map parameters
						argsStr = mapToolParams(argsStr, mapping.ParamMap, mapping.FixedParams)
					}

					histToolUses = append(histToolUses, HistoryToolUse{
						Input:     json.RawMessage(argsStr),
						Name:      toolName,
						ToolUseID: tc.Get("id").String(),
					})
				}
			}

			history = append(history, HistoryEntry{
				AssistantResponseMessage: &HistoryAssistantResponse{
					Content:  content,
					ToolUses: histToolUses,
				},
			})

		case "tool":
			// Tool result message
			toolUseID := msg.Get("tool_call_id").String()
			content := extractMessageContent(msg)

			// Truncate if too long
			if len(content) > MaxToolOutputLength {
				content = content[:MaxToolOutputLength] + "\n... (truncated)"
			}

			toolResults = append(toolResults, ToolResult{
				ToolUseID: toolUseID,
				Content:   []ToolResultContent{{Text: content}},
				Status:    "success",
			})
		}
	}

	// If we have tool results but no current content, set default
	if len(toolResults) > 0 && currentContent == "" {
		currentContent = "Tool results provided."
	}

	// Prepend system prompt to current content
	if systemPrompt != "" && currentContent != "" {
		currentContent = systemPrompt + "\n\n" + currentContent
	} else if systemPrompt != "" {
		currentContent = systemPrompt
	}

	if currentContent == "" {
		return nil, fmt.Errorf("no user message content found")
	}

	// Build the request
	convReq := &ConversationRequest{
		ConversationState: ConversationState{
			ChatTriggerType: "MANUAL",
			ConversationID:  uuid.New().String(),
			CurrentMessage: CurrentMessage{
				UserInputMessage: UserInputMessage{
					Content: currentContent,
					ModelID: mappedModel,
					Origin:  "AI_EDITOR",
					Images:  currentImages,
				},
			},
			History: history,
		},
	}

	// Add tool context if present
	if len(toolResults) > 0 || len(toolSpecs) > 0 {
		ctx := &UserInputMessageContext{}
		if len(toolResults) > 0 {
			ctx.ToolResults = toolResults
		}
		if len(toolSpecs) > 0 {
			ctx.Tools = toolSpecs
		}
		convReq.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext = ctx
	}

	// Add profileArn for IdC auth
	if profileArn != "" {
		convReq.ProfileArn = profileArn
	}

	return convReq, nil
}

// ============================================================================
// Claude Request Conversion
// ============================================================================

// ConvertClaudeToKiro converts a Claude Messages API request body (as JSON)
// to a CodeWhisperer ConversationRequest.
func ConvertClaudeToKiro(requestJSON []byte, mappedModel string, enableThinking bool, profileArn string) (*ConversationRequest, error) {
	result := gjson.ParseBytes(requestJSON)
	messages := result.Get("messages")
	systemPrompt := result.Get("system").String()
	tools := result.Get("tools")

	if !messages.Exists() {
		return nil, fmt.Errorf("no messages in request")
	}

	// The system field in Claude can also be an array of content blocks
	if systemPrompt == "" && result.Get("system").IsArray() {
		for _, block := range result.Get("system").Array() {
			if block.Get("type").String() == "text" {
				systemPrompt += block.Get("text").String()
			}
		}
	}

	if enableThinking {
		thinkingPrompt := "\n\nBefore replying, perform deep analysis inside <thinking>...</thinking> tags:\n- Break complex tasks into clear steps\n- Consider edge cases and potential issues\n- Ensure tool parameters fully meet requirements\nThen provide a well-thought-out response."
		systemPrompt += thinkingPrompt
	}

	var history []HistoryEntry
	var currentContent string
	var currentImages []ImageContent
	var toolResults []ToolResult
	var toolSpecs []ToolSpec

	// Convert Claude tools
	if tools.Exists() {
		for _, tool := range tools.Array() {
			name := tool.Get("name").String()
			desc := tool.Get("description").String()
			schema := tool.Get("input_schema").Raw

			if mapping, ok := CCToKiroToolMapping[name]; ok {
				if mapping.Remove {
					continue
				}
				name = mapping.KiroTool
				if desc == "" {
					desc = mapping.Description
				}
			}

			toolSpecs = append(toolSpecs, ToolSpec{
				ToolSpecification: ToolSpecification{
					Name:        name,
					Description: desc,
					InputSchema: json.RawMessage(schema),
				},
			})
		}
	}

	// Process Claude messages
	msgArray := messages.Array()
	for i, msg := range msgArray {
		role := msg.Get("role").String()
		isLast := i == len(msgArray)-1

		switch role {
		case "user":
			content := msg.Get("content")
			var textContent string
			var images []ImageContent
			var msgToolResults []ToolResult

			if content.Type == gjson.String {
				textContent = content.String()
			} else if content.IsArray() {
				for _, block := range content.Array() {
					blockType := block.Get("type").String()
					switch blockType {
					case "text":
						textContent += block.Get("text").String()
					case "image":
						format := block.Get("source.media_type").String()
						if idx := strings.Index(format, "/"); idx >= 0 {
							format = format[idx+1:]
						}
						if format == "" {
							format = "jpeg"
						}
						images = append(images, ImageContent{
							Format: format,
							Source: ImageSource{
								Bytes: block.Get("source.data").String(),
							},
						})
					case "tool_result":
						toolUseID := block.Get("tool_use_id").String()
						resultContent := block.Get("content").String()
						if block.Get("content").IsArray() {
							for _, rc := range block.Get("content").Array() {
								if rc.Get("type").String() == "text" {
									resultContent += rc.Get("text").String()
								}
							}
						}
						if len(resultContent) > MaxToolOutputLength {
							resultContent = resultContent[:MaxToolOutputLength] + "\n... (truncated)"
						}
						status := "success"
						if block.Get("is_error").Bool() {
							status = "error"
						}
						msgToolResults = append(msgToolResults, ToolResult{
							ToolUseID: toolUseID,
							Content:   []ToolResultContent{{Text: resultContent}},
							Status:    status,
						})
					}
				}
			}

			if isLast {
				currentContent = textContent
				currentImages = images
				toolResults = append(toolResults, msgToolResults...)
			} else {
				entry := HistoryEntry{
					UserInputMessage: &HistoryUserInput{
						Content: textContent,
					},
				}
				if len(msgToolResults) > 0 {
					entry.UserInputMessage.UserInputMessageContext = &UserInputMessageContext{
						ToolResults: msgToolResults,
					}
				}
				history = append(history, entry)
			}

		case "assistant":
			content := msg.Get("content")
			var textContent string
			var histToolUses []HistoryToolUse

			if content.Type == gjson.String {
				textContent = content.String()
			} else if content.IsArray() {
				for _, block := range content.Array() {
					switch block.Get("type").String() {
					case "text":
						textContent += block.Get("text").String()
					case "tool_use":
						name := block.Get("name").String()
						inputRaw := block.Get("input").Raw

						if mapping, ok := CCToKiroToolMapping[name]; ok && !mapping.Remove {
							name = mapping.KiroTool
							inputRaw = mapToolParams(inputRaw, mapping.ParamMap, mapping.FixedParams)
						}

						histToolUses = append(histToolUses, HistoryToolUse{
							Input:     json.RawMessage(inputRaw),
							Name:      name,
							ToolUseID: block.Get("id").String(),
						})
					}
				}
			}

			history = append(history, HistoryEntry{
				AssistantResponseMessage: &HistoryAssistantResponse{
					Content:  textContent,
					ToolUses: histToolUses,
				},
			})
		}
	}

	// Handle tool results with no current content
	if len(toolResults) > 0 && currentContent == "" {
		currentContent = "Tool results provided."
	}

	if systemPrompt != "" && currentContent != "" {
		currentContent = systemPrompt + "\n\n" + currentContent
	} else if systemPrompt != "" {
		currentContent = systemPrompt
	}

	if currentContent == "" {
		return nil, fmt.Errorf("no user message content found")
	}

	convReq := &ConversationRequest{
		ConversationState: ConversationState{
			ChatTriggerType: "MANUAL",
			ConversationID:  uuid.New().String(),
			CurrentMessage: CurrentMessage{
				UserInputMessage: UserInputMessage{
					Content: currentContent,
					ModelID: mappedModel,
					Origin:  "AI_EDITOR",
					Images:  currentImages,
				},
			},
			History: history,
		},
	}

	if len(toolResults) > 0 || len(toolSpecs) > 0 {
		ctx := &UserInputMessageContext{}
		if len(toolResults) > 0 {
			ctx.ToolResults = toolResults
		}
		if len(toolSpecs) > 0 {
			ctx.Tools = toolSpecs
		}
		convReq.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext = ctx
	}

	if profileArn != "" {
		convReq.ProfileArn = profileArn
	}

	return convReq, nil
}

// ============================================================================
// Response Conversion: CodeWhisperer Events → OpenAI/Claude format
// ============================================================================

// KiroStreamState tracks the state of an ongoing stream conversion.
type KiroStreamState struct {
	MessageID        string
	Model            string
	InputTokens      int
	OutputTokens     int
	TotalContent     string
	ThinkingContent  string
	ToolCalls        []ToolCallResult
	CurrentToolCall  *ToolCallResult
	SeenToolUseIDs   map[string]bool
	TextBlockStarted bool
	ThinkingStarted  bool
	ContentBlockIdx  int
}

// ToolCallResult holds a completed tool call.
type ToolCallResult struct {
	ToolUseID string
	Name      string
	Input     string
}

// NewKiroStreamState creates a new stream state for conversion.
func NewKiroStreamState(model string) *KiroStreamState {
	return &KiroStreamState{
		MessageID:      "msg_" + uuid.New().String()[:24],
		Model:          model,
		SeenToolUseIDs: make(map[string]bool),
	}
}

// ============================================================================
// Helper Functions
// ============================================================================

// extractMessageContent extracts text content from an OpenAI message.
func extractMessageContent(msg gjson.Result) string {
	content := msg.Get("content")
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		var parts []string
		for _, part := range content.Array() {
			if part.Get("type").String() == "text" {
				parts = append(parts, part.Get("text").String())
			}
		}
		return strings.Join(parts, "")
	}
	return ""
}

// extractImages extracts image content from an OpenAI message.
func extractImages(msg gjson.Result) []ImageContent {
	content := msg.Get("content")
	if !content.IsArray() {
		return nil
	}

	var images []ImageContent
	for _, part := range content.Array() {
		if part.Get("type").String() == "image" || part.Get("type").String() == "image_url" {
			format := "jpeg"
			var data string

			if part.Get("source.media_type").Exists() {
				mt := part.Get("source.media_type").String()
				if idx := strings.Index(mt, "/"); idx >= 0 {
					format = mt[idx+1:]
				}
				data = part.Get("source.data").String()
			} else if part.Get("image_url.url").Exists() {
				url := part.Get("image_url.url").String()
				// Handle data URLs: data:image/jpeg;base64,...
				if strings.HasPrefix(url, "data:image/") {
					if idx := strings.Index(url, ";base64,"); idx >= 0 {
						format = url[11:idx] // extract format from data:image/FORMAT;base64,
						data = url[idx+8:]
					}
				}
			}

			if data != "" {
				images = append(images, ImageContent{
					Format: format,
					Source: ImageSource{Bytes: data},
				})
			}
		}
	}
	return images
}

// mapToolParams remaps parameter names from CC format to Kiro format.
func mapToolParams(inputJSON string, paramMap map[string]string, fixedParams map[string]string) string {
	if paramMap == nil && fixedParams == nil {
		return inputJSON
	}

	var input map[string]interface{}
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return inputJSON
	}

	mapped := make(map[string]interface{})

	// Map parameter names
	if paramMap != nil {
		for ccName, kiroName := range paramMap {
			if val, ok := input[ccName]; ok {
				mapped[kiroName] = val
			}
		}
		// Keep unmapped params as-is
		for key, val := range input {
			isMapped := false
			for ccName := range paramMap {
				if key == ccName {
					isMapped = true
					break
				}
			}
			if !isMapped {
				mapped[key] = val
			}
		}
	} else {
		mapped = input
	}

	// Add fixed params
	if fixedParams != nil {
		for key, val := range fixedParams {
			mapped[key] = val
		}
	}

	result, err := json.Marshal(mapped)
	if err != nil {
		return inputJSON
	}
	return string(result)
}
