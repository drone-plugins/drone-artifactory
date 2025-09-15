package plugin

import (
    "errors"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "runtime"
    "strings"

    "github.com/sirupsen/logrus"
)

var GradleConfigJsonTagToExeFlagMapStringItemList = []JsonTagToExeFlagMapStringItem{
	{"--deploy-ivy-desc=", "PLUGIN_DEPLOY_IVY_DESC", false, false},
	{"--deploy-maven-desc=", "PLUGIN_DEPLOY_MAVEN_DESC", false, false},
	{"--global=", "PLUGIN_GLOBAL", false, false},
	{"--ivy-artifacts-pattern=", "PLUGIN_IVY_ARTIFACTS_PATTERN", false, false},
	{"--ivy-desc-pattern=", "PLUGIN_IVY_DESC_PATTERN", false, false},
	{"--repo-deploy=", "PLUGIN_REPO_DEPLOY", false, false},
	{"--repo-resolve=", "PLUGIN_REPO_RESOLVE", false, false},
	{"--server-id-deploy=", "PLUGIN_SERVER_ID_DEPLOY", false, false},
	{"--server-id-resolve=", "PLUGIN_SERVER_ID_RESOLVE", false, false},
	{"--use-wrapper=", "PLUGIN_USE_WRAPPER", false, false},
	{"--uses-plugin=", "PLUGIN_USES_PLUGIN", false, false},
}

var GradleRunJsonTagToExeFlagMapStringItemList = []JsonTagToExeFlagMapStringItem{
	{"--build-name=", "PLUGIN_BUILD_NAME", false, false},
	{"--build-number=", "PLUGIN_BUILD_NUMBER", false, false},
	{"--detailed-summary=", "PLUGIN_DETAILED_SUMMARY", false, false},
	{"--format=", "PLUGIN_FORMAT", false, false},
	{"--project=", "PLUGIN_PROJECT", false, false},
	{"--scan=", "PLUGIN_SCAN", false, false},
	{"--threads=", "PLUGIN_THREADS", false, false},
}

func GetGradleCommandArgs(args Args) ([][]string, error) {

	var cmdList [][]string

	jfrogConfigAddConfigCommandArgs, err := GetConfigAddConfigCommandArgs(args.ResolverId,
		args.Username, args.Password, args.URL, args.AccessToken, args.APIKey)
	if err != nil {
		return cmdList, err
	}

	gradleConfigCommandArgs := []string{GradleConfig}
	// Add necessary parameters for Windows to prevent all interactive prompts
	if runtime.GOOS == "windows" {
		// These parameters prevent all interactive prompts
		gradleConfigCommandArgs = append(gradleConfigCommandArgs, "--global=true")
		// Add server ID for deployment/resolution
		if args.ResolverId != "" {
			gradleConfigCommandArgs = append(gradleConfigCommandArgs, "--server-id-resolve="+args.ResolverId)
			gradleConfigCommandArgs = append(gradleConfigCommandArgs, "--server-id-deploy="+args.ResolverId)
		}
		// Add repos to prevent prompts
		if args.RepoResolve == "" {
			gradleConfigCommandArgs = append(gradleConfigCommandArgs, "--repo-resolve=libs-release")
		}
		if args.RepoDeploy == "" {
			gradleConfigCommandArgs = append(gradleConfigCommandArgs, "--repo-deploy=libs-release-local")
		}
		// Use maven-style plugin to enable dependency resolution
		gradleConfigCommandArgs = append(gradleConfigCommandArgs, "--uses-plugin=true")
	}

	err = PopulateArgs(&gradleConfigCommandArgs, &args, GradleConfigJsonTagToExeFlagMapStringItemList)
	if err != nil {
		return cmdList, err
	}

	gradleTaskCommandArgs := []string{GradleCmd, args.GradleTasks}
	err = PopulateArgs(&gradleTaskCommandArgs, &args, GradleRunJsonTagToExeFlagMapStringItemList)
	if err != nil {
		return cmdList, err
	}

	if len(args.BuildFile) > 0 {
		gradleTaskCommandArgs = append(gradleTaskCommandArgs, "-b "+args.BuildFile)
	}

	cmdList = append(cmdList, jfrogConfigAddConfigCommandArgs)
	cmdList = append(cmdList, gradleConfigCommandArgs)
	cmdList = append(cmdList, gradleTaskCommandArgs)

	return cmdList, nil
}

var GradleConfigCmdJsonTagToExeFlagMapStringItemList = []JsonTagToExeFlagMapStringItem{
	{"--exclude-patterns=", "PLUGIN_EXCLUDE_PATTERNS", false, false},
	{"--global=", "PLUGIN_GLOBAL", false, false},
	{"--include-patterns=", "PLUGIN_INCLUDE_PATTERNS", false, false},
	{"--repo-deploy-releases=", "PLUGIN_DEPLOY_RELEASE_REPO", false, false},
	{"--repo-deploy-snapshots=", "PLUGIN_DEPLOY_SNAPSHOT_REPO", false, false},
	{"--repo-resolve-releases=", "PLUGIN_RESOLVE_RELEASE_REPO", false, false},
	{"--repo-resolve-snapshots=", "PLUGIN_RESOLVE_SNAPSHOT_REPO", false, false},
	{"--repo-deploy=", "PLUGIN_REPO_DEPLOY", false, false},
	{"--repo-resolve=", "PLUGIN_REPO_RESOLVE", false, false},
	{"--server-id-deploy=", "PLUGIN_SERVER_ID_DEPLOY", false, false},
	{"--server-id-resolve=", "PLUGIN_RESOLVER_ID", false, false},
	{"--use-wrapper=", "PLUGIN_USE_WRAPPER", false, false},
}

func GetGradlePublishCommand(args Args) ([][]string, error) {

	var cmdList [][]string
	var jfrogConfigAddConfigCommandArgs []string

	tmpServerId := args.DeployerId
	jfrogConfigAddConfigCommandArgs, err := GetConfigAddConfigCommandArgs(tmpServerId,
		args.Username, args.Password, args.URL, args.AccessToken, args.APIKey)
	if err != nil {
		logrus.Println("GetConfigAddConfigCommandArgs error: ", err)
		return cmdList, err
	}

	gradleConfigCommandArgs := []string{GradleConfig}
	err = PopulateArgs(&gradleConfigCommandArgs, &args, GradleConfigCmdJsonTagToExeFlagMapStringItemList)
	if err != nil {
		logrus.Println("PopulateArgs error: ", err)
		return cmdList, err
	}
	gradleConfigCommandArgs = append(gradleConfigCommandArgs, "--server-id-deploy="+tmpServerId)
	gradleConfigCommandArgs = append(gradleConfigCommandArgs, "--server-id-resolve="+tmpServerId)

	rtPublishCommandArgs := []string{"gradle", Publish}
	switch {
	case args.Username != "":
		rtPublishCommandArgs = append(rtPublishCommandArgs, "-Pusername="+args.Username)
		rtPublishCommandArgs = append(rtPublishCommandArgs, "-Ppassword="+args.Password)
    case args.AccessToken != "":
        errMsg := "AccessToken is not supported for Gradle" +
            " try username: <username> , password: <access_token> instead"
        logrus.Println(errMsg)
        return cmdList, errors.New(errMsg)
	}
	rtPublishCommandArgs = append(rtPublishCommandArgs, "--build-name="+args.BuildName)
	rtPublishCommandArgs = append(rtPublishCommandArgs, "--build-number="+args.BuildNumber)

	rtPublishBuildInfoCommandArgs := []string{"rt", BuildPublish, args.BuildName, args.BuildNumber,
		"--server-id=" + tmpServerId}
	err = PopulateArgs(&rtPublishBuildInfoCommandArgs, &args, RtBuildInfoPublishCmdJsonTagToExeFlagMap)
	if err != nil {
		logrus.Println("PopulateArgs error: ", err)
		return cmdList, err
	}

	cmdList = append(cmdList, jfrogConfigAddConfigCommandArgs)
	cmdList = append(cmdList, gradleConfigCommandArgs)
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

// --- Gradle SDK-backed flows ---

func runGradleBuildSDK(args Args) error {
    initPath, cleanup, err := generateGradleInit(args)
    if err != nil {
        return err
    }
    defer cleanup()

    tasks := strings.TrimSpace(args.GradleTasks)
    if tasks == "" {
        tasks = "clean build"
    }
    cmdArgs := []string{"-I", initPath}
    if args.BuildFile != "" {
        cmdArgs = append(cmdArgs, "-b", args.BuildFile)
    }
    cmdArgs = append(cmdArgs, strings.Split(tasks, " ")...)
    cmd := exec.Command("gradle", cmdArgs...)
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    logrus.Printf("+ gradle %s", strings.Join(cmdArgs, " "))
    return cmd.Run()
}

func runGradlePublishSDK(args Args) error {
    // Generate init.gradle that injects publishing repo credentials if 'publishing' exists
    initPath, cleanup, err := generateGradleInit(args)
    if err != nil {
        return err
    }
    defer cleanup()

    // Default to 'publish' task if not provided
    tasks := strings.TrimSpace(args.GradleTasks)
    if tasks == "" {
        tasks = "publish"
    }
    cmdArgs := []string{"-I", initPath}
    if args.BuildFile != "" {
        cmdArgs = append(cmdArgs, "-b", args.BuildFile)
    }
    cmdArgs = append(cmdArgs, strings.Split(tasks, " ")...)
    cmd := exec.Command("gradle", cmdArgs...)
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    logrus.Printf("+ gradle %s", strings.Join(cmdArgs, " "))
    return cmd.Run()
}

// generateGradleInit creates an init.gradle that sets resolve and (if exists) publishing repo with credentials.
func generateGradleInit(args Args) (string, func(), error) {
    sanitized, err := sanitizeURL(args.URL)
    if err != nil {
        return "", func() {}, err
    }
    if !strings.HasSuffix(sanitized, "/") {
        sanitized += "/"
    }
    resolveRepo := strings.TrimLeft(args.RepoResolve, "/")
    deployRepo := strings.TrimLeft(args.RepoDeploy, "/")

    var username, password string
    if args.AccessToken != "" && args.Username != "" {
        username = args.Username
        password = args.AccessToken
    } else {
        username = args.Username
        password = args.Password
    }

    content := fmt.Sprintf(`
allprojects {
  repositories {
    maven { url '%s%s' %s }
  }
}

gradle.projectsEvaluated {
  allprojects { project ->
    if (project.hasProperty('publishing')) {
      project.publishing {
        repositories {
          maven {
            url '%s%s'
            credentials {
              username = '%s'
              password = '%s'
            }
          }
        }
      }
    }
  }
}
`, sanitized, resolveRepo, gradleCredentialsBlock(username, password), sanitized, deployRepo, escapeForGroovy(username), escapeForGroovy(password))

    tmpDir, err := os.MkdirTemp("", "gradle-init-")
    if err != nil {
        return "", func() {}, err
    }
    path := filepath.Join(tmpDir, "init.gradle")
    if err := os.WriteFile(path, []byte(content), 0600); err != nil {
        return "", func() {}, err
    }
    cleanup := func() { _ = os.RemoveAll(tmpDir) }
    return path, cleanup, nil
}

func gradleCredentialsBlock(user, pass string) string {
    if strings.TrimSpace(user) == "" {
        return ""
    }
    return fmt.Sprintf("credentials { username '%s'; password '%s' }", escapeForGroovy(user), escapeForGroovy(pass))
}

func escapeForGroovy(s string) string {
    s = strings.ReplaceAll(s, "\\", "\\\\")
    s = strings.ReplaceAll(s, "'", "\\'")
    return s
}
