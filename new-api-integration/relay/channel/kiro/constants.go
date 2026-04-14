package kiro

import "time"

// Kiro API endpoint URL templates ({{region}} is replaced at runtime)
const (
	RefreshURL      = "https://prod.%s.auth.desktop.kiro.dev/refreshToken"
	RefreshIDCURL   = "https://oidc.%s.amazonaws.com/token"
	DeviceAuthURL   = "https://oidc.%s.amazonaws.com/device_authorization"
	RegisterURL     = "https://oidc.%s.amazonaws.com/client/register"
	BaseURL         = "https://codewhisperer.%s.amazonaws.com/generateAssistantResponse"
	AmazonQURL      = "https://codewhisperer.%s.amazonaws.com/SendMessageStreaming"
	UsageLimitsURL  = "https://q.%s.amazonaws.com/getUsageLimits"
	DefaultRegion   = "us-east-1"
	DefaultModelID  = "claude-sonnet-4-20250514"
	KiroVersion     = "0.7.45"
	KiroUserAgent   = "KiroIDE"
	AuthMethodIDC   = "IdC"
	AuthMethodSocial = "social"
)

// Timeouts and limits
const (
	AxiosTimeout               = 120 * time.Second
	ExpireWindowDuration       = 5 * time.Minute
	RefreshDebounceDuration    = 30 * time.Second
	MaxContextTokens           = 200000
	AutoSummarizeThreshold     = 0.80
	MaxToolOutputLength        = 64000
	SlowModelTimeoutMultiplier = 3.0
	FirstTokenTimeout          = 120 * time.Second
	StreamReadTimeout          = 300 * time.Second
	DeviceGrantType            = "urn:ietf:params:oauth:grant-type:device_code"
)

// OAuth client registration scopes
var OAuthScopes = []string{
	"codewhisperer:completions",
	"codewhisperer:analysis",
	"codewhisperer:conversations",
	"codewhisperer:transformations",
	"codewhisperer:taskassist",
}

// FullModelMapping maps standard Anthropic model IDs to AWS CodeWhisperer model IDs.
var FullModelMapping = map[string]string{
	// Opus 4.5 mappings (AWS uses dot format)
	"claude-opus-4-5":          "claude-opus-4.5",
	"claude-opus-4-5-20251101": "claude-opus-4.5",
	"claude-opus-4-20250514":   "claude-opus-4.5",
	"claude-opus-4-0":          "claude-opus-4.5",
	// Haiku 4.5 mappings (AWS uses dot format)
	"claude-haiku-4-5":          "claude-haiku-4.5",
	"claude-haiku-4-5-20251001": "claude-haiku-4.5",
	// Sonnet 4.5 mappings (AWS uses uppercase V1_0 format)
	"claude-sonnet-4-5":          "CLAUDE_SONNET_4_5_20250929_V1_0",
	"claude-sonnet-4-5-20250929": "CLAUDE_SONNET_4_5_20250929_V1_0",
	// Sonnet 4.0 mappings (AWS uses uppercase V1_0 format)
	"claude-sonnet-4-20250514":       "CLAUDE_SONNET_4_20250514_V1_0",
	"CLAUDE_SONNET_4_20250514_V1_0":  "CLAUDE_SONNET_4_20250514_V1_0",
}

// KiroModels is the list of all supported model IDs for the Kiro channel.
var KiroModels = []string{
	"claude-sonnet-4-20250514",
	"claude-opus-4.5",
	"claude-haiku-4-5",
	"claude-opus-4-5-20251101",
	"claude-haiku-4-5-20251001",
	"CLAUDE_SONNET_4_20250514_V1_0",
	"claude-sonnet-4-5",
	"claude-sonnet-4-5-20250929",
}

// SlowModels is the set of models that require extended timeouts.
var SlowModels = map[string]bool{
	"claude-opus-4-5":   true,
	"claude-3-opus":     true,
	"claude-opus-4.5":   true,
}

// ToolMapping maps Claude Code tool names to Kiro tool names and parameter mappings.
type ToolMappingEntry struct {
	KiroTool    string
	ParamMap    map[string]string // CC param name → Kiro param name
	FixedParams map[string]string // Fixed params to inject
	Description string
	Remove      bool   // If true, tool is unsupported and should be removed
	RemoveReason string
	ServerSide  bool   // If true, tool is executed server-side by Kiro
}

// CCToKiroToolMapping maps Claude Code (CC) tool names to their Kiro equivalents.
var CCToKiroToolMapping = map[string]ToolMappingEntry{
	"Read": {
		KiroTool:    "readFile",
		ParamMap:    map[string]string{"file_path": "path", "offset": "start_line", "limit": "end_line"},
		Description: "Read file content",
	},
	"Write": {
		KiroTool:    "fsWrite",
		ParamMap:    map[string]string{"file_path": "path", "content": "text"},
		Description: "Write file",
	},
	"Edit": {
		KiroTool:    "strReplace",
		ParamMap:    map[string]string{"file_path": "path", "old_string": "oldStr", "new_string": "newStr"},
		Description: "Replace text in file",
	},
	"Bash": {
		KiroTool:    "executeBash",
		ParamMap:    map[string]string{"command": "command", "timeout": "timeout"},
		Description: "Execute shell command",
	},
	"Glob": {
		KiroTool:    "fileSearch",
		ParamMap:    map[string]string{"pattern": "query"},
		Description: "Search files by pattern",
	},
	"Grep": {
		KiroTool:    "grepSearch",
		ParamMap:    map[string]string{"pattern": "query", "path": "includePattern"},
		Description: "Search content in files",
	},
	"LS": {
		KiroTool:    "listDirectory",
		ParamMap:    map[string]string{"path": "path"},
		Description: "List directory",
	},
	"AskUserQuestion": {
		KiroTool:    "userInput",
		ParamMap:    map[string]string{"question": "question"},
		Description: "Ask user for input",
	},
	"Task": {
		KiroTool:    "invokeSubAgent",
		ParamMap:    map[string]string{"subagent_type": "name", "prompt": "prompt", "description": "explanation"},
		Description: "Invoke sub-agent for complex tasks",
	},
	"KillShell": {
		KiroTool:    "controlProcess",
		ParamMap:    map[string]string{"shell_id": "processId"},
		FixedParams: map[string]string{"action": "stop"},
		Description: "Stop background process",
	},
	"TaskOutput": {
		KiroTool:    "getProcessOutput",
		ParamMap:    map[string]string{"task_id": "processId"},
		Description: "Get process output",
	},
	"WebSearch": {
		KiroTool:    "webSearch",
		ParamMap:    map[string]string{"query": "query"},
		Description: "Search the web for information",
		ServerSide:  true,
	},
	"NotebookRead": {
		KiroTool:    "readFile",
		ParamMap:    map[string]string{"notebook_path": "path"},
		Description: "Read notebook as file",
	},
	// Unsupported tools (removed from requests)
	"LSP":           {Remove: true, RemoveReason: "Kiro getDiagnostics is not equivalent to CC LSP operations"},
	"WebFetch":      {Remove: true, RemoveReason: "AWS CodeWhisperer does not support builtin tools"},
	"TodoWrite":     {Remove: true, RemoveReason: "Not supported by Kiro"},
	"TodoRead":      {Remove: true, RemoveReason: "Not supported by Kiro"},
	"EnterPlanMode": {Remove: true, RemoveReason: "Not supported by Kiro"},
	"ExitPlanMode":  {Remove: true, RemoveReason: "Not supported by Kiro"},
	"NotebookEdit":  {Remove: true, RemoveReason: "Not supported by Kiro"},
	"Skill":         {Remove: true, RemoveReason: "CC internal only"},
}

// MapModelName converts a standard Anthropic model ID to the CodeWhisperer model ID.
// Returns the original name if no mapping is found.
func MapModelName(model string) string {
	if mapped, ok := FullModelMapping[model]; ok {
		return mapped
	}
	return model
}

// IsSlowModel returns true if the given model is known to be slow.
func IsSlowModel(model string) bool {
	for prefix := range SlowModels {
		if model == prefix || len(model) > len(prefix) && model[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
