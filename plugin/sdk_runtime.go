package plugin

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/google/shlex"
	buildinfoCmd "github.com/jfrog/jfrog-cli-artifactory/artifactory/commands/buildinfo"
	genericCmd "github.com/jfrog/jfrog-cli-artifactory/artifactory/commands/generic"
	gradleCmd "github.com/jfrog/jfrog-cli-artifactory/artifactory/commands/gradle"
	mvnCmd "github.com/jfrog/jfrog-cli-artifactory/artifactory/commands/mvn"
	rtUtils "github.com/jfrog/jfrog-cli-core/v2/artifactory/utils"
	jfrogBuild "github.com/jfrog/jfrog-cli-core/v2/common/build"
	commonCliUtils "github.com/jfrog/jfrog-cli-core/v2/common/cliutils"
	jfrogProject "github.com/jfrog/jfrog-cli-core/v2/common/project"
	jfrogSpec "github.com/jfrog/jfrog-cli-core/v2/common/spec"
	jfrogConfig "github.com/jfrog/jfrog-cli-core/v2/utils/config"
	"github.com/jfrog/jfrog-cli-core/v2/utils/coreutils"
	artBuildInfo "github.com/jfrog/jfrog-client-go/artifactory/buildinfo"
	"github.com/jfrog/jfrog-client-go/artifactory/services"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const (
	jfrogHomeDirEnv = "JFROG_CLI_HOME_DIR"
)

type runtimeContext struct {
	args         Args
	tempDir      string
	homeDir      string
	projectDir   string
	restoreHome  func() error
	tempSpecPath string
}

type projectConfigFile struct {
	Version    int                     `yaml:"version,omitempty"`
	ConfigType string                  `yaml:"type,omitempty"`
	Resolver   jfrogProject.Repository `yaml:"resolver,omitempty"`
	Deployer   jfrogProject.Repository `yaml:"deployer,omitempty"`
	UsePlugin  bool                    `yaml:"usePlugin,omitempty"`
	UseWrapper bool                    `yaml:"useWrapper,omitempty"`
}

func newRuntimeContext(args Args) (*runtimeContext, error) {
	ctx := &runtimeContext{args: args}

	tempDir, err := os.MkdirTemp("", "drone-artifactory-*")
	if err != nil {
		return nil, err
	}
	ctx.tempDir = tempDir
	ctx.homeDir = filepath.Join(tempDir, ".jfrog")
	ctx.projectDir = filepath.Join(tempDir, "projects")

	if err := os.MkdirAll(filepath.Join(ctx.homeDir, "security", "certs"), 0o700); err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, err
	}
	if err := os.MkdirAll(ctx.projectDir, 0o700); err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, err
	}

	if err := ctx.seedRuntimeCerts(); err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, err
	}

	if err := ctx.installPemCertificates(); err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, err
	}

	ctx.restoreHome = setEnvWithRestore(jfrogHomeDirEnv, ctx.homeDir)
	return ctx, nil
}

func (ctx *runtimeContext) Close() error {
	var errs []error
	if ctx.restoreHome != nil {
		if err := ctx.restoreHome(); err != nil {
			errs = append(errs, err)
		}
	}
	if ctx.tempDir != "" {
		if err := os.RemoveAll(ctx.tempDir); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs[0]
}

func (ctx *runtimeContext) runDefaultUpload() error {
	if ctx.args.URL == "" {
		return fmt.Errorf("JFrog Artifactory URL must be set, or anonymous access is not permitted")
	}

	serverDetails, err := ctx.createServerDetails("")
	if err != nil {
		return err
	}
	specFiles, err := ctx.uploadSpec()
	if err != nil {
		return err
	}

	traceCommand("jf-sdk rt upload")
	uploadCmd := genericCmd.NewUploadCommand()
	uploadCmd.SetServerDetails(serverDetails)
	uploadCmd.SetSpec(specFiles)
	uploadCmd.SetBuildConfiguration(ctx.buildConfiguration())
	uploadCmd.SetDetailedSummary(false)
	uploadCmd.SetRetries(ctx.args.Retries)
	uploadCmd.SetRetryWaitMilliSecs(0)
	uploadCmd.SetUploadConfiguration(&rtUtils.UploadConfiguration{
		Threads: ctx.threadsOrDefault(),
	})

	if err := uploadCmd.Run(); err != nil {
		return err
	}

	if ctx.args.PublishBuildInfo {
		return ctx.publishBuildInfo()
	}
	return nil
}

func (ctx *runtimeContext) runRTCommand() error {
	switch {
	case ctx.args.BuildTool == MvnCmd && (ctx.args.Command == "" || ctx.args.Command == "build"):
		if err := ctx.runMaven(false); err != nil {
			return err
		}
		if ctx.args.PublishBuildInfo {
			return ctx.publishBuildInfo()
		}
		return nil
	case ctx.args.BuildTool == MvnCmd && ctx.args.Command == "publish":
		if err := ctx.runMaven(true); err != nil {
			return err
		}
		if IsBuildDiscardArgs(ctx.args) {
			return ctx.runBuildDiscard()
		}
		return nil
	case ctx.args.BuildTool == GradleCmd && (ctx.args.Command == "" || ctx.args.Command == "build"):
		if err := ctx.runGradle(false); err != nil {
			return err
		}
		if ctx.args.PublishBuildInfo {
			return ctx.publishBuildInfo()
		}
		return nil
	case ctx.args.BuildTool == GradleCmd && ctx.args.Command == "publish":
		if err := ctx.runGradle(true); err != nil {
			return err
		}
		if IsBuildDiscardArgs(ctx.args) {
			return ctx.runBuildDiscard()
		}
		return nil
	case ctx.args.Command == "download":
		if err := ctx.runDownload(); err != nil {
			return err
		}
		if ctx.args.PublishBuildInfo {
			return ctx.publishBuildInfo()
		}
		return nil
	case ctx.args.Command == "cleanup":
		return ctx.runBuildClean()
	case ctx.args.Command == "scan":
		return ctx.runBuildScan()
	case ctx.args.Command == "publish-build-info":
		return ctx.publishBuildInfo()
	case ctx.args.Command == "promote":
		return ctx.runPromote()
	case ctx.args.Command == "add-build-dependencies":
		if err := ctx.runAddDependencies(); err != nil {
			return err
		}
		return ctx.publishBuildInfo()
	case ctx.args.Command == "build-discard":
		return ctx.runBuildDiscard()
	default:
		return fmt.Errorf("unsupported build tool / command combination: build_tool=%q command=%q", ctx.args.BuildTool, ctx.args.Command)
	}
}

func (ctx *runtimeContext) runDownload() error {
	serverDetails, err := ctx.createServerDetails("")
	if err != nil {
		return err
	}
	specFiles, err := ctx.downloadSpec()
	if err != nil {
		return err
	}

	traceCommand("jf-sdk rt download")
	downloadCmd := genericCmd.NewDownloadCommand()
	downloadCmd.SetServerDetails(serverDetails)
	downloadCmd.SetSpec(specFiles)
	downloadCmd.SetBuildConfiguration(ctx.buildConfiguration())
	downloadCmd.SetDetailedSummary(false)
	downloadCmd.SetRetries(ctx.args.Retries)
	downloadCmd.SetRetryWaitMilliSecs(0)
	downloadCmd.SetConfiguration(&rtUtils.DownloadConfiguration{
		Threads: ctx.threadsOrDefault(),
	})

	return downloadCmd.Run()
}

func (ctx *runtimeContext) publishBuildInfo() error {
	buildConfig := ctx.buildConfiguration()
	if err := buildConfig.ValidateBuildParams(); err != nil {
		return fmt.Errorf("both build name and build number need to be set when publishing build info")
	}

	serverDetails, err := ctx.createServerDetails("")
	if err != nil {
		return err
	}

	traceCommand("jf-sdk rt build-publish")
	publishCmd := buildinfoCmd.NewBuildPublishCommand()
	publishCmd.SetServerDetails(serverDetails).
		SetBuildConfiguration(buildConfig).
		SetConfig(&artBuildInfo.Configuration{})

	return publishCmd.Run()
}

func (ctx *runtimeContext) runBuildClean() error {
	traceCommand("jf-sdk rt build-clean")
	return buildinfoCmd.NewBuildCleanCommand().
		SetBuildConfiguration(ctx.buildConfiguration()).
		Run()
}

func (ctx *runtimeContext) runBuildScan() error {
	serverDetails, err := ctx.createServerDetails("")
	if err != nil {
		return err
	}

	traceCommand("jf-sdk rt build-scan")
	return buildinfoCmd.NewBuildScanLegacyCommand().
		SetServerDetails(serverDetails).
		SetBuildConfiguration(ctx.buildConfiguration()).
		SetFailBuild(false).
		Run()
}

func (ctx *runtimeContext) runPromote() error {
	serverDetails, err := ctx.createServerDetails("")
	if err != nil {
		return err
	}

	params := services.NewPromotionParams()
	params.TargetRepo = ctx.args.Target
	params.Copy = parseBoolOrDefault(false, ctx.args.Copy)

	traceCommand("jf-sdk rt build-promote")
	return buildinfoCmd.NewBuildPromotionCommand().
		SetServerDetails(serverDetails).
		SetBuildConfiguration(ctx.buildConfiguration()).
		SetPromotionParams(params).
		Run()
}

func (ctx *runtimeContext) runAddDependencies() error {
	specFiles, serverDetails, err := ctx.buildDependenciesSpecAndServer()
	if err != nil {
		return err
	}

	traceCommand("jf-sdk rt build-add-dependencies")
	cmd := buildinfoCmd.NewBuildAddDependenciesCommand().
		SetBuildConfiguration(ctx.buildConfiguration()).
		SetDependenciesSpec(specFiles)
	if serverDetails != nil {
		cmd.SetServerDetails(serverDetails)
	}
	return cmd.Run()
}

func (ctx *runtimeContext) runBuildDiscard() error {
	serverDetails, err := ctx.createServerDetails("")
	if err != nil {
		return err
	}

	params := services.NewDiscardBuildsParams()
	params.BuildName = ctx.args.BuildName
	params.ProjectKey = ctx.args.Project
	params.MaxDays = ctx.args.MaxDays
	params.MaxBuilds = ctx.args.MaxBuilds
	params.ExcludeBuilds = ctx.args.ExcludeBuilds
	params.Async = parseBoolOrDefault(false, ctx.args.Async)
	params.DeleteArtifacts = parseBoolOrDefault(false, ctx.args.DeleteArtifacts)

	traceCommand("jf-sdk rt build-discard")
	return buildinfoCmd.NewBuildDiscardCommand().
		SetServerDetails(serverDetails).
		SetDiscardBuildsParams(params).
		Run()
}

func (ctx *runtimeContext) runMaven(publish bool) error {
	resolveServerID := ctx.args.ResolverId
	if resolveServerID == "" {
		resolveServerID = tmpServerId
	}
	deployServerID := ctx.args.DeployerId
	if deployServerID == "" {
		deployServerID = resolveServerID
	}

	serverIDs := []string{resolveServerID}
	if deployServerID != resolveServerID {
		serverIDs = append(serverIDs, deployServerID)
	}
	if err := ctx.registerServers(serverIDs...); err != nil {
		return err
	}

	configPath, err := ctx.writeProjectConfig("maven.yaml", ctx.mavenConfig(resolveServerID, deployServerID))
	if err != nil {
		return err
	}

	traceCommand("jf-sdk mvn")
	goals, err := shellSplit(ctx.args.MvnGoals)
	if err != nil {
		return err
	}
	if publish {
		goals = []string{Deploy}
	}
	if ctx.args.MvnPomFile != "" {
		goals = append(goals, "-f", ctx.args.MvnPomFile)
	}

	cmd := mvnCmd.NewMvnCommand().
		SetConfigPath(configPath).
		SetConfiguration(ctx.buildConfiguration()).
		SetGoals(goals).
		SetThreads(ctx.threadsOrDefault()).
		SetInsecureTls(parseBoolOrDefault(false, ctx.args.Insecure))

	if err := cmd.Run(); err != nil {
		return normalizeHostToolError("maven", err)
	}
	if publish {
		return ctx.publishBuildInfo()
	}
	return nil
}

func (ctx *runtimeContext) runGradle(publish bool) error {
	resolveServerID := ctx.args.ResolverId
	if resolveServerID == "" {
		resolveServerID = tmpServerId
	}
	deployServerID := ctx.args.DeployerId
	if deployServerID == "" {
		deployServerID = resolveServerID
	}

	serverIDs := []string{resolveServerID}
	if deployServerID != resolveServerID {
		serverIDs = append(serverIDs, deployServerID)
	}
	if err := ctx.registerServers(serverIDs...); err != nil {
		return err
	}

	configPath, err := ctx.writeProjectConfig("gradle.yaml", ctx.gradleConfig(resolveServerID, deployServerID, publish))
	if err != nil {
		return err
	}

	traceCommand("jf-sdk gradle")
	tasks, err := shellSplit(ctx.args.GradleTasks)
	if err != nil {
		return err
	}
	if publish {
		tasks = []string{Publish}
		switch {
		case ctx.args.Username != "":
			tasks = append(tasks, "-Pusername="+ctx.args.Username, "-Ppassword="+ctx.args.Password)
		case ctx.args.AccessToken != "":
			return fmt.Errorf("AccessToken is not supported for Gradle try username: <username> , password: <access_token> instead")
		case ctx.args.APIKey != "":
			return fmt.Errorf("API key is not supported for Gradle publish without username; use username/password or access token")
		}
	}
	if ctx.args.BuildFile != "" {
		tasks = append(tasks, "-b", ctx.args.BuildFile)
	}

	cmd := gradleCmd.NewGradleCommand().
		SetConfigPath(configPath).
		SetConfiguration(ctx.buildConfiguration()).
		SetTasks(tasks).
		SetThreads(ctx.threadsOrDefault())

	if err := cmd.Run(); err != nil {
		return normalizeHostToolError("gradle", err)
	}
	if publish {
		return ctx.publishBuildInfo()
	}
	return nil
}

func normalizeHostToolError(tool string, err error) error {
	if err == nil {
		return nil
	}

	lower := strings.ToLower(err.Error())
	switch tool {
	case "maven":
		if errors.Is(err, exec.ErrNotFound) ||
			strings.Contains(lower, `exec: "mvn"`) ||
			strings.Contains(lower, "mvn\": executable file not found") ||
			strings.Contains(lower, "mvn': executable file not found") ||
			strings.Contains(lower, "executable file not found") {
			return fmt.Errorf("maven executable not found in PATH; install Maven or run inside the plugin container")
		}
	case "gradle":
		if strings.Contains(lower, "failed to find gradle executable") ||
			strings.Contains(lower, "gradle executable not found") ||
			errors.Is(err, exec.ErrNotFound) ||
			strings.Contains(lower, `exec: "gradle"`) ||
			strings.Contains(lower, `exec: "gradlew"`) {
			return fmt.Errorf("gradle executable not found; neither a gradlew wrapper nor system gradle in PATH was available. Install Gradle, add a wrapper, or run inside the plugin container")
		}
	}
	return err
}

func (ctx *runtimeContext) buildConfiguration() *jfrogBuild.BuildConfiguration {
	return jfrogBuild.NewBuildConfiguration(ctx.args.BuildName, ctx.args.BuildNumber, ctx.args.Module, ctx.args.Project)
}

func (ctx *runtimeContext) createServerDetails(serverID string) (*jfrogConfig.ServerDetails, error) {
	sanitizedURL, err := sanitizeURL(ctx.args.URL)
	if err != nil {
		return nil, err
	}
	platformURL := strings.TrimSuffix(strings.TrimSuffix(sanitizedURL, "/artifactory/"), "/") + "/"

	serverDetails := &jfrogConfig.ServerDetails{
		ServerId:       serverID,
		Url:            platformURL,
		ArtifactoryUrl: sanitizedURL,
		InsecureTls:    parseBoolOrDefault(false, ctx.args.Insecure),
	}
	switch {
	case ctx.args.Username != "" && ctx.args.Password != "":
		serverDetails.User = ctx.args.Username
		serverDetails.Password = ctx.args.Password
	case ctx.args.APIKey != "":
		// The JFrog Go config type does not expose a dedicated API key field.
		// Passing the API key through AccessToken preserves client-go's API-key detection path.
		serverDetails.User = ctx.args.Username
		serverDetails.AccessToken = ctx.args.APIKey
	case ctx.args.AccessToken != "":
		serverDetails.User = ctx.args.Username
		serverDetails.AccessToken = ctx.args.AccessToken
	default:
		return nil, fmt.Errorf("either username/password, api key or access token needs to be set")
	}
	return serverDetails, nil
}

func (ctx *runtimeContext) registerServers(serverIDs ...string) error {
	configs := make([]*jfrogConfig.ServerDetails, 0, len(serverIDs))
	for _, serverID := range serverIDs {
		if serverID == "" {
			continue
		}
		serverDetails, err := ctx.createServerDetails(serverID)
		if err != nil {
			return err
		}
		configs = append(configs, serverDetails)
	}
	if len(configs) == 0 {
		return nil
	}
	return jfrogConfig.SaveServersConf(configs)
}

func (ctx *runtimeContext) writeProjectConfig(fileName string, cfg projectConfigFile) (string, error) {
	content, err := yaml.Marshal(cfg)
	if err != nil {
		return "", err
	}
	path := filepath.Join(ctx.projectDir, fileName)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (ctx *runtimeContext) mavenConfig(resolveServerID, deployServerID string) projectConfigFile {
	cfg := projectConfigFile{
		Version:    1,
		ConfigType: "maven",
		Resolver: jfrogProject.Repository{
			ServerId:     resolveServerID,
			ReleaseRepo:  ctx.args.ResolveReleaseRepo,
			SnapshotRepo: ctx.args.ResolveSnapshotRepo,
		},
		Deployer: jfrogProject.Repository{
			ServerId:     deployServerID,
			ReleaseRepo:  ctx.args.DeployReleaseRepo,
			SnapshotRepo: ctx.args.DeploySnapshotRepo,
		},
	}
	if runtime.GOOS == "windows" {
		if cfg.Resolver.ReleaseRepo == "" {
			cfg.Resolver.ReleaseRepo = "libs-release"
		}
		if cfg.Resolver.SnapshotRepo == "" {
			cfg.Resolver.SnapshotRepo = "libs-snapshot"
		}
		if cfg.Deployer.ReleaseRepo == "" {
			cfg.Deployer.ReleaseRepo = "libs-release-local"
		}
		if cfg.Deployer.SnapshotRepo == "" {
			cfg.Deployer.SnapshotRepo = "libs-snapshot-local"
		}
	}
	if ctx.args.DeployReleaseRepo == "" && ctx.args.DeploySnapshotRepo == "" && ctx.args.DeployRepo == "" {
		cfg.Deployer = jfrogProject.Repository{}
	}
	if ctx.args.ResolveReleaseRepo == "" && ctx.args.ResolveSnapshotRepo == "" {
		cfg.Resolver = jfrogProject.Repository{
			ServerId:     resolveServerID,
			ReleaseRepo:  cfg.Resolver.ReleaseRepo,
			SnapshotRepo: cfg.Resolver.SnapshotRepo,
		}
	}
	return cfg
}

func (ctx *runtimeContext) gradleConfig(resolveServerID, deployServerID string, publish bool) projectConfigFile {
	cfg := projectConfigFile{
		Version:    1,
		ConfigType: "gradle",
		Resolver: jfrogProject.Repository{
			ServerId: resolveServerID,
			Repo:     ctx.args.RepoResolve,
		},
		Deployer: jfrogProject.Repository{
			ServerId:         deployServerID,
			Repo:             ctx.args.RepoDeploy,
			ReleaseRepo:      ctx.args.DeployReleaseRepo,
			SnapshotRepo:     ctx.args.DeploySnapshotRepo,
			IncludePatterns:  "",
			ExcludePatterns:  "",
			DeployMavenDesc:  true,
			DeployIvyDesc:    true,
			IvyPattern:       "[organization]/[module]/ivy-[revision].xml",
			ArtifactsPattern: "[organization]/[module]/[revision]/[artifact]-[revision](-[classifier]).[ext]",
		},
	}
	if runtime.GOOS == "windows" && !publish {
		if cfg.Resolver.Repo == "" {
			cfg.Resolver.Repo = "libs-release"
		}
		if cfg.Deployer.Repo == "" {
			cfg.Deployer.Repo = "libs-release-local"
		}
		cfg.UsePlugin = true
	}
	if publish {
		cfg.Resolver.ServerId = deployServerID
		cfg.Deployer.ServerId = deployServerID
	}
	if ctx.args.RepoResolve == "" && ctx.args.ResolveReleaseRepo == "" && ctx.args.ResolveSnapshotRepo == "" {
		if runtime.GOOS != "windows" || publish {
			cfg.Resolver = jfrogProject.Repository{ServerId: cfg.Resolver.ServerId}
		}
	}
	return cfg
}

func (ctx *runtimeContext) uploadSpec() (*jfrogSpec.SpecFiles, error) {
	if ctx.args.SpecPath != "" || ctx.args.Spec != "" {
		specPath, err := ctx.resolveSpecPath(ctx.args.SpecPath, ctx.args.Spec)
		if err != nil {
			return nil, err
		}
		return jfrogSpec.CreateSpecFromFile(specPath, coreutils.SpecVarsStringToMap(ctx.args.SpecVars))
	}
	if ctx.args.Source == "" {
		return nil, fmt.Errorf("source file needs to be set")
	}
	if ctx.args.Target == "" {
		return nil, fmt.Errorf("target path needs to be set")
	}
	specFiles := jfrogSpec.NewBuilder().
		Pattern(ctx.args.Source).
		Target(strings.TrimPrefix(ctx.args.Target, "/")).
		Flat(parseBoolOrDefault(false, ctx.args.Flat)).
		TargetProps(filterTargetProps(ctx.args.TargetProps)).
		Recursive(true).
		BuildSpec()
	commonCliUtils.FixWinPathsForFileSystemSourcedCmds(specFiles, false, false)
	return specFiles, nil
}

func (ctx *runtimeContext) downloadSpec() (*jfrogSpec.SpecFiles, error) {
	if ctx.args.SpecPath != "" || ctx.args.Spec != "" {
		specPath, err := ctx.resolveSpecPath(ctx.args.SpecPath, ctx.args.Spec)
		if err != nil {
			return nil, err
		}
		specFiles, err := jfrogSpec.CreateSpecFromFile(specPath, coreutils.SpecVarsStringToMap(ctx.args.SpecVars))
		if err != nil {
			return nil, err
		}
		for i := range specFiles.Files {
			specFiles.Files[i].Pattern = strings.TrimPrefix(specFiles.Files[i].Pattern, "/")
		}
		return specFiles, nil
	}

	specFiles := jfrogSpec.NewBuilder().
		Pattern(strings.TrimPrefix(ctx.args.Target, "/")).
		Target(ctx.args.Source).
		Build(buildReference(ctx.args.BuildName, ctx.args.BuildNumber)).
		Project(ctx.args.Project).
		Recursive(true).
		Flat(false).
		BuildSpec()
	if ctx.args.Module != "" {
		specFiles.Files[0].Build = buildReference(ctx.args.BuildName, ctx.args.BuildNumber)
	}
	return specFiles, nil
}

func (ctx *runtimeContext) buildDependenciesSpecAndServer() (*jfrogSpec.SpecFiles, *jfrogConfig.ServerDetails, error) {
	specPath := ctx.args.SpecPath
	if specPath == "" && ctx.args.Spec != "" {
		resolvedPath, err := ctx.resolveSpecPath("", ctx.args.Spec)
		if err != nil {
			return nil, nil, err
		}
		specPath = resolvedPath
	}
	if specPath != "" {
		specFiles, err := jfrogSpec.CreateSpecFromFile(specPath, coreutils.SpecVarsStringToMap(ctx.args.SpecVars))
		if err != nil {
			return nil, nil, err
		}
		if parseBoolOrDefault(false, ctx.args.FromRt) {
			serverDetails, err := ctx.createServerDetails("")
			if err != nil {
				return nil, nil, err
			}
			return specFiles, serverDetails, nil
		}
		commonCliUtils.FixWinPathsForFileSystemSourcedCmds(specFiles, true, false)
		return specFiles, nil, nil
	}

	specFiles := jfrogSpec.NewBuilder().
		Pattern(ctx.args.DependencyPattern).
		Recursive(parseBoolOrDefault(true, ctx.args.Recursive)).
		Regexp(parseBoolOrDefault(false, ctx.args.Regexp)).
		Exclusions(splitList(ctx.args.Exclusions)).
		BuildSpec()

	if parseBoolOrDefault(false, ctx.args.FromRt) {
		serverDetails, err := ctx.createServerDetails("")
		if err != nil {
			return nil, nil, err
		}
		return specFiles, serverDetails, nil
	}
	commonCliUtils.FixWinPathsForFileSystemSourcedCmds(specFiles, false, ctx.args.Exclusions != "")
	return specFiles, nil, nil
}

func (ctx *runtimeContext) resolveSpecPath(specPath, specValue string) (string, error) {
	if specPath != "" {
		return specPath, nil
	}
	trimmed := strings.TrimSpace(specValue)
	if trimmed == "" {
		return "", fmt.Errorf("spec file path cannot be empty")
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		if ctx.tempSpecPath != "" {
			return ctx.tempSpecPath, nil
		}
		tempPath := filepath.Join(ctx.tempDir, "inline-spec.json")
		if err := os.WriteFile(tempPath, []byte(specValue), 0o600); err != nil {
			return "", err
		}
		ctx.tempSpecPath = tempPath
		return tempPath, nil
	}
	return specValue, nil
}

func (ctx *runtimeContext) seedRuntimeCerts() error {
	sourceDir := defaultJfrogCertsDir()
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	targetDir := filepath.Join(ctx.homeDir, "security", "certs")
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		src := filepath.Join(sourceDir, entry.Name())
		dst := filepath.Join(targetDir, entry.Name())
		content, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, content, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (ctx *runtimeContext) installPemCertificates() error {
	if ctx.args.PEMFileContents == "" || parseBoolOrDefault(false, ctx.args.Insecure) {
		return nil
	}

	legacyPath := ctx.args.PEMFilePath
	if legacyPath == "" {
		legacyPath = filepath.Join(defaultJfrogCertsDir(), "cert.pem")
	}
	if err := ensurePemFile(legacyPath, ctx.args.PEMFileContents); err != nil {
		return err
	}

	runtimePath := filepath.Join(ctx.homeDir, "security", "certs", filepath.Base(legacyPath))
	return ensurePemFile(runtimePath, ctx.args.PEMFileContents)
}

func ensurePemFile(path, contents string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("error creating pem folder: %s", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		return fmt.Errorf("error writing pem file: %s", err)
	}
	return nil
}

func defaultJfrogCertsDir() string {
	if runtime.GOOS == "windows" {
		return "C:/users/ContainerAdministrator/.jfrog/security/certs"
	}
	return "/root/.jfrog/security/certs"
}

func threadsOrDefault() int {
	return 3
}

func (ctx *runtimeContext) threadsOrDefault() int {
	if ctx.args.Threads > 0 {
		return ctx.args.Threads
	}
	return threadsOrDefault()
}

func shellSplit(input string) ([]string, error) {
	if strings.TrimSpace(input) == "" {
		return []string{}, nil
	}
	parts, err := shlex.Split(input)
	if err == nil {
		return parts, nil
	}
	return strings.Fields(input), nil
}

func splitList(raw string) []string {
	if raw == "" {
		return nil
	}
	separator := ","
	if strings.Contains(raw, ";") {
		separator = ";"
	}
	items := strings.Split(raw, separator)
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			result = append(result, item)
		}
	}
	return result
}

func buildReference(buildName, buildNumber string) string {
	if buildName == "" || buildNumber == "" {
		return ""
	}
	return buildName + "/" + buildNumber
}

func setEnvWithRestore(key, value string) func() error {
	previousValue, existed := os.LookupEnv(key)
	_ = os.Setenv(key, value)
	return func() error {
		if !existed {
			return os.Unsetenv(key)
		}
		return os.Setenv(key, previousValue)
	}
}

func traceCommand(command string) {
	fmt.Fprintf(os.Stdout, "+ %s\n", command)
	logrus.Debugf("executing %s", command)
}
