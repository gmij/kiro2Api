// Package constant contains patches for new-api's constant package.
//
// These constants should be added to the existing constant/channel.go and
// constant/api_type.go files in the new-api project.
package constant

// ============================================================================
// Patch for constant/channel.go
// ============================================================================
//
// Add these BEFORE ChannelTypeDummy:
//
//   ChannelTypeKiro = 58
//
// Add to ChannelBaseURLs array (index 58):
//
//   "https://codewhisperer.us-east-1.amazonaws.com", // 58 (Kiro)
//
// Add to ChannelTypeNames map:
//
//   ChannelTypeKiro: "Kiro",

// ChannelTypeKiro is the channel type constant for Kiro (AWS CodeWhisperer).
const ChannelTypeKiro = 58

// ============================================================================
// Patch for constant/api_type.go
// ============================================================================
//
// Add a new API type for Kiro:

// APITypeKiro is the API type constant for the Kiro channel.
const APITypeKiro = 34

// ============================================================================
// The following is the full diff to apply to constant/channel.go:
// ============================================================================
//
// --- a/constant/channel.go
// +++ b/constant/channel.go
// @@ -55,6 +55,7 @@
//  	ChannelTypeCodex          = 57
// +	ChannelTypeKiro           = 58
//  	ChannelTypeDummy          // this one is only for count, do not add any channel after this
//
// @@ -114,6 +115,7 @@
//  	"https://chatgpt.com",                       //57
// +	"https://codewhisperer.us-east-1.amazonaws.com", //58
//  }
//
// @@ -143,6 +145,7 @@
//  	ChannelTypeCodex:          "Codex",
// +	ChannelTypeKiro:           "Kiro",
//  }
