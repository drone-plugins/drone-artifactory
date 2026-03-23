package plugin

import (
	"testing"
)

func TestExtractPyPIRepoName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"api/pypi/pypi-internalfacing", "pypi-internalfacing"},
		{"api/pypi/pypi-internalfacing/", "pypi-internalfacing"},
		{"pypi-internalfacing", "pypi-internalfacing"},
		{"pypi-internalfacing/", "pypi-internalfacing"},
		{"pypi-repo/sub/path", "pypi-repo"},
		{"api/pypi/pypi-repo/sub/path", "pypi-repo"},
		{"", ""},
	}

	for _, tc := range tests {
		result := extractPyPIRepoName(tc.input)
		if result != tc.expected {
			t.Errorf("extractPyPIRepoName(%q) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

func TestGetPyPIPublishCommandArgs(t *testing.T) {
	args := Args{
		Username:    "admin",
		Password:    "password123",
		URL:         RtUrlTestStr,
		Source:      "dist/*.whl",
		Target:      "api/pypi/pypi-internalfacing",
		BuildName:   "myBuild",
		BuildNumber: "1",
	}

	cmdList, err := GetPyPIPublishCommandArgs(args)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Expect 4 commands: config add, pip-config, twine upload, build-publish
	if len(cmdList) != 4 {
		t.Fatalf("Expected 4 commands, got %d", len(cmdList))
	}

	// Verify config add command
	if cmdList[0][0] != "config" || cmdList[0][1] != "add" {
		t.Errorf("Expected config add command, got %v", cmdList[0])
	}

	// Verify pip-config command
	if cmdList[1][0] != "pip-config" {
		t.Errorf("Expected pip-config command, got %v", cmdList[1])
	}
	foundRepoDeploy := false
	for _, arg := range cmdList[1] {
		if arg == "--repo-deploy=pypi-internalfacing" {
			foundRepoDeploy = true
		}
	}
	if !foundRepoDeploy {
		t.Errorf("Expected --repo-deploy=pypi-internalfacing in pip-config, got %v", cmdList[1])
	}

	// Verify twine upload command
	if cmdList[2][0] != "twine" || cmdList[2][1] != "upload" || cmdList[2][2] != "dist/*.whl" {
		t.Errorf("Expected twine upload dist/*.whl, got %v", cmdList[2])
	}

	// Verify build-publish command
	if cmdList[3][0] != "rt" || cmdList[3][1] != "build-publish" {
		t.Errorf("Expected rt build-publish command, got %v", cmdList[3])
	}
}

func TestGetPyPIPublishCommandArgsAccessToken(t *testing.T) {
	args := Args{
		AccessToken: "token123",
		URL:         RtUrlTestStr,
		Source:      "dist/*.whl",
		Target:      "pypi-local",
		BuildName:   "myBuild",
		BuildNumber: "1",
	}

	cmdList, err := GetPyPIPublishCommandArgs(args)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(cmdList) != 4 {
		t.Fatalf("Expected 4 commands, got %d", len(cmdList))
	}

	// Verify access token is used in config add
	foundAccessToken := false
	for _, arg := range cmdList[0] {
		if arg == "--access-token $PLUGIN_ACCESS_TOKEN" {
			foundAccessToken = true
		}
	}
	if !foundAccessToken {
		t.Errorf("Expected --access-token in config add, got %v", cmdList[0])
	}
}

func TestGetPyPIPublishCommandArgsNoBuildInfo(t *testing.T) {
	args := Args{
		Username: "admin",
		Password: "password123",
		URL:      RtUrlTestStr,
		Source:   "dist/*.whl",
		Target:   "pypi-local",
	}

	cmdList, err := GetPyPIPublishCommandArgs(args)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Expect 3 commands: config add, pip-config, twine upload (no build-publish)
	if len(cmdList) != 3 {
		t.Fatalf("Expected 3 commands (no build-publish), got %d", len(cmdList))
	}

	// Verify last command is twine upload, not build-publish
	if cmdList[2][0] != "twine" {
		t.Errorf("Expected twine as last command, got %v", cmdList[2])
	}
}

func TestGetPyPIPublishCommandArgsMissingSource(t *testing.T) {
	args := Args{
		Username: "admin",
		Password: "password123",
		URL:      RtUrlTestStr,
		Target:   "pypi-local",
	}

	_, err := GetPyPIPublishCommandArgs(args)
	if err == nil {
		t.Fatalf("Expected error for missing source, got nil")
	}
}

func TestGetPyPIPublishCommandArgsMissingTarget(t *testing.T) {
	args := Args{
		Username: "admin",
		Password: "password123",
		URL:      RtUrlTestStr,
		Source:   "dist/*.whl",
	}

	_, err := GetPyPIPublishCommandArgs(args)
	if err == nil {
		t.Fatalf("Expected error for missing target, got nil")
	}
}
