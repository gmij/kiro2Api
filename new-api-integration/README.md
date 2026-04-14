# Kiro Channel Integration for new-api

This directory contains a complete Go implementation of the Kiro (AWS CodeWhisperer) channel adaptor for [new-api](https://github.com/QuantumNous/new-api).

## Overview

The Kiro channel enables new-api to route LLM requests through AWS CodeWhisperer using Kiro OAuth credentials, providing access to Claude models (Sonnet 4, Opus 4.5, Haiku 4.5, etc.) via the AWS backend.

## Architecture

```
┌──────────────────┐     ┌────────────────────┐     ┌──────────────────────────────┐
│  Client Request   │────▶│    new-api Relay    │────▶│  AWS CodeWhisperer API       │
│  (OpenAI/Claude)  │     │  (Kiro Adaptor)    │     │  (generateAssistantResponse) │
└──────────────────┘     └────────────────────┘     └──────────────────────────────┘
                              │                            │
                              │ ConvertOpenAIRequest()     │ AWS Event Stream
                              │ ConvertClaudeRequest()     │ Binary Protocol
                              ▼                            ▼
                         conversationState JSON        Parsed Events → SSE/JSON
```

## File Structure

```
new-api-integration/
├── relay/channel/kiro/
│   ├── adaptor.go          # channel.Adaptor interface implementation (main entry)
│   ├── auth.go             # OAuth token management + device authorization flow
│   ├── converter.go        # Request/response format conversion
│   ├── event_stream.go     # AWS Event Stream binary protocol parser
│   └── constants.go        # Model mapping, URLs, tool mapping, constants
├── constant/
│   └── channel_patch.go    # ChannelTypeKiro + APITypeKiro constants (patch instructions)
├── controller/
│   └── kiro_oauth.go       # Device authorization REST endpoints
├── relay/
│   └── relay_adaptor_patch.go  # GetAdaptor() factory patch instructions
└── README.md               # This file
```

## Integration Steps

### Step 1: Copy the Kiro channel package

```bash
cp -r new-api-integration/relay/channel/kiro/ /path/to/new-api/relay/channel/kiro/
```

### Step 2: Add channel type constants

In `constant/channel.go`, add before `ChannelTypeDummy`:

```go
ChannelTypeKiro = 58
```

In `ChannelBaseURLs` array, add at index 58:

```go
"https://codewhisperer.us-east-1.amazonaws.com", //58
```

In `ChannelTypeNames` map:

```go
ChannelTypeKiro: "Kiro",
```

### Step 3: Add API type constant

In `constant/api_type.go`, add:

```go
APITypeKiro = 34
```

In the channel-to-API-type mapping function, add:

```go
case ChannelTypeKiro:
    apiType = APITypeKiro
```

### Step 4: Register the adaptor

In `relay/relay_adaptor.go`, add import:

```go
"github.com/QuantumNous/new-api/relay/channel/kiro"
```

In `GetAdaptor()` switch:

```go
case constant.APITypeKiro:
    return &kiro.Adaptor{}
```

### Step 5: Add OAuth controller (optional)

Copy `controller/kiro_oauth.go` to the new-api controllers and register routes:

In `router/api-router.go`:

```go
kiroRouter := apiRouter.Group("/kiro")
kiroRouter.Use(middleware.AdminAuth())
{
    kiroRouter.POST("/oauth/start", controller.KiroOAuthStart)
    kiroRouter.GET("/oauth/poll/:device_code", controller.KiroOAuthPoll)
}
```

### Step 6: Frontend support (optional)

Add "Kiro" to the channel type selector in the web frontend:

```javascript
// In the channel type options array
{ value: 58, label: 'Kiro (AWS CodeWhisperer)' }
```

## Channel Configuration

### Channel Key Format

The channel's `key` field stores JSON-encoded Kiro credentials:

```json
{
  "accessToken": "eyJhbGciOiJSUzI1NiIsInR5cCI6...",
  "refreshToken": "AOTp2GAkFI...",
  "clientId": "bQ1t9g...",
  "clientSecret": "secret-abc...",
  "expiresAt": "2025-01-15T12:00:00Z",
  "region": "us-east-1",
  "authMethod": "IdC",
  "profileArn": "arn:aws:iam::...",
  "startUrl": "https://view.awsapps.com/start/"
}
```

### Obtaining Credentials

#### Method 1: OAuth Device Authorization (via API)

1. `POST /api/kiro/oauth/start` → Returns verification URL + user code
2. User opens URL in browser and authorizes
3. `GET /api/kiro/oauth/poll/:device_code` → Returns credentials
4. Create channel with returned `channelKey`

#### Method 2: Manual (from existing Kiro/AWS SSO session)

1. Obtain credentials from `~/.aws/sso/cache/` or Kiro config
2. Format as JSON and paste into channel key field

### Channel Settings

| Setting | Value |
|---------|-------|
| Type | Kiro (58) |
| Base URL | (leave empty, auto-detected from credentials) |
| Key | JSON credentials (see above) |
| Models | claude-sonnet-4-20250514, claude-opus-4.5, claude-haiku-4-5, etc. |

## Supported Models

| Model ID | AWS CodeWhisperer ID |
|----------|---------------------|
| `claude-sonnet-4-20250514` | `CLAUDE_SONNET_4_20250514_V1_0` |
| `claude-sonnet-4-5-20250929` | `CLAUDE_SONNET_4_5_20250929_V1_0` |
| `claude-opus-4.5` | `claude-opus-4.5` |
| `claude-opus-4-5` | `claude-opus-4.5` |
| `claude-haiku-4-5` | `claude-haiku-4.5` |

## Features

### Reused from new-api (zero additional implementation)

- ✅ Multi-account pool → Channel weight-based load balancing
- ✅ API Key authentication → Token system + quota management
- ✅ Health checking → Channel test + auto-disable
- ✅ Failure retry → Configurable retry count
- ✅ Protocol conversion (OpenAI↔Claude) → Existing conversion layer
- ✅ Redis caching → Existing Redis integration
- ✅ Usage tracking → Token usage + quota deduction
- ✅ Frontend management → React admin panel
- ✅ Streaming → Existing SSE/Stream handling

### Implemented in this package

- ✅ AWS Event Stream binary protocol parsing
- ✅ OAuth token auto-refresh (5-min window + 30s debounce)
- ✅ OpenAI → CodeWhisperer request conversion
- ✅ Claude → CodeWhisperer request conversion
- ✅ Tool call mapping (Claude Code → Kiro tools)
- ✅ Model name mapping (Anthropic → AWS format)
- ✅ Device fingerprint randomization (anti-detection)
- ✅ Extended thinking via prompt injection
- ✅ Image/vision content support
- ✅ Device authorization flow (OAuth)
- ✅ Adaptive timeouts for slow models

## Technical Details

### AWS Event Stream Protocol

The CodeWhisperer API returns responses in AWS Event Stream binary format:

```
[4 bytes] Total message length (big-endian uint32)
[4 bytes] Headers length (big-endian uint32)
[4 bytes] Prelude CRC (big-endian uint32)
[N bytes] Headers (name-value pairs)
[M bytes] Payload (JSON)
[4 bytes] Message CRC (big-endian uint32)
```

Event types:
- `assistantResponseEvent` → Text content
- `toolUseEvent` → Tool invocation
- `meteringEvent` → Token usage
- `reasoningContentEvent` → Extended thinking
- `codeReferenceEvent` → Open-source attribution
- `messageMetadataEvent` → Conversation metadata

### Token Refresh

Mirrors the official AWS SDK logic:
- **Expiry window**: 5 minutes before expiration
- **Debounce**: 30 seconds between refresh attempts
- **IdC method**: Uses `https://oidc.{region}.amazonaws.com/token`
- **Social method**: Uses `https://prod.{region}.auth.desktop.kiro.dev/refreshToken`

### Anti-Detection

Each request uses randomized:
- User-Agent with random SDK version, Node.js version, Windows version
- MAC address SHA256 hash (device fingerprint)
- Kiro IDE version string
- `amz-sdk-invocation-id` (UUID per request)

## Dependencies

All dependencies are already in new-api's `go.mod`:
- `github.com/google/uuid` — UUID generation
- `github.com/tidwall/gjson` — JSON parsing
- `github.com/gin-gonic/gin` — HTTP framework
- Standard library: `encoding/binary`, `encoding/json`, `crypto/sha256`, `net/http`

Optional (for enhanced Event Stream parsing):
- `github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream` — Already in go.mod

## License

Same as the parent kiro2Api project.
