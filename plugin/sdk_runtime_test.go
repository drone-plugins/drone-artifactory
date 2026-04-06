package plugin

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCreateServerDetailsPrefersUsernamePassword(t *testing.T) {
	ctx := &runtimeContext{
		args: Args{
			URL:         RtUrlTestStr,
			Username:    "user",
			Password:    "pass",
			APIKey:      "api-key",
			AccessToken: "access-token",
		},
	}

	serverDetails, err := ctx.createServerDetails("server-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if serverDetails.ServerId != "server-1" {
		t.Fatalf("unexpected server id: %s", serverDetails.ServerId)
	}
	if serverDetails.User != "user" || serverDetails.Password != "pass" {
		t.Fatalf("expected username/password auth, got user=%q password=%q", serverDetails.User, serverDetails.Password)
	}
	if serverDetails.AccessToken != "" {
		t.Fatalf("expected empty access token, got %q", serverDetails.AccessToken)
	}
	if serverDetails.ArtifactoryUrl != RtUrlTestStr {
		t.Fatalf("unexpected artifactory url: %s", serverDetails.ArtifactoryUrl)
	}
}

func TestUploadSpecFromSourceTarget(t *testing.T) {
	ctx := &runtimeContext{
		args: Args{
			Source:      "dist/*.tgz",
			Target:      "/libs-release-local/app/",
			Flat:        "true",
			TargetProps: "key1=value1,key2='',key3=null,key4=value4",
		},
	}

	specFiles, err := ctx.uploadSpec()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	file := specFiles.Get(0)
	if file.Pattern != "dist/*.tgz" {
		t.Fatalf("unexpected pattern: %s", file.Pattern)
	}
	if file.Target != "libs-release-local/app/" {
		t.Fatalf("unexpected target: %s", file.Target)
	}
	if file.TargetProps != "key1=value1,key4=value4" {
		t.Fatalf("unexpected target props: %s", file.TargetProps)
	}
	if flat, err := file.IsFlat(false); err != nil || !flat {
		t.Fatalf("expected flat upload, got flat=%v err=%v", flat, err)
	}
}

func TestDownloadSpecMapsTargetToSourcePattern(t *testing.T) {
	ctx := &runtimeContext{
		args: Args{
			Source:      "/tmp/out",
			Target:      "libs-release-local/app/*.tgz",
			BuildName:   "build-name",
			BuildNumber: "42",
			Project:     "proj",
		},
	}

	specFiles, err := ctx.downloadSpec()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	file := specFiles.Get(0)
	if file.Pattern != "libs-release-local/app/*.tgz" {
		t.Fatalf("unexpected pattern: %s", file.Pattern)
	}
	if file.Target != "/tmp/out" {
		t.Fatalf("unexpected target: %s", file.Target)
	}
	if file.Build != "build-name/42" {
		t.Fatalf("unexpected build ref: %s", file.Build)
	}
	if file.Project != "proj" {
		t.Fatalf("unexpected project: %s", file.Project)
	}
}

func TestResolveSpecPathWritesInlineSpec(t *testing.T) {
	tempDir := t.TempDir()
	ctx := &runtimeContext{tempDir: tempDir}
	specContent := `{"files":[{"pattern":"dist/*.zip","target":"repo/path/"}]}`

	specPath, err := ctx.resolveSpecPath("", specContent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if specPath == "" {
		t.Fatal("expected temp spec path")
	}

	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("unexpected read error: %v", err)
	}
	if string(data) != specContent {
		t.Fatalf("unexpected spec file content: %s", string(data))
	}
	if filepath.Dir(specPath) != tempDir {
		t.Fatalf("expected spec in temp dir, got %s", specPath)
	}
}

func TestNormalizeHostToolErrorMaven(t *testing.T) {
	err := normalizeHostToolError("maven", &exec.Error{Name: "mvn", Err: exec.ErrNotFound})
	if err == nil {
		t.Fatal("expected error")
	}
	expected := "maven executable not found in PATH; install Maven or run inside the plugin container"
	if err.Error() != expected {
		t.Fatalf("unexpected error: %s", err)
	}
}

func TestNormalizeHostToolErrorGradle(t *testing.T) {
	sourceErr := errors.New("failed to find Gradle executable: gradle executable not found: neither wrapper in /workspace nor system gradle in PATH")
	err := normalizeHostToolError("gradle", sourceErr)
	if err == nil {
		t.Fatal("expected error")
	}
	expected := "gradle executable not found; neither a gradlew wrapper nor system gradle in PATH was available. Install Gradle, add a wrapper, or run inside the plugin container"
	if err.Error() != expected {
		t.Fatalf("unexpected error: %s", err)
	}
}
