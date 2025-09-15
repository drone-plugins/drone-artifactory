package plugin

import (
    "bytes"
    "encoding/xml"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "runtime"
    "strings"

    "github.com/sirupsen/logrus"
)

var MavenRunCmdJsonTagToExeFlagMapStringItemList = []JsonTagToExeFlagMapStringItem{
	{"--build-name=", "PLUGIN_BUILD_NAME", false, false},
	{"--build-number=", "PLUGIN_BUILD_NUMBER", false, false},
	{"--detailed-summary=", "PLUGIN_DETAILED_SUMMARY", false, false},
	{"--format=", "PLUGIN_FORMAT", false, false},
	{"--insecure-tls=", "PLUGIN_INSECURE", false, false},
	{"--project=", "PLUGIN_PROJECT", false, false},
	{"--scan=", "PLUGIN_SCAN", false, false},
	{"--threads=", "PLUGIN_THREADS", false, false},
}

var MavenConfigCmdJsonTagToExeFlagMapStringItemList = []JsonTagToExeFlagMapStringItem{
	{"--exclude-patterns=", "PLUGIN_EXCLUDE_PATTERNS", false, false},
	{"--global=", "PLUGIN_GLOBAL", false, false},
	{"--include-patterns=", "PLUGIN_INCLUDE_PATTERNS", false, false},
	{"--repo-deploy-releases=", "PLUGIN_DEPLOY_RELEASE_REPO", false, false},
	{"--repo-deploy-snapshots=", "PLUGIN_DEPLOY_SNAPSHOT_REPO", false, false},
	{"--repo-resolve-releases=", "PLUGIN_RESOLVE_RELEASE_REPO", false, false},
	{"--repo-resolve-snapshots=", "PLUGIN_RESOLVE_SNAPSHOT_REPO", false, false},
	{"--server-id-deploy=", "PLUGIN_SERVER_ID_DEPLOY", false, false},
	{"--server-id-resolve=", "PLUGIN_RESOLVER_ID", false, false},
	{"--use-wrapper=", "PLUGIN_USE_WRAPPER", false, false},
}

func GetMavenBuildCommandArgs(args Args) ([][]string, error) {

	var cmdList [][]string

	jfrogConfigAddConfigCommandArgs, err := GetConfigAddConfigCommandArgs(args.ResolverId,
		args.Username, args.Password, args.URL, args.AccessToken, args.APIKey)
	if err != nil {
		return cmdList, err
	}

	mvnConfigCommandArgs := []string{MvnConfig}
	// Add necessary parameters for Windows to prevent all interactive prompts
	if runtime.GOOS == "windows" {
		// These parameters prevent all interactive prompts
		mvnConfigCommandArgs = append(mvnConfigCommandArgs, "--global=true")
		// Add server ID for deployment/resolution
		if args.ResolverId != "" {
			mvnConfigCommandArgs = append(mvnConfigCommandArgs, "--server-id-resolve="+args.ResolverId)
			mvnConfigCommandArgs = append(mvnConfigCommandArgs, "--server-id-deploy="+args.ResolverId)
		}
		// Add repos to prevent prompts
		// Must set both release and snapshot repos to prevent errors
		if args.ResolveReleaseRepo == "" {
			mvnConfigCommandArgs = append(mvnConfigCommandArgs, "--repo-resolve-releases=libs-release")
		}
		if args.ResolveSnapshotRepo == "" {
			mvnConfigCommandArgs = append(mvnConfigCommandArgs, "--repo-resolve-snapshots=libs-snapshot")
		}
		if args.DeployReleaseRepo == "" {
			mvnConfigCommandArgs = append(mvnConfigCommandArgs, "--repo-deploy-releases=libs-release-local")
		}
		if args.DeploySnapshotRepo == "" {
			mvnConfigCommandArgs = append(mvnConfigCommandArgs, "--repo-deploy-snapshots=libs-snapshot-local")
		}
	}

	err = PopulateArgs(&mvnConfigCommandArgs, &args, MavenConfigCmdJsonTagToExeFlagMapStringItemList)
	if err != nil {
		return cmdList, err
	}

	mvnRunCommandArgs := []string{MvnCmd, args.MvnGoals}
	err = PopulateArgs(&mvnRunCommandArgs, &args, MavenRunCmdJsonTagToExeFlagMapStringItemList)
	if err != nil {
		return cmdList, err
	}
	if len(args.MvnPomFile) > 0 {
		mvnRunCommandArgs = append(mvnRunCommandArgs, "-f "+args.MvnPomFile)
	}

	cmdList = append(cmdList, jfrogConfigAddConfigCommandArgs)
	cmdList = append(cmdList, mvnConfigCommandArgs)
	cmdList = append(cmdList, mvnRunCommandArgs)

	return cmdList, nil
}

var RtBuildInfoPublishCmdJsonTagToExeFlagMap = []JsonTagToExeFlagMapStringItem{
	{"--project=", "PLUGIN_PROJECT", false, false},
}

func GetMavenPublishCommand(args Args) ([][]string, error) {
	var cmdList [][]string
	var jfrogConfigAddConfigCommandArgs []string

	tmpServerId := args.DeployerId
	jfrogConfigAddConfigCommandArgs, err := GetConfigAddConfigCommandArgs(tmpServerId,
		args.Username, args.Password, args.URL, args.AccessToken, args.APIKey)
	if err != nil {
		logrus.Println("GetConfigAddConfigCommandArgs error: ", err)
		return cmdList, err
	}

	mvnConfigCommandArgs := []string{MvnConfig}
	err = PopulateArgs(&mvnConfigCommandArgs, &args, MavenConfigCmdJsonTagToExeFlagMapStringItemList)
	if err != nil {
		logrus.Println("mvnConfigCommandArgs PopulateArgs error: ", err)
		return cmdList, err
	}

	rtPublishCommandArgs := []string{MvnCmd, Deploy,
		"--build-name=" + args.BuildName, "--build-number=" + args.BuildNumber}
	err = PopulateArgs(&rtPublishCommandArgs, &args, RtBuildInfoPublishCmdJsonTagToExeFlagMap)
	if err != nil {
		logrus.Println("rtPublishCommandArgs PopulateArgs error: ", err)
		return cmdList, err
	}

	rtPublishBuildInfoCommandArgs := []string{"rt", BuildPublish, args.BuildName, args.BuildNumber,
		"--server-id=" + tmpServerId}
	err = PopulateArgs(&rtPublishBuildInfoCommandArgs, &args, RtBuildInfoPublishCmdJsonTagToExeFlagMap)
	if err != nil {
		logrus.Println("PopulateArgs error: ", err)
		return cmdList, err
	}

	cmdList = append(cmdList, jfrogConfigAddConfigCommandArgs)
	cmdList = append(cmdList, mvnConfigCommandArgs)
	cmdList = append(cmdList, rtPublishCommandArgs)
	cmdList = append(cmdList, rtPublishBuildInfoCommandArgs)

	if IsBuildDiscardArgs(args) {
		buildDiscardBuildArgsList, err := GetBuildDiscardCommandArgs(args)
		if err != nil {
			logrus.Println("GetBuildDiscardCommandArgs error: ", err)
			return cmdList, err
		}
		cmdList = append(cmdList, buildDiscardBuildArgsList...)
	}
	return cmdList, nil
}

// --- Maven SDK-backed flows ---

func runMavenBuildSDK(args Args) error {
    settingsPath, cleanup, err := generateMavenSettings(args, false)
    if err != nil {
        return err
    }
    defer cleanup()

    goals := strings.TrimSpace(args.MvnGoals)
    if goals == "" {
        goals = "clean install"
    }

    cmdArgs := []string{"-s", settingsPath}
    if args.MvnPomFile != "" {
        cmdArgs = append(cmdArgs, "-f", args.MvnPomFile)
    }
    cmdArgs = append(cmdArgs, strings.Split(goals, " ")...)

    cmd := exec.Command("mvn", cmdArgs...)
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    logrus.Printf("+ mvn %s", strings.Join(cmdArgs, " "))
    return cmd.Run()
}

func runMavenPublishSDK(args Args) error {
    // Generate settings with server credentials and resolver repos
    settingsPath, cleanup, err := generateMavenSettings(args, true)
    if err != nil {
        return err
    }
    defer cleanup()

    // Determine repo for altDeploymentRepository based on POM version (-SNAPSHOT)
    pomPath := args.MvnPomFile
    if pomPath == "" {
        pomPath = "pom.xml"
    }
    version, err := readMavenProjectVersion(pomPath)
    if err != nil {
        return fmt.Errorf("failed reading maven version: %w", err)
    }
    isSnapshot := strings.Contains(strings.ToUpper(version), "SNAPSHOT")

    // Choose repo by version
    targetRepo := args.DeployReleaseRepo
    if isSnapshot && args.DeploySnapshotRepo != "" {
        targetRepo = args.DeploySnapshotRepo
    }
    if targetRepo == "" {
        return fmt.Errorf("deploy repo not provided (release/snapshot)")
    }

    // Build full repo URL from sanitized base
    sanitized, err := sanitizeURL(args.URL)
    if err != nil {
        return err
    }
    // Ensure sanitized ends with "/"
    if !strings.HasSuffix(sanitized, "/") {
        sanitized += "/"
    }
    repoURL := sanitized + strings.TrimLeft(targetRepo, "/")

    // Use server id 'artifactory' for credentials lookup
    alt := fmt.Sprintf("artifactory::default::%s", repoURL)

    goals := strings.TrimSpace(args.MvnGoals)
    if goals == "" {
        goals = "deploy"
    }

    cmdArgs := []string{"-s", settingsPath, "-DaltDeploymentRepository=" + alt}
    if args.MvnPomFile != "" {
        cmdArgs = append(cmdArgs, "-f", args.MvnPomFile)
    }
    cmdArgs = append(cmdArgs, strings.Split(goals, " ")...)

    cmd := exec.Command("mvn", cmdArgs...)
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    logrus.Printf("+ mvn %s", strings.Join(cmdArgs, " "))
    if err := cmd.Run(); err != nil {
        return err
    }
    return nil
}

// generateMavenSettings writes a temporary settings.xml with server credentials and resolver repos.
// If withServer is true, creates a <server> with id "artifactory" using args credentials.
func generateMavenSettings(args Args, withServer bool) (string, func(), error) {
    type Server struct {
        ID       string `xml:"id"`
        Username string `xml:"username,omitempty"`
        Password string `xml:"password,omitempty"`
    }
    type RepositoryPolicy struct {
        Enabled string `xml:"enabled"`
    }
    type Repository struct {
        ID       string           `xml:"id"`
        URL      string           `xml:"url"`
        Releases *RepositoryPolicy `xml:"releases,omitempty"`
        Snapshots *RepositoryPolicy `xml:"snapshots,omitempty"`
    }
    type Repositories struct {
        Repositories []Repository `xml:"repository"`
    }
    type Profile struct {
        ID           string       `xml:"id"`
        Repositories *Repositories `xml:"repositories,omitempty"`
        PluginRepos  *Repositories `xml:"pluginRepositories,omitempty"`
    }
    type Profiles struct {
        Profiles []Profile `xml:"profile"`
    }
    type ActiveProfiles struct {
        ActiveProfile []string `xml:"activeProfile"`
    }
    type Servers struct {
        Servers []Server `xml:"server"`
    }
    type Settings struct {
        XMLName        xml.Name       `xml:"settings"`
        XMLNS          string         `xml:"xmlns,attr,omitempty"`
        XSI            string         `xml:"xmlns:xsi,attr,omitempty"`
        SchemaLocation string         `xml:"xsi:schemaLocation,attr,omitempty"`
        Servers        *Servers       `xml:"servers,omitempty"`
        Profiles       *Profiles      `xml:"profiles,omitempty"`
        ActiveProfiles *ActiveProfiles `xml:"activeProfiles,omitempty"`
    }

    // Build repositories list for resolution
    sanitized, err := sanitizeURL(args.URL)
    if err != nil {
        return "", func() {}, err
    }
    if !strings.HasSuffix(sanitized, "/") {
        sanitized += "/"
    }
    repos := []Repository{}
    if args.ResolveReleaseRepo != "" {
        repos = append(repos, Repository{
            ID:  "artifactory-releases",
            URL: sanitized + strings.TrimLeft(args.ResolveReleaseRepo, "/"),
            Releases:  &RepositoryPolicy{Enabled: "true"},
            Snapshots: &RepositoryPolicy{Enabled: "false"},
        })
    }
    if args.ResolveSnapshotRepo != "" {
        repos = append(repos, Repository{
            ID:  "artifactory-snapshots",
            URL: sanitized + strings.TrimLeft(args.ResolveSnapshotRepo, "/"),
            Releases:  &RepositoryPolicy{Enabled: "false"},
            Snapshots: &RepositoryPolicy{Enabled: "true"},
        })
    }

    settings := &Settings{}

    if withServer {
        // Use server id 'artifactory'
        var user, pass string
        if args.AccessToken != "" && args.Username != "" {
            user = args.Username
            pass = args.AccessToken
        } else {
            user = args.Username
            pass = args.Password
        }
        settings.Servers = &Servers{Servers: []Server{{ID: "artifactory", Username: user, Password: pass}}}
    }

    if len(repos) > 0 {
        prof := Profile{ID: "artifactory", Repositories: &Repositories{Repositories: repos}, PluginRepos: &Repositories{Repositories: repos}}
        settings.Profiles = &Profiles{Profiles: []Profile{prof}}
        settings.ActiveProfiles = &ActiveProfiles{ActiveProfile: []string{"artifactory"}}
    }

    // Write settings to temp file
    tmpDir, err := os.MkdirTemp("", "mvn-settings-")
    if err != nil {
        return "", func() {}, err
    }
    path := filepath.Join(tmpDir, "settings.xml")
    buf := &bytes.Buffer{}
    buf.WriteString(xml.Header)
    enc := xml.NewEncoder(buf)
    enc.Indent("", "  ")
    if err := enc.Encode(settings); err != nil {
        return "", func() {}, err
    }
    if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
        return "", func() {}, err
    }
    cleanup := func() {
        _ = os.RemoveAll(tmpDir)
    }
    return path, cleanup, nil
}

// readMavenProjectVersion extracts <version> from pom.xml (falls back to parent version if needed).
func readMavenProjectVersion(pomPath string) (string, error) {
    type Parent struct{ Version string `xml:"version"` }
    type Project struct {
        Version string `xml:"version"`
        Parent  *Parent `xml:"parent"`
    }
    data, err := os.ReadFile(pomPath)
    if err != nil {
        return "", err
    }
    var p Project
    if err := xml.Unmarshal(data, &p); err != nil {
        return "", err
    }
    if p.Version != "" {
        return p.Version, nil
    }
    if p.Parent != nil && p.Parent.Version != "" {
        return p.Parent.Version, nil
    }
    return "", fmt.Errorf("version not found in %s", pomPath)
}
