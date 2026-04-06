// Copyright 2020 the Drone Authors. All rights reserved.
// Use of this source code is governed by the Blue Oak Model License
// that can be found in the LICENSE file.

package plugin

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
)

const (
	harnessHTTPProxy  = "HARNESS_HTTP_PROXY"
	harnessHTTPSProxy = "HARNESS_HTTPS_PROXY"
	harnessNoProxy    = "HARNESS_NO_PROXY"
	httpProxy         = "HTTP_PROXY"
	httpsProxy        = "HTTPS_PROXY"
	noProxy           = "NO_PROXY"
)

// Args provides plugin execution arguments.
type Args struct {
	Pipeline

	// Level defines the plugin log level.
	Level string `envconfig:"PLUGIN_LOG_LEVEL"`

	// TODO replace or remove
	Username         string `envconfig:"PLUGIN_USERNAME"`
	Password         string `envconfig:"PLUGIN_PASSWORD"`
	APIKey           string `envconfig:"PLUGIN_API_KEY"`
	AccessToken      string `envconfig:"PLUGIN_ACCESS_TOKEN"`
	URL              string `envconfig:"PLUGIN_URL"`
	Source           string `envconfig:"PLUGIN_SOURCE"`
	Target           string `envconfig:"PLUGIN_TARGET"`
	Retries          int    `envconfig:"PLUGIN_RETRIES"`
	Flat             string `envconfig:"PLUGIN_FLAT"`
	Spec             string `envconfig:"PLUGIN_SPEC"`
	Threads          int    `envconfig:"PLUGIN_THREADS"`
	SpecVars         string `envconfig:"PLUGIN_SPEC_VARS"`
	TargetProps      string `envconfig:"PLUGIN_TARGET_PROPS"`
	Insecure         string `envconfig:"PLUGIN_INSECURE"`
	PEMFileContents  string `envconfig:"PLUGIN_PEM_FILE_CONTENTS"`
	PEMFilePath      string `envconfig:"PLUGIN_PEM_FILE_PATH"`
	BuildNumber      string `envconfig:"PLUGIN_BUILD_NUMBER"`
	BuildName        string `envconfig:"PLUGIN_BUILD_NAME"`
	PublishBuildInfo bool   `envconfig:"PLUGIN_PUBLISH_BUILD_INFO"`
	EnableProxy      string `envconfig:"PLUGIN_ENABLE_PROXY"`

	// RT commands
	BuildTool string `envconfig:"PLUGIN_BUILD_TOOL"`
	Command   string `envconfig:"PLUGIN_COMMAND"`

	// Mvn commands
	ResolveReleaseRepo  string `envconfig:"PLUGIN_RESOLVE_RELEASE_REPO"`
	ResolveSnapshotRepo string `envconfig:"PLUGIN_RESOLVE_SNAPSHOT_REPO"`
	DeployReleaseRepo   string `envconfig:"PLUGIN_DEPLOY_RELEASE_REPO"`
	DeploySnapshotRepo  string `envconfig:"PLUGIN_DEPLOY_SNAPSHOT_REPO"`
	DeployRepo          string `envconfig:"PLUGIN_DEPLOY_REPO"`
	MvnGoals            string `envconfig:"PLUGIN_GOALS"`
	MvnPomFile          string `envconfig:"PLUGIN_POM_FILE"`
	DeployerId          string `envconfig:"PLUGIN_DEPLOYER_ID"`
	ResolverId          string `envconfig:"PLUGIN_RESOLVER_ID"`

	// Gradle commands
	GradleTasks string `envconfig:"PLUGIN_TASKS"`
	BuildFile   string `envconfig:"PLUGIN_BUILD_FILE"`
	RepoDeploy  string `envconfig:"PLUGIN_REPO_DEPLOY"`
	RepoResolve string `envconfig:"PLUGIN_REPO_RESOLVE"`

	// Upload Download commands
	SpecPath string `envconfig:"PLUGIN_SPEC_PATH"`
	Module   string `envconfig:"PLUGIN_MODULE"`
	Project  string `envconfig:"PLUGIN_PROJECT"`

	// Promote commands
	Copy string `envconfig:"PLUGIN_COPY"`

	// Add Dependencies to build commands
	Exclusions        string `envconfig:"PLUGIN_EXCLUSIONS"`
	FromRt            string `envconfig:"PLUGIN_FROM_RT"`
	Recursive         string `envconfig:"PLUGIN_RECURSIVE"`
	Regexp            string `envconfig:"PLUGIN_REGEXP"`
	DependencyPattern string `envconfig:"PLUGIN_DEPENDENCY"`

	// Build Discard commands
	Async           string `envconfig:"PLUGIN_ASYNC"`
	DeleteArtifacts string `envconfig:"PLUGIN_DELETE_ARTIFACTS"`
	ExcludeBuilds   string `envconfig:"PLUGIN_EXCLUDE_BUILDS"`
	MaxBuilds       string `envconfig:"PLUGIN_MAX_BUILDS"`
	MaxDays         string `envconfig:"PLUGIN_MAX_DAYS"`
}

// Exec executes the plugin.
func Exec(ctx context.Context, args Args) error {
	_ = ctx

	logrus.Println("Checking RT commands")
	if args.BuildTool != "" || args.Command != "" {
		logrus.Println("Handling rt command handleRtCommand")
		return HandleRtCommands(args)
	}

	enableProxy := parseBoolOrDefault(false, args.EnableProxy)
	if enableProxy {
		logrus.Printf("setting proxy config for upload")
		setSecureConnectProxies()
	}

	runtimeCtx, err := newRuntimeContext(args)
	if err != nil {
		return err
	}
	defer runtimeCtx.Close()

	return runtimeCtx.runDefaultUpload()
}

func publishBuildInfo(args Args) error {
	runtimeCtx, err := newRuntimeContext(args)
	if err != nil {
		return err
	}
	defer runtimeCtx.Close()

	return runtimeCtx.publishBuildInfo()
}

// Function to filter TargetProps based on criteria
func filterTargetProps(rawProps string) string {
	keyValuePairs := strings.Split(rawProps, ",")
	validPairs := []string{}

	for _, pair := range keyValuePairs {
		keyValuePair := strings.SplitN(pair, "=", 2)
		if len(keyValuePair) != 2 {
			continue // skip if it's not a valid key-value pair
		}

		key := strings.TrimSpace(keyValuePair[0])
		value := strings.TrimSpace(keyValuePair[1])

		// Remove single or double quotes from value
		trimmedValue := strings.Trim(value, "\"'")

		// Check value is not empty, not "null", and not just whitespace
		if trimmedValue != "" && strings.ToLower(trimmedValue) != "null" {
			validPairs = append(validPairs, key+"="+value)
		}
	}

	return strings.Join(validPairs, ",")
}

// sanitizeURL trims the URL to include only up to the '/artifactory/' path.
func sanitizeURL(inputURL string) (string, error) {
	parsedURL, err := url.Parse(inputURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %s", inputURL)
	}
	if parsedURL.Scheme == "" || parsedURL.Host == "" {
		return "", fmt.Errorf("invalid URL: %s", inputURL)
	}
	parts := strings.Split(parsedURL.Path, "/artifactory")
	if len(parts) < 2 {
		return "", fmt.Errorf("url does not contain '/artifactory': %s", inputURL)
	}

	// Always set the path to the first part + "/artifactory/"
	parsedURL.Path = parts[0] + "/artifactory/"

	return parsedURL.String(), nil
}

// setAuthParams appends authentication parameters to cmdArgs based on the provided credentials.
func setAuthParams(cmdArgs []string, args Args) ([]string, error) {
	// Set authentication params
	envPrefix := getEnvPrefix()
	if args.Username != "" && args.Password != "" {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--user %sPLUGIN_USERNAME", envPrefix))
		cmdArgs = append(cmdArgs, fmt.Sprintf("--password %sPLUGIN_PASSWORD", envPrefix))
	} else if args.APIKey != "" {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--apikey %sPLUGIN_API_KEY", envPrefix))
	} else if args.AccessToken != "" {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--access-token %sPLUGIN_ACCESS_TOKEN", envPrefix))
	} else {
		return nil, fmt.Errorf("either username/password, api key or access token needs to be set")
	}
	return cmdArgs, nil
}

func getShell() (string, string) {
	if runtime.GOOS == "windows" {
		// First check for PowerShell Core (pwsh.exe) which is used in PowerShell Nanoserver
		if _, err := os.Stat("C:/Program Files/PowerShell/pwsh.exe"); err == nil {
			return "pwsh", "-Command"
		}

		// Fall back to traditional PowerShell
		return "powershell", "-Command"
	}

	return "sh", "-c"
}

func getJfrogBin() string {
	if runtime.GOOS == "windows" {
		if _, err := os.Stat("C:/bin/jfrog.exe"); err == nil {
			return "C:/bin/jfrog.exe"
		}
	}
	return "jf"
}

func getEnvPrefix() string {
	if runtime.GOOS == "windows" {
		return "$Env:"
	}
	return "$"
}

func parseBoolOrDefault(defaultValue bool, s string) (result bool) {
	var err error
	result, err = strconv.ParseBool(s)
	if err != nil {
		result = defaultValue
	}

	return
}

// trace writes each command to stdout with the command wrapped in an xml
// tag so that it can be extracted and displayed in the logs.
func trace(cmd *exec.Cmd) {
	fmt.Fprintf(os.Stdout, "+ %s\n", strings.Join(cmd.Args, " "))
}

func setSecureConnectProxies() {
	copyEnvVariableIfExists(harnessHTTPProxy, httpProxy)
	copyEnvVariableIfExists(harnessHTTPSProxy, httpsProxy)
	copyEnvVariableIfExists(harnessNoProxy, noProxy)
}

func copyEnvVariableIfExists(src string, dest string) {
	srcValue := os.Getenv(src)
	if srcValue == "" {
		return
	}
	err := os.Setenv(dest, srcValue)
	if err != nil {
		logrus.Printf("Failed to copy env variable from %s to %s with error %v", src, dest, err)
	}
}
