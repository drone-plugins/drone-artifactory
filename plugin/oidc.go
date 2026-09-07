// Copyright 2025 Harness Inc. All rights reserved.
// Use of this source code is governed by the Blue Oak Model License
// that can be found in the LICENSE file.

package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	oidcTokenEndpoint = "/access/api/v1/oidc/token"
	oidcGrantType     = "urn:ietf:params:oauth:grant-type:token-exchange"
	oidcTokenType     = "urn:ietf:params:oauth:token-type:id_token"
)

type oidcTokenRequest struct {
	GrantType        string `json:"grant_type"`
	SubjectTokenType string `json:"subject_token_type"`
	SubjectToken     string `json:"subject_token"`
	ProviderName     string `json:"provider_name"`
	ProjectKey       string `json:"project_key,omitempty"`
}

type oidcTokenResponse struct {
	AccessToken     string `json:"access_token"`
	TokenType       string `json:"token_type"`
	ExpiresIn       int64  `json:"expires_in"`
	IssuedTokenType string `json:"issued_token_type"`
}

func exchangeOidcToken(artifactoryURL, oidcToken, providerName, projectKey string) (string, error) {
	endpoint := resolveOidcEndpoint(artifactoryURL)
	logrus.Printf("Exchanging OIDC token with JFrog at %s", endpoint)

	reqBody := oidcTokenRequest{
		GrantType:        oidcGrantType,
		SubjectTokenType: oidcTokenType,
		SubjectToken:     oidcToken,
		ProviderName:     providerName,
	}
	if projectKey != "" {
		reqBody.ProjectKey = projectKey
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal OIDC token request: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create OIDC token exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("OIDC token exchange request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read OIDC token exchange response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OIDC token exchange failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp oidcTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("failed to parse OIDC token exchange response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("OIDC token exchange response did not contain access_token")
	}

	logrus.Println("Successfully exchanged OIDC token for JFrog access token")
	return tokenResp.AccessToken, nil
}

// resolveOidcEndpoint strips /artifactory from the URL and appends the OIDC token endpoint.
func resolveOidcEndpoint(artifactoryURL string) string {
	base := strings.TrimRight(artifactoryURL, "/")
	base = strings.TrimSuffix(base, "/artifactory")
	return base + oidcTokenEndpoint
}
