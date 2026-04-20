package plugin

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	jfrogProject "github.com/jfrog/jfrog-cli-core/v2/common/project"
	artifactory "github.com/jfrog/jfrog-client-go/artifactory"
	"github.com/sirupsen/logrus"
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

func TestBuildArtAuthDetailsUsesApiKeyField(t *testing.T) {
	ctx := &runtimeContext{
		args: Args{
			URL:      RtUrlTestStr,
			Username: "user",
			APIKey:   "api-key",
		},
	}

	details, err := ctx.buildArtAuthDetails()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if details.GetUser() != "user" {
		t.Fatalf("unexpected user: %s", details.GetUser())
	}
	if details.GetApiKey() != "api-key" {
		t.Fatalf("unexpected api key: %s", details.GetApiKey())
	}
	if details.GetAccessToken() != "" {
		t.Fatalf("expected empty access token, got %q", details.GetAccessToken())
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

func TestDownloadSpecPreservesBuildReferenceWithModule(t *testing.T) {
	ctx := &runtimeContext{
		args: Args{
			Source:      "/tmp/out",
			Target:      "libs-release-local/app/*.tgz",
			BuildName:   "build-name",
			BuildNumber: "42",
			Module:      "module-a",
			Project:     "proj",
		},
	}

	specFiles, err := ctx.downloadSpec()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	file := specFiles.Get(0)
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

func TestRuntimeContextCloseJoinsErrors(t *testing.T) {
	ctx := &runtimeContext{
		tempDir: "unused",
		restoreHome: func() error {
			return errors.New("restore failed")
		},
		removeTemp: func(string) error {
			return errors.New("remove failed")
		},
	}

	err := ctx.Close()
	if err == nil {
		t.Fatal("expected error")
	}
	if !stringsContainAll(err.Error(), "restore failed", "remove failed") {
		t.Fatalf("expected joined error, got %q", err.Error())
	}
}

func TestRunDefaultUploadReturnsCanceledBeforeManagerCreation(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	ctx := &runtimeContext{
		ctx:  canceled,
		args: Args{},
		newManager: func(bool, int) (artifactory.ArtifactoryServicesManager, error) {
			t.Fatal("service manager should not be created")
			return nil, nil
		},
	}

	err := ctx.runDefaultUpload()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestPublishBuildInfoReturnsCanceledBeforeManagerCreation(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	ctx := &runtimeContext{
		ctx:  canceled,
		args: Args{BuildName: "build", BuildNumber: "42"},
		newManager: func(bool, int) (artifactory.ArtifactoryServicesManager, error) {
			t.Fatal("service manager should not be created")
			return nil, nil
		},
	}

	err := ctx.publishBuildInfo()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestRunMavenReturnsCanceledBeforeWrapperLaunch(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	ctx := &runtimeContext{ctx: canceled}
	err := ctx.runMaven(false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestShellSplitBestEffortParsesQuotedInput(t *testing.T) {
	got := shellSplitBestEffort(`clean deploy -Dprop="hello world"`)
	want := []string{"clean", "deploy", "-Dprop=hello world"}
	if len(got) != len(want) {
		t.Fatalf("unexpected arg count: got %d want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected arg at %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestShellSplitBestEffortFallsBackAndWarns(t *testing.T) {
	var logBuffer bytes.Buffer
	previousOut := logrus.StandardLogger().Out
	previousLevel := logrus.GetLevel()
	logrus.SetOutput(&logBuffer)
	logrus.SetLevel(logrus.WarnLevel)
	t.Cleanup(func() {
		logrus.SetOutput(previousOut)
		logrus.SetLevel(previousLevel)
	})

	got := shellSplitBestEffort(`clean "unterminated value`)
	want := []string{"clean", "\"unterminated", "value"}
	if len(got) != len(want) {
		t.Fatalf("unexpected arg count: got %d want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected arg at %d: got %q want %q", i, got[i], want[i])
		}
	}
	if !stringsContainAll(logBuffer.String(), "failed to parse quoted arguments with shlex", "falling back to whitespace splitting") {
		t.Fatalf("expected warning log, got %q", logBuffer.String())
	}
}

func TestMavenConfigWindowsDefaults(t *testing.T) {
	restore := setTestGOOS(t, "windows")
	defer restore()

	ctx := &runtimeContext{}
	cfg := ctx.mavenConfig("resolver-id", "deployer-id")

	if cfg.Resolver.ServerId != "resolver-id" {
		t.Fatalf("unexpected resolver server id: %s", cfg.Resolver.ServerId)
	}
	if cfg.Resolver.ReleaseRepo != "libs-release" || cfg.Resolver.SnapshotRepo != "libs-snapshot" {
		t.Fatalf("unexpected resolver defaults: %+v", cfg.Resolver)
	}
	if cfg.Deployer != (jfrogProject.Repository{}) {
		t.Fatalf("expected deployer to be cleared when no deploy repos are configured, got %+v", cfg.Deployer)
	}
}

func TestMavenConfigWindowsKeepsResolverAndDeployerInputs(t *testing.T) {
	restore := setTestGOOS(t, "windows")
	defer restore()

	ctx := &runtimeContext{
		args: Args{
			ResolveReleaseRepo:  "libs-rel",
			ResolveSnapshotRepo: "libs-snap",
			DeployRepo:          "libs-release-local",
		},
	}
	cfg := ctx.mavenConfig("resolver-id", "deployer-id")

	if cfg.Resolver.ReleaseRepo != "libs-rel" || cfg.Resolver.SnapshotRepo != "libs-snap" {
		t.Fatalf("unexpected resolver repos: %+v", cfg.Resolver)
	}
	if cfg.Deployer.ReleaseRepo != "libs-release-local" || cfg.Deployer.SnapshotRepo != "libs-release-local" {
		t.Fatalf("unexpected deployer repos: %+v", cfg.Deployer)
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

func stringsContainAll(value string, substrings ...string) bool {
	for _, substring := range substrings {
		if !strings.Contains(value, substring) {
			return false
		}
	}
	return true
}

func setTestGOOS(t *testing.T, value string) func() {
	t.Helper()
	previous := currentGOOS
	currentGOOS = value
	return func() {
		currentGOOS = previous
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
