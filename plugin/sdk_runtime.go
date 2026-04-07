package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/google/shlex"
	"github.com/jfrog/build-info-go/entities"
	ioutils "github.com/jfrog/gofrog/io"
	buildinfoCmd "github.com/jfrog/jfrog-cli-artifactory/artifactory/commands/buildinfo"
	gradleCmd "github.com/jfrog/jfrog-cli-artifactory/artifactory/commands/gradle"
	mvnCmd "github.com/jfrog/jfrog-cli-artifactory/artifactory/commands/mvn"
	"github.com/jfrog/jfrog-cli-artifactory/artifactory/utils/civcs"
	rtUtils "github.com/jfrog/jfrog-cli-core/v2/artifactory/utils"
	jfrogBuild "github.com/jfrog/jfrog-cli-core/v2/common/build"
	commonCliUtils "github.com/jfrog/jfrog-cli-core/v2/common/cliutils"
	jfrogProject "github.com/jfrog/jfrog-cli-core/v2/common/project"
	jfrogSpec "github.com/jfrog/jfrog-cli-core/v2/common/spec"
	jfrogConfig "github.com/jfrog/jfrog-cli-core/v2/utils/config"
	"github.com/jfrog/jfrog-cli-core/v2/utils/coreutils"
	artifactory "github.com/jfrog/jfrog-client-go/artifactory"
	artifactoryAuth "github.com/jfrog/jfrog-client-go/artifactory/auth"
	"github.com/jfrog/jfrog-client-go/artifactory/services"
	rtServicesUtils "github.com/jfrog/jfrog-client-go/artifactory/services/utils"
	clientAuth "github.com/jfrog/jfrog-client-go/auth"
	clientConfig "github.com/jfrog/jfrog-client-go/config"
	clientutils "github.com/jfrog/jfrog-client-go/utils"
	"github.com/jfrog/jfrog-client-go/utils/io/content"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const (
	jfrogHomeDirEnv = "JFROG_CLI_HOME_DIR"
	defaultThreads  = 3
)

var currentGOOS = runtime.GOOS

type runtimeContext struct {
	ctx          context.Context
	args         Args
	tempDir      string
	homeDir      string
	projectDir   string
	restoreHome  func() error
	removeTemp   func(string) error
	tempSpecPath string
	newManager   func(dryRun bool, threads int) (artifactory.ArtifactoryServicesManager, error)
}

type projectConfigFile struct {
	Version    int                     `yaml:"version,omitempty"`
	ConfigType string                  `yaml:"type,omitempty"`
	Resolver   jfrogProject.Repository `yaml:"resolver,omitempty"`
	Deployer   jfrogProject.Repository `yaml:"deployer,omitempty"`
	UsePlugin  bool                    `yaml:"usePlugin,omitempty"`
	UseWrapper bool                    `yaml:"useWrapper,omitempty"`
}

func newRuntimeContext(parent context.Context, args Args) (*runtimeContext, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx := &runtimeContext{ctx: parent, args: args}

	tempDir, err := os.MkdirTemp("", "drone-artifactory-*")
	if err != nil {
		return nil, err
	}
	ctx.tempDir = tempDir
	ctx.homeDir = filepath.Join(tempDir, ".jfrog")
	ctx.projectDir = filepath.Join(tempDir, "projects")
	ctx.removeTemp = os.RemoveAll

	if err := os.MkdirAll(filepath.Join(ctx.homeDir, "security", "certs"), 0o700); err != nil {
		_ = ctx.removeTemp(tempDir)
		return nil, err
	}
	if err := os.MkdirAll(ctx.projectDir, 0o700); err != nil {
		_ = ctx.removeTemp(tempDir)
		return nil, err
	}

	if err := ctx.seedRuntimeCerts(); err != nil {
		_ = ctx.removeTemp(tempDir)
		return nil, err
	}

	if err := ctx.installPemCertificates(); err != nil {
		_ = ctx.removeTemp(tempDir)
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
		removeTemp := ctx.removeTemp
		if removeTemp == nil {
			removeTemp = os.RemoveAll
		}
		if err := removeTemp(ctx.tempDir); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

func (ctx *runtimeContext) runDefaultUpload() error {
	if err := ctx.checkContext(); err != nil {
		return err
	}
	if ctx.args.URL == "" {
		return fmt.Errorf("JFrog Artifactory URL must be set, or anonymous access is not permitted")
	}

	specFiles, err := ctx.uploadSpec()
	if err != nil {
		return err
	}

	traceCommand("jf-sdk rt upload")
	if err := ctx.uploadWithContext(specFiles); err != nil {
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
	if err := ctx.checkContext(); err != nil {
		return err
	}
	specFiles, err := ctx.downloadSpec()
	if err != nil {
		return err
	}

	traceCommand("jf-sdk rt download")
	return ctx.downloadWithContext(specFiles)
}

func (ctx *runtimeContext) publishBuildInfo() error {
	if err := ctx.checkContext(); err != nil {
		return err
	}
	buildConfig := ctx.buildConfiguration()
	if err := buildConfig.ValidateBuildParams(); err != nil {
		return fmt.Errorf("both build name and build number need to be set when publishing build info")
	}

	traceCommand("jf-sdk rt build-publish")
	return ctx.publishBuildInfoWithContext(buildConfig)
}

func (ctx *runtimeContext) runBuildClean() error {
	if err := ctx.checkContext(); err != nil {
		return err
	}
	traceCommand("jf-sdk rt build-clean")
	buildConfig := ctx.buildConfiguration()
	buildName, err := buildConfig.GetBuildName()
	if err != nil {
		return err
	}
	buildNumber, err := buildConfig.GetBuildNumber()
	if err != nil {
		return err
	}
	logrus.Info("Cleaning build info...")
	if err := jfrogBuild.RemoveBuildDir(buildName, buildNumber, buildConfig.GetProject()); err != nil {
		return err
	}
	logrus.Infof("Cleaned build info %s/%s.", buildName, buildNumber)
	return nil
}

func (ctx *runtimeContext) runBuildScan() error {
	if err := ctx.checkContext(); err != nil {
		return err
	}
	traceCommand("jf-sdk rt build-scan")
	return ctx.scanBuildWithContext()
}

func (ctx *runtimeContext) runPromote() error {
	if err := ctx.checkContext(); err != nil {
		return err
	}

	params := services.NewPromotionParams()
	params.TargetRepo = ctx.args.Target
	params.Copy = parseBoolOrDefault(false, ctx.args.Copy)

	traceCommand("jf-sdk rt build-promote")
	return ctx.promoteBuildWithContext(params)
}

func (ctx *runtimeContext) runAddDependencies() error {
	if err := ctx.checkContext(); err != nil {
		return err
	}
	traceCommand("jf-sdk rt build-add-dependencies")
	return ctx.addDependenciesWithContext()
}

func (ctx *runtimeContext) runBuildDiscard() error {
	if err := ctx.checkContext(); err != nil {
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
	manager, err := ctx.createServiceManager(false, defaultThreads)
	if err != nil {
		return err
	}
	return manager.DiscardBuilds(params)
}

func (ctx *runtimeContext) runMaven(publish bool) error {
	if err := ctx.checkContext(); err != nil {
		return err
	}
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
	goals := shellSplitBestEffort(ctx.args.MvnGoals)
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

	// The current JFrog Maven wrapper does not expose a context hook for mid-flight cancellation.
	if err := cmd.Run(); err != nil {
		return normalizeHostToolError("maven", err)
	}
	if err := ctx.checkContext(); err != nil {
		return err
	}
	if publish {
		return ctx.publishBuildInfo()
	}
	return nil
}

func (ctx *runtimeContext) runGradle(publish bool) error {
	if err := ctx.checkContext(); err != nil {
		return err
	}
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
	tasks := shellSplitBestEffort(ctx.args.GradleTasks)
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

	// The current JFrog Gradle wrapper does not expose a context hook for mid-flight cancellation.
	if err := cmd.Run(); err != nil {
		return normalizeHostToolError("gradle", err)
	}
	if err := ctx.checkContext(); err != nil {
		return err
	}
	if publish {
		return ctx.publishBuildInfo()
	}
	return nil
}

func (ctx *runtimeContext) uploadWithContext(specFiles *jfrogSpec.SpecFiles) (err error) {
	manager, err := ctx.createServiceManager(false, ctx.threadsOrDefault())
	if err != nil {
		return err
	}

	uploadCfg := &rtUtils.UploadConfiguration{Threads: ctx.threadsOrDefault()}
	uploadCfg.MinChecksumDeploySize, err = rtUtils.GetMinChecksumDeploySize()
	if err != nil {
		return err
	}

	buildCfg := ctx.buildConfiguration()
	toCollect, err := buildCfg.IsCollectBuildInfo()
	if err != nil {
		return err
	}
	buildProps := ""
	if toCollect {
		buildProps, err = jfrogBuild.CreateBuildPropsFromConfiguration(buildCfg)
		if err != nil {
			return err
		}
	}

	uploadParams := make([]services.UploadParams, 0, len(specFiles.Files))
	for i := range specFiles.Files {
		if err := ctx.checkContext(); err != nil {
			return err
		}
		file := specFiles.Get(i)
		file.TargetProps = clientutils.AddProps(file.TargetProps, file.Props)
		// TODO: Move the JFrog wrapper modules back to stable tags once a tagged
		// jfrog-cli-artifactory release includes civcs.MergeWithUserProps and
		// stops pinning pseudo-versioned jfrog-cli-core/client-go/build-info-go.
		file.TargetProps = civcs.MergeWithUserProps(file.TargetProps)
		params, err := createUploadParams(file, uploadCfg, buildProps, toCollect, false)
		if err != nil {
			return err
		}
		uploadParams = append(uploadParams, params)
	}

	summary, err := manager.UploadFilesWithSummary(artifactory.UploadServiceOptions{}, uploadParams...)
	if err != nil {
		return err
	}
	if summary == nil {
		return nil
	}
	if summary.TransferDetailsReader != nil {
		defer ioutils.Close(summary.TransferDetailsReader, &err)
	}
	if summary.ArtifactsDetailsReader != nil {
		defer ioutils.Close(summary.ArtifactsDetailsReader, &err)
	}
	if summary.TotalFailed > 0 {
		return errors.New("upload finished with errors. Review the logs for more information")
	}
	if toCollect && summary.ArtifactsDetailsReader != nil {
		artifacts, convErr := rtServicesUtils.ConvertArtifactsDetailsToBuildInfoArtifacts(summary.ArtifactsDetailsReader)
		if convErr != nil {
			return convErr
		}
		return jfrogBuild.PopulateBuildArtifactsAsPartials(artifacts, buildCfg, entities.Generic)
	}
	return nil
}

func (ctx *runtimeContext) downloadWithContext(specFiles *jfrogSpec.SpecFiles) (err error) {
	manager, err := ctx.createServiceManager(false, ctx.threadsOrDefault())
	if err != nil {
		return err
	}

	buildCfg := ctx.buildConfiguration()
	toCollect, err := buildCfg.IsCollectBuildInfo()
	if err != nil {
		return err
	}
	if toCollect {
		buildName, err := buildCfg.GetBuildName()
		if err != nil {
			return err
		}
		buildNumber, err := buildCfg.GetBuildNumber()
		if err != nil {
			return err
		}
		if err := jfrogBuild.SaveBuildGeneralDetails(buildName, buildNumber, buildCfg.GetProject()); err != nil {
			return err
		}
	}

	downloadCfg := &rtUtils.DownloadConfiguration{Threads: ctx.threadsOrDefault()}
	downloadParams := make([]services.DownloadParams, 0, len(specFiles.Files))
	for i := range specFiles.Files {
		if err := ctx.checkContext(); err != nil {
			return err
		}
		params, err := createDownloadParams(specFiles.Get(i), downloadCfg)
		if err != nil {
			return err
		}
		downloadParams = append(downloadParams, params)
	}

	summary, err := manager.DownloadFilesWithSummary(downloadParams...)
	if err != nil {
		return err
	}
	if summary == nil {
		return nil
	}
	if summary.TransferDetailsReader != nil {
		defer ioutils.Close(summary.TransferDetailsReader, &err)
	}
	if summary.ArtifactsDetailsReader != nil {
		defer ioutils.Close(summary.ArtifactsDetailsReader, &err)
	}
	if summary.TotalFailed > 0 {
		return errors.New("download finished with errors, please review the logs")
	}
	if toCollect && summary.ArtifactsDetailsReader != nil {
		buildName, err := buildCfg.GetBuildName()
		if err != nil {
			return err
		}
		buildNumber, err := buildCfg.GetBuildNumber()
		if err != nil {
			return err
		}
		buildDependencies, convErr := rtServicesUtils.ConvertArtifactsDetailsToBuildInfoDependencies(summary.ArtifactsDetailsReader)
		if convErr != nil {
			return convErr
		}
		return jfrogBuild.SavePartialBuildInfo(buildName, buildNumber, buildCfg.GetProject(), func(partial *entities.Partial) {
			partial.Dependencies = buildDependencies
			partial.ModuleId = buildCfg.GetModule()
			partial.ModuleType = entities.Generic
		})
	}
	return nil
}

func (ctx *runtimeContext) publishBuildInfoWithContext(buildConfig *jfrogBuild.BuildConfiguration) error {
	manager, err := ctx.createServiceManager(false, defaultThreads)
	if err != nil {
		return err
	}
	buildInfoService := jfrogBuild.CreateBuildInfoService()
	buildName, err := buildConfig.GetBuildName()
	if err != nil {
		return err
	}
	buildNumber, err := buildConfig.GetBuildNumber()
	if err != nil {
		return err
	}
	buildInstance, err := buildInfoService.GetOrCreateBuildWithProject(buildName, buildNumber, buildConfig.GetProject())
	if err != nil {
		return err
	}
	buildInstance.SetAgentName(coreutils.GetCliUserAgentName())
	buildInstance.SetAgentVersion(coreutils.GetCliUserAgentVersion())
	buildInstance.SetBuildAgentVersion(coreutils.GetClientAgentVersion())
	buildInstance.SetPrincipal(ctx.args.Username)
	buildInfo, err := buildInstance.ToBuildInfo()
	if err != nil {
		return err
	}
	_, err = manager.PublishBuildInfo(buildInfo, buildConfig.GetProject())
	return err
}

func (ctx *runtimeContext) promoteBuildWithContext(params services.PromotionParams) error {
	buildConfig := ctx.buildConfiguration()
	if err := buildConfig.ValidateBuildParams(); err != nil {
		return err
	}
	buildName, err := buildConfig.GetBuildName()
	if err != nil {
		return err
	}
	buildNumber, err := buildConfig.GetBuildNumber()
	if err != nil {
		return err
	}
	params.BuildName = buildName
	params.BuildNumber = buildNumber
	params.ProjectKey = buildConfig.GetProject()

	manager, err := ctx.createServiceManager(false, defaultThreads)
	if err != nil {
		return err
	}
	return manager.PromoteBuild(params)
}

func (ctx *runtimeContext) scanBuildWithContext() error {
	buildConfig := ctx.buildConfiguration()
	params := services.NewXrayScanParams()
	buildName, err := buildConfig.GetBuildName()
	if err != nil {
		return err
	}
	buildNumber, err := buildConfig.GetBuildNumber()
	if err != nil {
		return err
	}
	params.BuildName = buildName
	params.BuildNumber = buildNumber
	params.ProjectKey = buildConfig.GetProject()

	manager, err := ctx.createServiceManager(false, defaultThreads)
	if err != nil {
		return err
	}
	logrus.Info("Triggered Xray build scan... The scan may take a few minutes.")
	result, err := manager.XrayScanBuild(params)
	if err != nil {
		return err
	}
	logrus.Info("Xray scan completed.")
	logrus.Println(clientutils.IndentJson(result))
	return nil
}

func (ctx *runtimeContext) addDependenciesWithContext() error {
	specFiles, fromRT, err := ctx.buildDependenciesSpec()
	if err != nil {
		return err
	}
	if !fromRT {
		return buildinfoCmd.NewBuildAddDependenciesCommand().
			SetBuildConfiguration(ctx.buildConfiguration()).
			SetDependenciesSpec(specFiles).
			Run()
	}

	buildConfig := ctx.buildConfiguration()
	buildName, err := buildConfig.GetBuildName()
	if err != nil {
		return err
	}
	buildNumber, err := buildConfig.GetBuildNumber()
	if err != nil {
		return err
	}
	if err := jfrogBuild.SaveBuildGeneralDetails(buildName, buildNumber, buildConfig.GetProject()); err != nil {
		return err
	}

	manager, err := ctx.createServiceManager(false, defaultThreads)
	if err != nil {
		return err
	}
	for i := range specFiles.Files {
		if err := ctx.checkContext(); err != nil {
			return err
		}
		searchParams, err := rtUtils.GetSearchParams(specFiles.Get(i))
		if err != nil {
			return err
		}
		reader, err := manager.SearchFiles(searchParams)
		if err != nil {
			return err
		}
		if err := ctx.saveRemoteDependencies(reader); err != nil {
			_ = reader.Close()
			return err
		}
		if err := reader.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (ctx *runtimeContext) saveRemoteDependencies(reader *content.ContentReader) error {
	buildCfg := ctx.buildConfiguration()
	buffered := 0
	var dependencies []entities.Dependency
	for resultItem := new(rtServicesUtils.ResultItem); reader.NextRecord(resultItem) == nil; resultItem = new(rtServicesUtils.ResultItem) {
		dependencies = append(dependencies, resultItem.ToDependency())
		buffered++
		// Keep flush granularity aligned with the upstream reader buffering limit so
		// remote dependency collection does not accumulate more in memory than the
		// client-go content pipeline is already tuned to process at once.
		if buffered > clientutils.MaxBufferSize {
			if err := saveBuildDependencies(buildCfg, dependencies); err != nil {
				return err
			}
			buffered = 0
			dependencies = nil
		}
	}
	if err := reader.GetError(); err != nil {
		return err
	}
	if len(dependencies) > 0 {
		return saveBuildDependencies(buildCfg, dependencies)
	}
	return nil
}

func createUploadParams(f *jfrogSpec.File, configuration *rtUtils.UploadConfiguration, buildProps string, addVcsProps bool, dryRun bool) (uploadParams services.UploadParams, err error) {
	uploadParams = services.NewUploadParams()
	uploadParams.CommonParams, err = f.ToCommonParams()
	if err != nil {
		return
	}
	uploadParams.Deb = configuration.Deb
	uploadParams.MinChecksumDeploy = configuration.MinChecksumDeploySize
	uploadParams.MinSplitSize = configuration.MinSplitSizeMB * rtServicesUtils.SizeMiB
	uploadParams.SplitCount = configuration.SplitCount
	uploadParams.ChunkSize = configuration.ChunkSizeMB * rtServicesUtils.SizeMiB
	uploadParams.AddVcsProps = addVcsProps
	uploadParams.BuildProps = buildProps
	uploadParams.Archive = f.Archive
	uploadParams.TargetPathInArchive = f.TargetPathInArchive

	uploadParams.Recursive, err = f.IsRecursive(true)
	if err != nil {
		return
	}
	uploadParams.Regexp, err = f.IsRegexp(false)
	if err != nil {
		return
	}
	uploadParams.Ant, err = f.IsAnt(false)
	if err != nil {
		return
	}
	includeDirs, err := f.IsIncludeDirs(false)
	if err != nil {
		return
	}
	uploadParams.IncludeDirs = includeDirs && !dryRun
	uploadParams.Flat, err = f.IsFlat(true)
	if err != nil {
		return
	}
	uploadParams.ExplodeArchive, err = f.IsExplode(false)
	if err != nil {
		return
	}
	uploadParams.Symlink, err = f.IsSymlinks(false)
	if err != nil {
		return
	}
	return
}

func createDownloadParams(f *jfrogSpec.File, configuration *rtUtils.DownloadConfiguration) (downloadParams services.DownloadParams, err error) {
	downloadParams = services.NewDownloadParams()
	downloadParams.CommonParams, err = f.ToCommonParams()
	if err != nil {
		return
	}
	downloadParams.Symlink = configuration.Symlink
	downloadParams.MinSplitSize = configuration.MinSplitSize
	downloadParams.SplitCount = configuration.SplitCount
	downloadParams.SkipChecksum = configuration.SkipChecksum

	downloadParams.Recursive, err = f.IsRecursive(true)
	if err != nil {
		return
	}
	downloadParams.IncludeDirs, err = f.IsIncludeDirs(false)
	if err != nil {
		return
	}
	downloadParams.Flat, err = f.IsFlat(false)
	if err != nil {
		return
	}
	downloadParams.Explode, err = f.IsExplode(false)
	if err != nil {
		return
	}
	downloadParams.BypassArchiveInspection, err = f.IsBypassArchiveInspection(false)
	if err != nil {
		return
	}
	downloadParams.ValidateSymlink, err = f.IsValidateSymlinks(false)
	if err != nil {
		return
	}
	downloadParams.ExcludeArtifacts, err = f.IsExcludeArtifacts(false)
	if err != nil {
		return
	}
	downloadParams.IncludeDeps, err = f.IsIncludeDeps(false)
	if err != nil {
		return
	}
	downloadParams.Transitive, err = f.IsTransitive(false)
	if err != nil {
		return
	}
	downloadParams.PublicGpgKey = f.GetPublicGpgKey()
	return
}

func saveBuildDependencies(buildCfg *jfrogBuild.BuildConfiguration, dependencies []entities.Dependency) error {
	logrus.Debug("Saving ", strconv.Itoa(len(dependencies)), " dependencies.")
	buildName, err := buildCfg.GetBuildName()
	if err != nil {
		return err
	}
	buildNumber, err := buildCfg.GetBuildNumber()
	if err != nil {
		return err
	}
	return jfrogBuild.SavePartialBuildInfo(buildName, buildNumber, buildCfg.GetProject(), func(partial *entities.Partial) {
		partial.ModuleType = entities.Generic
		partial.Dependencies = dependencies
		partial.ModuleId = buildCfg.GetModule()
	})
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

func (ctx *runtimeContext) checkContext() error {
	if ctx.ctx == nil {
		return nil
	}
	return ctx.ctx.Err()
}

func (ctx *runtimeContext) buildArtAuthDetails() (clientAuth.ServiceDetails, error) {
	sanitizedURL, err := sanitizeURL(ctx.args.URL)
	if err != nil {
		return nil, err
	}
	details := artifactoryAuth.NewArtifactoryDetails()
	details.SetUrl(sanitizedURL)
	switch {
	case ctx.args.Username != "" && ctx.args.Password != "":
		details.SetUser(ctx.args.Username)
		details.SetPassword(ctx.args.Password)
	case ctx.args.APIKey != "":
		details.SetUser(ctx.args.Username)
		details.SetApiKey(ctx.args.APIKey)
	case ctx.args.AccessToken != "":
		details.SetUser(ctx.args.Username)
		details.SetAccessToken(ctx.args.AccessToken)
	default:
		return nil, fmt.Errorf("either username/password, api key or access token needs to be set")
	}
	return details, nil
}

func (ctx *runtimeContext) createServiceManager(dryRun bool, threads int) (artifactory.ArtifactoryServicesManager, error) {
	if ctx.newManager != nil {
		return ctx.newManager(dryRun, threads)
	}
	authDetails, err := ctx.buildArtAuthDetails()
	if err != nil {
		return nil, err
	}
	certsPath, err := coreutils.GetJfrogCertsDir()
	if err != nil {
		return nil, err
	}
	cfg, err := clientConfig.NewConfigBuilder().
		SetServiceDetails(authDetails).
		SetCertificatesPath(certsPath).
		SetInsecureTls(parseBoolOrDefault(false, ctx.args.Insecure)).
		SetDryRun(dryRun).
		SetThreads(threads).
		SetContext(ctx.ctx).
		SetHttpRetries(ctx.args.Retries).
		SetHttpRetryWaitMilliSecs(0).
		Build()
	if err != nil {
		return nil, err
	}
	return artifactory.New(cfg)
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
		// Temporary wrapper-path compatibility: Maven/Gradle server registration still
		// depends on jfrog-cli-core ServerDetails, which does not expose ApiKey.
		// Remove this workaround once those wrapper paths can consume client-go auth
		// details directly or jfrog-cli-core adds first-class API key support.
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

	if ctx.args.DeployRepo != "" {
		cfg.Deployer.ReleaseRepo = ctx.args.DeployRepo
		cfg.Deployer.SnapshotRepo = ctx.args.DeployRepo
	}

	hasExplicitResolver := ctx.args.ResolveReleaseRepo != "" || ctx.args.ResolveSnapshotRepo != ""
	hasExplicitDeployer := ctx.args.DeployReleaseRepo != "" || ctx.args.DeploySnapshotRepo != "" || ctx.args.DeployRepo != ""

	if currentGOOS == "windows" {
		if !hasExplicitResolver && cfg.Resolver.ReleaseRepo == "" {
			cfg.Resolver.ReleaseRepo = "libs-release"
		}
		if !hasExplicitResolver && cfg.Resolver.SnapshotRepo == "" {
			cfg.Resolver.SnapshotRepo = "libs-snapshot"
		}
		if !hasExplicitDeployer && cfg.Deployer.ReleaseRepo == "" {
			cfg.Deployer.ReleaseRepo = "libs-release-local"
		}
		if !hasExplicitDeployer && cfg.Deployer.SnapshotRepo == "" {
			cfg.Deployer.SnapshotRepo = "libs-snapshot-local"
		}
	}

	if !hasExplicitDeployer {
		cfg.Deployer = jfrogProject.Repository{}
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
	if currentGOOS == "windows" && !publish {
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
		if currentGOOS != "windows" || publish {
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
	return specFiles, nil
}

func (ctx *runtimeContext) buildDependenciesSpec() (*jfrogSpec.SpecFiles, bool, error) {
	specPath := ctx.args.SpecPath
	if specPath == "" && ctx.args.Spec != "" {
		resolvedPath, err := ctx.resolveSpecPath("", ctx.args.Spec)
		if err != nil {
			return nil, false, err
		}
		specPath = resolvedPath
	}
	if specPath != "" {
		specFiles, err := jfrogSpec.CreateSpecFromFile(specPath, coreutils.SpecVarsStringToMap(ctx.args.SpecVars))
		if err != nil {
			return nil, false, err
		}
		if parseBoolOrDefault(false, ctx.args.FromRt) {
			return specFiles, true, nil
		}
		commonCliUtils.FixWinPathsForFileSystemSourcedCmds(specFiles, true, false)
		return specFiles, false, nil
	}

	specFiles := jfrogSpec.NewBuilder().
		Pattern(ctx.args.DependencyPattern).
		Recursive(parseBoolOrDefault(true, ctx.args.Recursive)).
		Regexp(parseBoolOrDefault(false, ctx.args.Regexp)).
		Exclusions(splitList(ctx.args.Exclusions)).
		BuildSpec()

	if parseBoolOrDefault(false, ctx.args.FromRt) {
		return specFiles, true, nil
	}
	commonCliUtils.FixWinPathsForFileSystemSourcedCmds(specFiles, false, ctx.args.Exclusions != "")
	return specFiles, false, nil
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
	if currentGOOS == "windows" {
		return "C:/users/ContainerAdministrator/.jfrog/security/certs"
	}
	return "/root/.jfrog/security/certs"
}

func (ctx *runtimeContext) threadsOrDefault() int {
	if ctx.args.Threads > 0 {
		return ctx.args.Threads
	}
	return defaultThreads
}

func shellSplitBestEffort(input string) []string {
	if strings.TrimSpace(input) == "" {
		return []string{}
	}
	parts, err := shlex.Split(input)
	if err == nil {
		return parts
	}
	logrus.WithError(err).Warn("failed to parse quoted arguments with shlex; falling back to whitespace splitting")
	return strings.Fields(input)
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
