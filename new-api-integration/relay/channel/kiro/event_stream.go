package kiro

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
)

// EventStreamMessage represents a single parsed AWS Event Stream message.
type EventStreamMessage struct {
	EventType   string
	ContentType string
	MessageType string
	Payload     []byte
	TotalLength uint32
	NextOffset  int
}

// EventStreamEvent represents a parsed event from the stream.
type EventStreamEvent struct {
	Type string
	Data interface{}
}

// ContentEvent holds text content data.
type ContentEvent struct {
	Content string `json:"content"`
}

// ToolUseEvent holds tool use data.
type ToolUseEvent struct {
	Name      string `json:"name"`
	ToolUseID string `json:"toolUseId"`
	Input     string `json:"input"`
	Stop      bool   `json:"stop"`
}

// MeteringEvent holds token usage data.
type MeteringEvent struct {
	Usage int    `json:"usage"`
	Unit  string `json:"unit"`
}

// ThinkingEvent holds reasoning/thinking data.
type ThinkingEvent struct {
	Thinking string `json:"thinking"`
}

// CodeReferenceEvent holds code attribution data.
type CodeReference struct {
	LicenseName string `json:"licenseName"`
	Repository  string `json:"repository"`
	URL         string `json:"url"`
}

type CodeReferenceEvent struct {
	References []CodeReference `json:"references"`
}

// MetadataEvent holds conversation metadata.
type MetadataEvent struct {
	ConversationID string `json:"conversationId"`
}

// ParseResult contains parsed events and any remaining unparsed buffer.
type ParseResult struct {
	Events    []EventStreamEvent
	Remaining []byte
}

// ParseEventStreamMessage parses a single AWS Event Stream message from the buffer.
//
// AWS Event Stream binary format:
//
//	[4 bytes] Total length (big-endian uint32)
//	[4 bytes] Headers length (big-endian uint32)
//	[4 bytes] Prelude CRC (big-endian uint32)
//	[N bytes] Headers
//	[M bytes] Payload
//	[4 bytes] Message CRC (big-endian uint32)
//
// Header format:
//
//	[1 byte]  Header name length
//	[N bytes] Header name (UTF-8)
//	[1 byte]  Header value type (7 = string)
//	[2 bytes] Header value length (big-endian uint16)
//	[N bytes] Header value (UTF-8)
func ParseEventStreamMessage(buffer []byte, offset int) *EventStreamMessage {
	remaining := len(buffer) - offset
	// Need at least 16 bytes for prelude + message CRC
	if remaining < 16 {
		return nil
	}

	// Read Prelude (12 bytes)
	totalLength := binary.BigEndian.Uint32(buffer[offset : offset+4])
	headersLength := binary.BigEndian.Uint32(buffer[offset+4 : offset+8])
	// preludeCRC at buffer[offset+8 : offset+12] — we skip CRC validation for simplicity

	// Check if we have the complete message
	if uint32(remaining) < totalLength {
		return nil
	}

	// Parse Headers
	headerOffset := offset + 12
	headersEnd := headerOffset + int(headersLength)
	headers := make(map[string]string)

	for headerOffset < headersEnd {
		if headerOffset >= len(buffer) {
			break
		}
		headerNameLength := int(buffer[headerOffset])
		headerOffset++

		if headerOffset+headerNameLength > len(buffer) {
			break
		}
		headerName := string(buffer[headerOffset : headerOffset+headerNameLength])
		headerOffset += headerNameLength

		if headerOffset >= len(buffer) {
			break
		}
		headerValueType := buffer[headerOffset]
		headerOffset++

		if headerOffset+2 > len(buffer) {
			break
		}

		// Type 7 = string value
		if headerValueType == 7 {
			headerValueLength := int(binary.BigEndian.Uint16(buffer[headerOffset : headerOffset+2]))
			headerOffset += 2
			if headerOffset+headerValueLength > len(buffer) {
				break
			}
			headerValue := string(buffer[headerOffset : headerOffset+headerValueLength])
			headerOffset += headerValueLength
			headers[headerName] = headerValue
		} else {
			// Other types: read 2-byte length then skip
			headerValueLength := int(binary.BigEndian.Uint16(buffer[headerOffset : headerOffset+2]))
			headerOffset += 2
			headerOffset += headerValueLength
		}
	}

	// Read Payload
	payloadStart := offset + 12 + int(headersLength)
	payloadEnd := offset + int(totalLength) - 4 // Subtract message CRC
	if payloadEnd < payloadStart {
		payloadEnd = payloadStart
	}
	payload := buffer[payloadStart:payloadEnd]

	eventType := headers[":event-type"]
	if eventType == "" {
		eventType = "unknown"
	}
	contentType := headers[":content-type"]
	if contentType == "" {
		contentType = "application/json"
	}
	messageType := headers[":message-type"]
	if messageType == "" {
		messageType = "event"
	}

	return &EventStreamMessage{
		EventType:   eventType,
		ContentType: contentType,
		MessageType: messageType,
		Payload:     payload,
		TotalLength: totalLength,
		NextOffset:  offset + int(totalLength),
	}
}

// ParseEventStreamBuffer parses all complete messages from a buffer.
// Returns parsed events and any remaining unparsed bytes.
func ParseEventStreamBuffer(buffer []byte) ParseResult {
	var events []EventStreamEvent
	offset := 0

	for offset < len(buffer) {
		msg := ParseEventStreamMessage(buffer, offset)
		if msg == nil {
			// Not enough data for a complete message
			return ParseResult{
				Events:    events,
				Remaining: buffer[offset:],
			}
		}

		offset = msg.NextOffset
		events = append(events, parseMessageToEvent(msg)...)
	}

	return ParseResult{
		Events:    events,
		Remaining: nil,
	}
}

// parseMessageToEvent converts a raw EventStreamMessage into typed events.
func parseMessageToEvent(msg *EventStreamMessage) []EventStreamEvent {
	if msg.MessageType == "exception" {
		// Error message from AWS
		return []EventStreamEvent{{
			Type: "error",
			Data: fmt.Sprintf("AWS exception (%s): %s", msg.EventType, string(msg.Payload)),
		}}
	}

	var events []EventStreamEvent
	payload := msg.Payload

	switch msg.EventType {
	case "assistantResponseEvent":
		var parsed struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(payload, &parsed); err == nil && parsed.Content != "" {
			events = append(events, EventStreamEvent{
				Type: "content",
				Data: ContentEvent{Content: parsed.Content},
			})
		}

	case "toolUseEvent":
		var parsed struct {
			Name      string `json:"name"`
			ToolUseID string `json:"toolUseId"`
			Input     string `json:"input"`
			Stop      bool   `json:"stop"`
		}
		if err := json.Unmarshal(payload, &parsed); err == nil {
			events = append(events, EventStreamEvent{
				Type: "toolUse",
				Data: ToolUseEvent{
					Name:      parsed.Name,
					ToolUseID: parsed.ToolUseID,
					Input:     parsed.Input,
					Stop:      parsed.Stop,
				},
			})
		}

	case "meteringEvent":
		var parsed struct {
			Usage int    `json:"usage"`
			Unit  string `json:"unit"`
		}
		if err := json.Unmarshal(payload, &parsed); err == nil {
			events = append(events, EventStreamEvent{
				Type: "metering",
				Data: MeteringEvent{Usage: parsed.Usage, Unit: parsed.Unit},
			})
		}

	case "reasoningContentEvent":
		var parsed struct {
			Text          string `json:"text"`
			ReasoningText string `json:"reasoningText"`
		}
		if err := json.Unmarshal(payload, &parsed); err == nil {
			text := parsed.Text
			if text == "" {
				text = parsed.ReasoningText
			}
			if text != "" {
				events = append(events, EventStreamEvent{
					Type: "thinking",
					Data: ThinkingEvent{Thinking: text},
				})
			}
		}

	case "codeReferenceEvent":
		var parsed struct {
			References []CodeReference `json:"references"`
		}
		if err := json.Unmarshal(payload, &parsed); err == nil && len(parsed.References) > 0 {
			var valid []CodeReference
			for _, ref := range parsed.References {
				if ref.LicenseName != "" && ref.Repository != "" && ref.URL != "" {
					valid = append(valid, ref)
				}
			}
			if len(valid) > 0 {
				events = append(events, EventStreamEvent{
					Type: "codeReference",
					Data: CodeReferenceEvent{References: valid},
				})
			}
		}

	case "messageMetadataEvent":
		var parsed struct {
			ConversationID string `json:"conversationId"`
		}
		if err := json.Unmarshal(payload, &parsed); err == nil && parsed.ConversationID != "" {
			events = append(events, EventStreamEvent{
				Type: "metadata",
				Data: MetadataEvent{ConversationID: parsed.ConversationID},
			})
		}

	case "followupPromptEvent":
		// Ignored for now — not used in API relay
	}

	return events
}
