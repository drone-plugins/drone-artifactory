package plugin

import (
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
)

var PyPITwineUploadCmdJsonTagToExeFlagMapStringItemList = []JsonTagToExeFlagMapStringItem{
	{"--build-name=", "PLUGIN_BUILD_NAME", false, false},
	{"--build-number=", "PLUGIN_BUILD_NUMBER", false, false},
	{"--project=", "PLUGIN_PROJECT", false, false},
	{"--module=", "PLUGIN_MODULE", false, false},
}

func GetPyPIPublishCommandArgs(args Args) ([][]string, error) {
	var cmdList [][]string

	if args.Source == "" {
		return cmdList, fmt.Errorf("source file pattern is required for PyPI publish (e.g., dist/*.whl)")
	}
	if args.Target == "" {
		return cmdList, fmt.Errorf("target repository is required for PyPI publish")
	}
	if args.URL == "" {
		return cmdList, fmt.Errorf("JFrog Artifactory URL must be set")
	}

	// Step 1: jf config add
	serverId := args.DeployerId
	if serverId == "" {
		serverId = tmpServerId
	}
	jfrogConfigAddArgs, err := GetConfigAddConfigCommandArgs(serverId,
		args.Username, args.Password, args.URL, args.AccessToken, args.APIKey)
	if err != nil {
		logrus.Println("GetConfigAddConfigCommandArgs error: ", err)
		return cmdList, err
	}

	// Step 2: jf pip-config --server-id-deploy=<id> --repo-deploy=<repo>
	repoName := extractPyPIRepoName(args.Target)
	pipConfigArgs := []string{"pip-config",
		"--server-id-deploy=" + serverId,
		"--repo-deploy=" + repoName,
	}

	// Step 3: jf twine upload <source>
	twineUploadArgs := []string{"twine", "upload", args.Source}
	err = PopulateArgs(&twineUploadArgs, &args, PyPITwineUploadCmdJsonTagToExeFlagMapStringItemList)
	if err != nil {
		logrus.Println("twineUploadArgs PopulateArgs error: ", err)
		return cmdList, err
	}

	cmdList = append(cmdList, jfrogConfigAddArgs)
	cmdList = append(cmdList, pipConfigArgs)
	cmdList = append(cmdList, twineUploadArgs)

	// Step 4: jf rt build-publish (only if build info fields are present)
	if args.BuildName != "" && args.BuildNumber != "" {
		rtPublishBuildInfoArgs := []string{"rt", BuildPublish, args.BuildName, args.BuildNumber,
			"--server-id=" + serverId}
		err = PopulateArgs(&rtPublishBuildInfoArgs, &args, RtBuildInfoPublishCmdJsonTagToExeFlagMap)
		if err != nil {
			logrus.Println("PopulateArgs error: ", err)
			return cmdList, err
		}
		cmdList = append(cmdList, rtPublishBuildInfoArgs)
	}

	// Optional build discard
	if IsBuildDiscardArgs(args) {
		buildDiscardArgsList, err := GetBuildDiscardCommandArgs(args)
		if err != nil {
			logrus.Println("GetBuildDiscardCommandArgs error: ", err)
			return cmdList, err
		}
		cmdList = append(cmdList, buildDiscardArgsList...)
	}

	return cmdList, nil
}

// extractPyPIRepoName extracts the repository name from a target path.
// It strips the "api/pypi/" prefix if present and returns the bare repo name.
func extractPyPIRepoName(target string) string {
	target = strings.TrimSpace(target)
	target = strings.TrimSuffix(target, "/")

	if idx := strings.Index(target, "api/pypi/"); idx != -1 {
		target = target[idx+len("api/pypi/"):]
	}

	parts := strings.SplitN(target, "/", 2)
	return parts[0]
}
