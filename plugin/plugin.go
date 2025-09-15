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

    artifactory "github.com/jfrog/jfrog-client-go/artifactory"
    services "github.com/jfrog/jfrog-client-go/artifactory/services"
    artutils "github.com/jfrog/jfrog-client-go/artifactory/services/utils"
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
    CleanupAfterPublish string `envconfig:"PLUGIN_CLEANUP_AFTER_PUBLISH"`
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

    // write code here
    if args.URL == "" {
        return fmt.Errorf("JFrog Artifactory URL must be set, or anonymous access is not permitted")
    }

    if err := runUpload(args); err != nil {
        return err
    }

    // Publish build-info if requested.
    if args.PublishBuildInfo {
        if err := publishBuildInfo(args); err != nil {
            return err
        }
    }

    return nil
}

func publishBuildInfo(args Args) error {
    if args.BuildName == "" || args.BuildNumber == "" {
        return fmt.Errorf("both build name and build number need to be set when publishing build info")
    }

    sanitizedURL, err := sanitizeURL(args.URL)
    if err != nil {
        return err
    }

    // Aggregate partials and publish via SDK
    args.URL = sanitizedURL
    rt, cleanup, err := createServiceManager(args)
    if err != nil {
        return err
    }
    defer cleanup()
    bim := NewBuildInfoManager(rt)
    if err := bim.PublishAggregated(args); err != nil {
        return fmt.Errorf("error publishing build info: %s", err)
    }
    // Optional auto-cleanup after successful publish
    if parseBoolOrDefault(false, args.CleanupAfterPublish) {
        if err := bim.Clean(args); err != nil {
            logrus.Printf("warning: failed to cleanup build-info cache: %v", err)
        }
    }
    return nil
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

// getShell, getJfrogBin and getEnvPrefix are no longer needed once CLI usage is fully removed.
// They are intentionally removed in favor of jfrog-client-go.

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
    // retained for other shell-based flows (maven/gradle); to be removed when refactoring them
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

// runUploadWithSDK performs generic upload using jfrog-client-go.
func runUpload(args Args) error {
    rt, cleanup, err := createServiceManager(args)
    if err != nil {
        return err
    }
    defer cleanup()

    var params []services.UploadParams
    // Build params from spec or source/target
    if args.Spec != "" || args.SpecPath != "" {
        var fs *FileSpec
        if args.Spec != "" {
            content := applySpecVars(args.Spec, args.SpecVars)
            fs, err = parseFileSpecFromString(content)
        } else {
            b, rErr := os.ReadFile(args.SpecPath)
            if rErr != nil {
                return rErr
            }
            content := applySpecVars(string(b), args.SpecVars)
            fs, err = parseFileSpecFromString(content)
        }
        if err != nil {
            return err
        }
        params, err = fs.ToUploadParams()
        if err != nil {
            return err
        }
    } else {
        if args.Source == "" {
            return fmt.Errorf("source file needs to be set")
        }
        if args.Target == "" {
            return fmt.Errorf("target path needs to be set")
        }
        p := services.NewUploadParams()
        p.Pattern = args.Source
        p.Target = args.Target
        // Target props
        if filtered := filterTargetProps(args.TargetProps); filtered != "" {
            pr := artutils.NewProperties()
            _ = pr.ParseAndAddProperties(filtered)
            p.TargetProps = pr
        }
        // Flat flag
        p.Flat = parseBoolOrDefault(false, args.Flat)
        // Default recursive true for patterns
        p.Recursive = true
        params = append(params, p)
    }

    // Perform upload with summary to collect artifacts for build-info
    summary, err := rt.UploadFilesWithSummary(artifactory.UploadServiceOptions{}, params...)
    if err != nil {
        return err
    }
    defer summary.Close()
    if summary.TotalFailed > 0 {
        return fmt.Errorf("%d of %d uploads failed", summary.TotalFailed, summary.TotalFailed+summary.TotalSucceeded)
    }
    logrus.Printf("Uploaded %d files successfully", summary.TotalSucceeded)

    // Save artifacts as partials for later publish
    artifacts, err := artutils.ConvertArtifactsDetailsToBuildInfoArtifacts(summary.ArtifactsDetailsReader)
    if err == nil && len(artifacts) > 0 {
        bim := NewBuildInfoManager(rt)
        if perr := bim.SaveArtifactsPartial(args, artifacts); perr != nil {
            logrus.Printf("warning: failed to save build-info partials: %v", perr)
        }
    }
    return nil
}

// getEnvPrefix is still needed for CLI fallbacks during migration.
func getEnvPrefix() string {
    if runtime.GOOS == "windows" {
        return "$Env:"
    }
    return "$"
}
