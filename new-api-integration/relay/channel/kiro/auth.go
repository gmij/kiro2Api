package kiro

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// KiroCredentials stores the OAuth credentials for a Kiro channel.
// This is serialized as JSON in the Channel's key field.
type KiroCredentials struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	ExpiresAt    string `json:"expiresAt"`
	Region       string `json:"region"`
	AuthMethod   string `json:"authMethod"` // "IdC" or "social"
	ProfileArn   string `json:"profileArn,omitempty"`
	StartURL     string `json:"startUrl,omitempty"`
}

// ParseCredentials parses Kiro credentials from the channel key field (JSON string).
func ParseCredentials(key string) (*KiroCredentials, error) {
	var creds KiroCredentials
	if err := json.Unmarshal([]byte(key), &creds); err != nil {
		return nil, fmt.Errorf("failed to parse Kiro credentials: %w", err)
	}
	if creds.AccessToken == "" {
		return nil, fmt.Errorf("Kiro credentials missing accessToken")
	}
	if creds.Region == "" {
		creds.Region = DefaultRegion
	}
	if creds.AuthMethod == "" {
		creds.AuthMethod = AuthMethodIDC
	}
	return &creds, nil
}

// TokenManager handles OAuth token refresh with debounce logic per account.
// It mirrors the Node.js refreshAccessTokenIfNeeded with 5-minute expiry window
// and 30-second debounce.
type TokenManager struct {
	mu              sync.Mutex
	creds           *KiroCredentials
	lastRefreshTime time.Time
	refreshPromise  chan error // nil when no refresh is in progress
	httpClient      *http.Client
}

// NewTokenManager creates a new TokenManager for the given credentials.
func NewTokenManager(creds *KiroCredentials) *TokenManager {
	return &TokenManager{
		creds: creds,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// GetAccessToken returns the current access token, refreshing if needed.
func (tm *TokenManager) GetAccessToken() (string, error) {
	if err := tm.RefreshIfNeeded(); err != nil {
		return "", err
	}
	return tm.creds.AccessToken, nil
}

// GetCredentials returns a copy of the current credentials.
func (tm *TokenManager) GetCredentials() KiroCredentials {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return *tm.creds
}

// RefreshIfNeeded checks token expiry and refreshes if within the 5-minute window.
// Implements 30-second debounce to prevent rapid refresh attempts.
func (tm *TokenManager) RefreshIfNeeded() error {
	tm.mu.Lock()

	if tm.creds.RefreshToken == "" {
		tm.mu.Unlock()
		return nil // No refresh token available, use current access token
	}

	// Parse expiry time
	expiresAt, err := time.Parse(time.RFC3339, tm.creds.ExpiresAt)
	if err != nil {
		tm.mu.Unlock()
		return nil // Can't parse expiry, assume still valid
	}

	now := time.Now()
	timeUntilExpiry := expiresAt.Sub(now)

	// If not within expiry window (5 minutes), no need to refresh
	if timeUntilExpiry > ExpireWindowDuration {
		tm.mu.Unlock()
		return nil
	}

	// Check debounce (30 seconds)
	timeSinceLastRefresh := now.Sub(tm.lastRefreshTime)
	if timeSinceLastRefresh < RefreshDebounceDuration {
		if timeUntilExpiry <= 0 {
			tm.mu.Unlock()
			return fmt.Errorf("token is expired and refresh was attempted too recently (debounce)")
		}
		tm.mu.Unlock()
		return nil // Still within debounce window but token not yet expired
	}

	// If a refresh is already in progress, wait for it
	if tm.refreshPromise != nil {
		ch := tm.refreshPromise
		tm.mu.Unlock()
		return <-ch
	}

	// Start refresh
	tm.lastRefreshTime = now
	tm.refreshPromise = make(chan error, 1)
	tm.mu.Unlock()

	err = tm.doRefresh()

	tm.mu.Lock()
	ch := tm.refreshPromise
	tm.refreshPromise = nil
	tm.mu.Unlock()

	// Notify waiting goroutines
	ch <- err
	close(ch)

	return err
}

// doRefresh performs the actual token refresh HTTP call.
func (tm *TokenManager) doRefresh() error {
	tm.mu.Lock()
	creds := tm.creds
	tm.mu.Unlock()

	var refreshURL string
	body := map[string]string{
		"refreshToken": creds.RefreshToken,
	}

	if creds.AuthMethod != AuthMethodSocial {
		// IdC method: use OIDC token endpoint
		refreshURL = fmt.Sprintf(RefreshIDCURL, creds.Region)
		body["clientId"] = creds.ClientID
		body["clientSecret"] = creds.ClientSecret
		body["grantType"] = "refresh_token"
	} else {
		// Social method: use Kiro desktop auth endpoint
		refreshURL = fmt.Sprintf(RefreshURL, creds.Region)
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal refresh body: %w", err)
	}

	req, err := http.NewRequest("POST", refreshURL, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return fmt.Errorf("failed to create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := tm.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("token refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("token refresh failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    *int   `json:"expiresIn"`
		ExpiresAt    string `json:"expiresAt"`
		ProfileArn   string `json:"profileArn"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode refresh response: %w", err)
	}

	if result.AccessToken == "" {
		return fmt.Errorf("refresh response missing accessToken")
	}

	// Update credentials
	tm.mu.Lock()
	defer tm.mu.Unlock()

	tm.creds.AccessToken = result.AccessToken
	if result.RefreshToken != "" {
		tm.creds.RefreshToken = result.RefreshToken
	}
	if result.ProfileArn != "" {
		tm.creds.ProfileArn = result.ProfileArn
	}

	// Calculate new expiry
	if result.ExpiresIn != nil {
		tm.creds.ExpiresAt = time.Now().Add(time.Duration(*result.ExpiresIn) * time.Second).Format(time.RFC3339)
	} else if result.ExpiresAt != "" {
		tm.creds.ExpiresAt = result.ExpiresAt
	} else {
		tm.creds.ExpiresAt = time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	}

	return nil
}

// ---- Device Authorization Flow ----

// DeviceAuthResult holds the result of the device authorization start.
type DeviceAuthResult struct {
	DeviceCode              string `json:"deviceCode"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
}

// RegisterClient registers a new OIDC client with AWS SSO.
func RegisterClient(region, startURL string) (clientID, clientSecret string, err error) {
	registerURL := fmt.Sprintf(RegisterURL, region)

	// Randomize client name for anti-detection
	clientName := fmt.Sprintf("Kiro-%s-%s", uuid.New().String()[:8], uuid.New().String()[:4])

	body := map[string]interface{}{
		"clientName":   clientName,
		"clientType":   "public",
		"scopes":       OAuthScopes,
		"grantTypes":   []string{"authorization_code", "refresh_token"},
		"redirectUris": []string{fmt.Sprintf("http://127.0.0.1:%d/oauth/callback", 49152+time.Now().UnixNano()%16383)},
		"issuerUrl":    startURL,
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal register body: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("POST", registerURL, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return "", "", fmt.Errorf("failed to create register request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("register client request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("register client failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("failed to decode register response: %w", err)
	}

	return result.ClientID, result.ClientSecret, nil
}

// StartDeviceAuthorization starts the AWS OIDC device authorization flow.
func StartDeviceAuthorization(region, clientID, clientSecret, startURL string) (*DeviceAuthResult, error) {
	deviceAuthURL := fmt.Sprintf(DeviceAuthURL, region)

	body := map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"startUrl":     startURL,
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal device auth body: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("POST", deviceAuthURL, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return nil, fmt.Errorf("failed to create device auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("device authorization request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("device authorization failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var result DeviceAuthResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode device auth response: %w", err)
	}

	return &result, nil
}

// PollDeviceToken polls for the device token after user authorization.
// Returns credentials on success. Blocks until success, timeout, or error.
func PollDeviceToken(region, clientID, clientSecret, deviceCode string, interval, expiresIn int) (*KiroCredentials, error) {
	tokenURL := fmt.Sprintf(RefreshIDCURL, region)

	body := map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"deviceCode":   deviceCode,
		"grantType":    DeviceGrantType,
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal token poll body: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	deadline := time.Now().Add(time.Duration(expiresIn) * time.Second)
	pollInterval := time.Duration(interval) * time.Second
	if pollInterval < 5*time.Second {
		pollInterval = 5 * time.Second
	}

	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)

		req, err := http.NewRequest("POST", tokenURL, strings.NewReader(string(bodyJSON)))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			var result struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
				ExpiresIn    int    `json:"expiresIn"`
			}
			if err := json.Unmarshal(respBody, &result); err != nil {
				return nil, fmt.Errorf("failed to decode token response: %w", err)
			}

			expiresAt := time.Now().Add(time.Duration(result.ExpiresIn) * time.Second).Format(time.RFC3339)

			return &KiroCredentials{
				AccessToken:  result.AccessToken,
				RefreshToken: result.RefreshToken,
				ClientID:     clientID,
				ClientSecret: clientSecret,
				ExpiresAt:    expiresAt,
				Region:       region,
				AuthMethod:   AuthMethodIDC,
			}, nil
		}

		// Check for authorization_pending (expected during polling)
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil {
			if errResp.Error == "authorization_pending" {
				continue
			}
			if errResp.Error == "slow_down" {
				pollInterval += 5 * time.Second
				continue
			}
			if errResp.Error == "expired_token" {
				return nil, fmt.Errorf("device authorization expired")
			}
			if errResp.Error == "access_denied" {
				return nil, fmt.Errorf("user denied authorization")
			}
		}
	}

	return nil, fmt.Errorf("device authorization timed out after %d seconds", expiresIn)
}
