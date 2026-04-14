// Package relay contains the adaptor factory patch for Kiro channel.
//
// This patch should be applied to relay/relay_adaptor.go in the new-api project.
package relay

// ============================================================================
// Patch for relay/relay_adaptor.go
// ============================================================================
//
// 1. Add import:
//
//   "github.com/QuantumNous/new-api/relay/channel/kiro"
//
// 2. In the GetAdaptor() function's switch statement, add:
//
//   case constant.APITypeKiro:
//       return &kiro.Adaptor{}
//
// 3. In the channel type → API type mapping (if applicable), add:
//
//   constant.ChannelTypeKiro: constant.APITypeKiro,
//
// ============================================================================
// Full diff:
// ============================================================================
//
// --- a/relay/relay_adaptor.go
// +++ b/relay/relay_adaptor.go
// @@ imports
// +	"github.com/QuantumNous/new-api/relay/channel/kiro"
//
// @@ GetAdaptor switch
//  	case constant.APITypeCodex:
//  		return &codex.Adaptor{}
// +	case constant.APITypeKiro:
// +		return &kiro.Adaptor{}
//  	}
//  	return nil
//  }
