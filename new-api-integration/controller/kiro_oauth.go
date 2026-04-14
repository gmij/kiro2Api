// Package controller contains the Kiro OAuth device authorization flow endpoints.
//
// These endpoints should be registered in the new-api router (router/api-router.go).
// Add under the admin routes:
//
//   kiroRouter := apiRouter.Group("/kiro")
//   kiroRouter.Use(middleware.AdminAuth())
//   {
//       kiroRouter.POST("/oauth/start", controller.KiroOAuthStart)
//       kiroRouter.GET("/oauth/poll/:device_code", controller.KiroOAuthPoll)
//   }
package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	kiro "github.com/QuantumNous/new-api/relay/channel/kiro"

	"github.com/gin-gonic/gin"
)

// In-memory store for pending device authorization flows.
// In production, consider using Redis for multi-instance deployments.
var (
	pendingAuthFlows   = make(map[string]*pendingAuth)
	pendingAuthFlowsMu sync.Mutex
)

type pendingAuth struct {
	ClientID     string
	ClientSecret string
	Region       string
	StartURL     string
	Interval     int
	ExpiresIn    int
}

// KiroOAuthStartRequest is the request body for starting Kiro OAuth.
type KiroOAuthStartRequest struct {
	Region   string `json:"region"`
	StartURL string `json:"startUrl"`
}

// KiroOAuthStartResponse is returned when device authorization is started.
type KiroOAuthStartResponse struct {
	DeviceCode              string `json:"deviceCode"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
}

// KiroOAuthStart initiates the AWS SSO OIDC device authorization flow.
//
// POST /api/kiro/oauth/start
//
// Request Body:
//
//	{
//	  "region": "us-east-1",           // optional, defaults to us-east-1
//	  "startUrl": "https://view.awsapps.com/start/"  // optional
//	}
//
// Response:
//
//	{
//	  "deviceCode": "...",
//	  "userCode": "ABCD-EFGH",
//	  "verificationUri": "https://device.sso.us-east-1.amazonaws.com/",
//	  "verificationUriComplete": "https://device.sso.us-east-1.amazonaws.com/?user_code=ABCD-EFGH",
//	  "expiresIn": 600,
//	  "interval": 5
//	}
func KiroOAuthStart(c *gin.Context) {
	var req KiroOAuthStartRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Use defaults
		req.Region = kiro.DefaultRegion
		req.StartURL = "https://view.awsapps.com/start/"
	}
	if req.Region == "" {
		req.Region = kiro.DefaultRegion
	}
	if req.StartURL == "" {
		req.StartURL = "https://view.awsapps.com/start/"
	}

	// Step 1: Register client
	clientID, clientSecret, err := kiro.RegisterClient(req.Region, req.StartURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("Failed to register OIDC client: %v", err),
		})
		return
	}

	// Step 2: Start device authorization
	authResult, err := kiro.StartDeviceAuthorization(req.Region, clientID, clientSecret, req.StartURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("Failed to start device authorization: %v", err),
		})
		return
	}

	// Store the pending auth flow for polling
	pendingAuthFlowsMu.Lock()
	pendingAuthFlows[authResult.DeviceCode] = &pendingAuth{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Region:       req.Region,
		StartURL:     req.StartURL,
		Interval:     authResult.Interval,
		ExpiresIn:    authResult.ExpiresIn,
	}
	pendingAuthFlowsMu.Unlock()

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": KiroOAuthStartResponse{
			DeviceCode:              authResult.DeviceCode,
			UserCode:                authResult.UserCode,
			VerificationURI:         authResult.VerificationURI,
			VerificationURIComplete: authResult.VerificationURIComplete,
			ExpiresIn:               authResult.ExpiresIn,
			Interval:                authResult.Interval,
		},
	})
}

// KiroOAuthPoll polls for the device token after user authorization.
//
// GET /api/kiro/oauth/poll/:device_code
//
// Response (success):
//
//	{
//	  "success": true,
//	  "data": {
//	    "channelKey": "{...JSON credentials...}",
//	    "channelName": "Kiro-xxxx",
//	    "channelType": 58
//	  }
//	}
//
// Response (pending):
//
//	{
//	  "success": false,
//	  "message": "authorization_pending"
//	}
func KiroOAuthPoll(c *gin.Context) {
	deviceCode := c.Param("device_code")
	if deviceCode == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "device_code is required",
		})
		return
	}

	pendingAuthFlowsMu.Lock()
	pending, exists := pendingAuthFlows[deviceCode]
	pendingAuthFlowsMu.Unlock()

	if !exists {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": "No pending authorization found for this device code",
		})
		return
	}

	// Poll for token (single attempt, client should retry)
	creds, err := kiro.PollDeviceToken(
		pending.Region,
		pending.ClientID,
		pending.ClientSecret,
		deviceCode,
		pending.Interval,
		pending.ExpiresIn,
	)
	if err != nil {
		// Check if it's a pending error
		if err.Error() == "device authorization expired" || err.Error() == "user denied authorization" {
			// Clean up
			pendingAuthFlowsMu.Lock()
			delete(pendingAuthFlows, deviceCode)
			pendingAuthFlowsMu.Unlock()
		}

		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	// Success — clean up and return credentials
	pendingAuthFlowsMu.Lock()
	delete(pendingAuthFlows, deviceCode)
	pendingAuthFlowsMu.Unlock()

	// Serialize credentials as channel key
	channelKey, _ := json.Marshal(creds)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"channelKey":  string(channelKey),
			"channelName": fmt.Sprintf("Kiro-%s", deviceCode[:8]),
			"channelType": 58, // ChannelTypeKiro
		},
	})
}
