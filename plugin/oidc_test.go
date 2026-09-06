// Copyright 2025 Harness Inc. All rights reserved.
// Use of this source code is governed by the Blue Oak Model License
// that can be found in the LICENSE file.

package plugin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveOidcEndpoint(t *testing.T) {
	tests := []struct {
		inputURL string
		expected string
	}{
		{
			inputURL: "https://example.jfrog.io/artifactory/",
			expected: "https://example.jfrog.io/access/api/v1/oidc/token",
		},
		{
			inputURL: "https://example.jfrog.io/artifactory",
			expected: "https://example.jfrog.io/access/api/v1/oidc/token",
		},
		{
			inputURL: "https://on-prem.company.com/artifactory/",
			expected: "https://on-prem.company.com/access/api/v1/oidc/token",
		},
		{
			inputURL: "https://example.jfrog.io",
			expected: "https://example.jfrog.io/access/api/v1/oidc/token",
		},
	}

	for _, tc := range tests {
		result := resolveOidcEndpoint(tc.inputURL)
		if result != tc.expected {
			t.Errorf("resolveOidcEndpoint(%q) = %q, want %q", tc.inputURL, result, tc.expected)
		}
	}
}

func TestExchangeOidcToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/access/api/v1/oidc/token" {
			t.Errorf("expected /access/api/v1/oidc/token, got %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected application/json, got %s", r.Header.Get("Content-Type"))
		}

		var reqBody oidcTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if reqBody.GrantType != oidcGrantType {
			t.Errorf("expected grant_type %q, got %q", oidcGrantType, reqBody.GrantType)
		}
		if reqBody.SubjectToken != "test-oidc-token" {
			t.Errorf("expected subject_token %q, got %q", "test-oidc-token", reqBody.SubjectToken)
		}
		if reqBody.ProviderName != "test-provider" {
			t.Errorf("expected provider_name %q, got %q", "test-provider", reqBody.ProviderName)
		}
		if reqBody.ProjectKey != "test-project" {
			t.Errorf("expected project_key %q, got %q", "test-project", reqBody.ProjectKey)
		}

		resp := oidcTokenResponse{
			AccessToken: "exchanged-access-token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	accessToken, err := exchangeOidcToken(server.URL+"/artifactory/", "test-oidc-token", "test-provider", "test-project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if accessToken != "exchanged-access-token" {
		t.Errorf("expected %q, got %q", "exchanged-access-token", accessToken)
	}
}

func TestExchangeOidcTokenError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "invalid token"}`))
	}))
	defer server.Close()

	_, err := exchangeOidcToken(server.URL+"/artifactory/", "bad-token", "provider", "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
