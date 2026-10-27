package plugin

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
)

const (
	MvnCmd       = "mvn"
	MvnConfig    = "mvn-config"
	BuildPublish = "build-publish"
	Deploy       = "deploy"
	Publish      = "publish"
	GradleConfig = "gradle-config"
	GradleCmd    = "gradle"
	NpmConfig    = "npm-config"
	NpmCmd       = "npm"
	tmpServerId  = "tmpServerId"
)

func HandleRtCommands(ctx context.Context, args Args) error {
	artifactoryURL, err := normalizeArtifactoryURL(args.URL)
	if err != nil {
		return err
	}
	args.URL = artifactoryURL

	commandsList, err := GetRtCommandsList(args)
	if err != nil {
		logrus.Println("Error Unable to get rt commands list err = ", err)
		return err
	}
	defer cleanupTemporarySpecs(commandsList)

	err = WriteKnownGoodServerCertsForTls(args)
	if err != nil {
		logrus.Println("Error Unable to write TLS certs err = ", err)
		return err
	}
	trustCleanup, err := prepareNpmSystemTrust(args)
	if err != nil {
		return err
	}
	defer trustCleanup()

	for index, cmd := range commandsList {
		execArgs := []string{getJfrogBin()}
		execArgs = append(execArgs, cmd...)
		logrus.Printf("Running Artifactory command %d/%d: %s", index+1, len(commandsList), commandLabel(execArgs))
		err := ExecCommand(ctx, args, execArgs)
		if err != nil {
			logrus.Println("Error Unable to run err = ", err)
			return err
		}
	}

	if shouldPublishBuildInfo(args) {
		if err := publishBuildInfo(ctx, args); err != nil {
			logrus.Println("Error publishing build info: ", err)
			return err
		}
	}

	return nil
}

func shouldPublishBuildInfo(args Args) bool {
	if args.Command == "publish-build-info" || args.Command == "add-build-dependencies" {
		return false
	}
	if args.PublishBuildInfo {
		return true
	}
	// Preserve the existing Maven and Gradle publish contract. npm follows the
	// explicit publish_build_info setting documented for the new integration.
	return args.Command == Publish && (args.BuildTool == MvnCmd || args.BuildTool == GradleCmd)
}

func WriteKnownGoodServerCertsForTls(args Args) error {
	insecure := parseBoolOrDefault(false, args.Insecure)
	if insecure || args.PEMFileContents == "" {
		return nil
	}

	targets := []string{jfrogCLICertificatePath()}
	if custom := serverCertPath(args); !samePath(custom, targets[0]) {
		targets = append(targets, custom)
	}
	for _, path := range targets {
		if err := writePEMFile(path, args.PEMFileContents); err != nil {
			return err
		}
	}
	return nil
}

func writePEMFile(path, contents string) error {
	logrus.Printf("Writing pem file at %q\n", path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("error creating pem folder: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".cert-*.tmp")
	if err != nil {
		return fmt.Errorf("error creating temporary pem file: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return fmt.Errorf("error setting pem permissions: %w", err)
	}
	if _, err := temp.WriteString(contents); err != nil {
		temp.Close()
		return fmt.Errorf("error writing pem file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("error closing pem file: %w", err)
	}
	// Windows Rename does not replace an existing destination. Remove it only
	// after the complete replacement has been written and closed.
	if runtime.GOOS == "windows" {
		_ = os.Remove(path)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("error replacing pem file: %w", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("error securing pem file: %w", err)
	}
	logrus.Printf("Successfully wrote pem file at %q\n", path)
	return nil
}

var windowsCertificateStoreCommand = func(action, thumbprint, certificatePath string) ([]byte, error) {
	const script = `$ErrorActionPreference = 'Stop'
$store = [System.Security.Cryptography.X509Certificates.X509Store]::new(
  [System.Security.Cryptography.X509Certificates.StoreName]::Root,
  [System.Security.Cryptography.X509Certificates.StoreLocation]::LocalMachine
)
$store.Open([System.Security.Cryptography.X509Certificates.OpenFlags]::ReadWrite)
try {
  $matches = $store.Certificates.Find(
    [System.Security.Cryptography.X509Certificates.X509FindType]::FindByThumbprint,
    $env:DRONE_ARTIFACTORY_CERT_THUMBPRINT,
    $false
  )
  switch ($env:DRONE_ARTIFACTORY_CERT_ACTION) {
    'contains' {
      if ($matches.Count -eq 0) { throw 'certificate not found' }
    }
    'add' {
      $certificate = [System.Security.Cryptography.X509Certificates.X509Certificate2]::new(
        $env:DRONE_ARTIFACTORY_CERT_PATH
      )
      try { $store.Add($certificate) } finally { $certificate.Dispose() }
    }
    'remove' {
      foreach ($certificate in $matches) { $store.Remove($certificate) }
    }
    default { throw "unsupported certificate store action: $env:DRONE_ARTIFACTORY_CERT_ACTION" }
  }
} finally {
  $store.Close()
}`
	command := exec.Command("pwsh.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	command.Env = append(
		os.Environ(),
		"DRONE_ARTIFACTORY_CERT_ACTION="+action,
		"DRONE_ARTIFACTORY_CERT_THUMBPRINT="+thumbprint,
		"DRONE_ARTIFACTORY_CERT_PATH="+certificatePath,
	)
	return command.CombinedOutput()
}

func parsePEMCertificates(contents string) ([]*x509.Certificate, error) {
	remaining := []byte(contents)
	var certificates []*x509.Certificate
	for {
		remaining = bytes.TrimSpace(remaining)
		if len(remaining) == 0 {
			break
		}
		block, rest := pem.Decode(remaining)
		if block == nil {
			return nil, fmt.Errorf("pem_file_contents contains invalid PEM data")
		}
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("pem_file_contents contains unsupported PEM block %q", block.Type)
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("pem_file_contents contains an invalid certificate: %w", err)
		}
		certificates = append(certificates, certificate)
		remaining = rest
	}
	if len(certificates) == 0 {
		return nil, fmt.Errorf("pem_file_contents does not contain a PEM certificate")
	}
	return certificates, nil
}

func prepareNpmSystemTrust(args Args) (func(), error) {
	return prepareNpmSystemTrustForOS(args, runtime.GOOS)
}

func prepareNpmSystemTrustForOS(args Args, goos string) (func(), error) {
	if goos != "windows" || args.BuildTool != NpmCmd || args.PEMFileContents == "" ||
		parseBoolOrDefault(false, args.Insecure) {
		return func() {}, nil
	}
	certificates, err := parsePEMCertificates(args.PEMFileContents)
	if err != nil {
		return nil, err
	}
	tempDirectory, err := os.MkdirTemp("", "drone-artifactory-windows-root-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary Windows trust directory: %w", err)
	}
	added := make([]string, 0, len(certificates))
	cleanup := func() {
		for index := len(added) - 1; index >= 0; index-- {
			output, removeErr := windowsCertificateStoreCommand("remove", added[index], "")
			if removeErr != nil {
				logrus.Warnf(
					"could not remove temporary npm certificate %s from the Windows local-machine ROOT store: %v: %s",
					added[index],
					removeErr,
					strings.TrimSpace(string(output)),
				)
			}
		}
		if removeErr := os.RemoveAll(tempDirectory); removeErr != nil {
			logrus.Warnf("could not remove temporary npm trust directory: %v", removeErr)
		}
	}

	for index, certificate := range certificates {
		// Windows certificate stores identify entries by a SHA-1 thumbprint.
		// This is an identifier only, not a cryptographic validation decision.
		sum := sha1.Sum(certificate.Raw) // #nosec G401
		thumbprint := strings.ToUpper(hex.EncodeToString(sum[:]))
		if _, lookupErr := windowsCertificateStoreCommand("contains", thumbprint, ""); lookupErr == nil {
			continue
		}
		certificatePath := filepath.Join(tempDirectory, fmt.Sprintf("certificate-%d.cer", index))
		if err := os.WriteFile(certificatePath, certificate.Raw, 0600); err != nil {
			cleanup()
			return nil, fmt.Errorf("write temporary Windows trust certificate: %w", err)
		}
		output, err := windowsCertificateStoreCommand("add", thumbprint, certificatePath)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf(
				"add pem_file_contents certificate %s to the Windows local-machine ROOT store: %w: %s",
				thumbprint,
				err,
				strings.TrimSpace(string(output)),
			)
		}
		added = append(added, thumbprint)
	}
	logrus.Printf(
		"Added %d pem_file_contents certificate(s) to the Windows local-machine ROOT store for npm",
		len(added),
	)
	return cleanup, nil
}

func serverCertPath(args Args) string {
	if args.PEMFilePath != "" {
		return args.PEMFilePath
	}
	return jfrogCLICertificatePath()
}

// jfrogCLIHome follows JFrog CLI 2.115 resolution order and is pinned into
// JFROG_CLI_HOME_DIR before every command. A customer PEM written to any other
// profile is invisible to the CLI's /api/system/version request.
func jfrogCLIHome() string {
	if home := strings.TrimSpace(os.Getenv("JFROG_CLI_HOME_DIR")); home != "" {
		if absolute, err := filepath.Abs(home); err == nil {
			return absolute
		}
		return home
	}
	userHome := os.Getenv("HOME")
	if userHome == "" {
		userHome = os.Getenv("USERPROFILE")
	}
	if userHome == "" {
		if runtime.GOOS == "windows" {
			userHome = `C:\Users\ContainerAdministrator`
		} else {
			userHome = "/root"
		}
	}
	return filepath.Join(userHome, ".jfrog")
}

func jfrogCLICertificatePath() string {
	return filepath.Join(jfrogCLIHome(), "security", "certs", "cert.pem")
}

func samePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func GetRtCommandsList(args Args) ([][]string, error) {
	logrus.Println("Handling rt command handleRtCommand")
	logrus.Println("Checking GetRtCommandsList args.Command ", args.Command)

	var (
		commandsList [][]string
		err          error
	)
	switch args.Command {
	case "download":
		logIgnoredBuildTool(args)
		logrus.Println("download start")
		commandsList, err = GetDownloadCommandArgs(args)
	case "cleanup":
		logIgnoredBuildTool(args)
		logrus.Println("cleanup start")
		commandsList, err = GetCleanupCommandArgs(args)
	case "scan":
		logIgnoredBuildTool(args)
		logrus.Println("scan start")
		commandsList, err = GetScanCommandArgs(args)
	case "publish-build-info":
		logIgnoredBuildTool(args)
		logrus.Println("publish-build-info start")
		commandsList, err = GetBuildInfoPublishCommandArgs(args)
	case "promote":
		logIgnoredBuildTool(args)
		logrus.Println("promote start")
		commandsList, err = GetPromoteCommandArgs(args)
	case "add-build-dependencies":
		logIgnoredBuildTool(args)
		logrus.Println("add-build-dependencies start")
		commandsList, err = GetAddDependenciesCommandArgs(args)
	case "build-discard":
		logIgnoredBuildTool(args)
		logrus.Println("build-discard start")
		commandsList, err = GetBuildDiscardCommandArgs(args)
	default:
		commandsList, err = getBuildToolCommands(args)
	}
	if err != nil {
		return nil, err
	}
	if len(commandsList) == 0 {
		return nil, fmt.Errorf(
			"no Artifactory commands generated for build_tool/command combination: %q/%q",
			args.BuildTool,
			args.Command,
		)
	}
	return commandsList, nil
}

func logIgnoredBuildTool(args Args) {
	if args.BuildTool != "" {
		logrus.Warnf(
			"build_tool %q is ignored for standalone command %q; remove it because this compatibility behavior is deprecated",
			args.BuildTool,
			args.Command,
		)
	}
}

func getBuildToolCommands(args Args) ([][]string, error) {
	switch {
	case args.BuildTool == MvnCmd && (args.Command == "" || args.Command == "build"):
		logrus.Println("mvn build start")
		return GetMavenBuildCommandArgs(args)
	case args.BuildTool == MvnCmd && args.Command == Publish:
		return GetMavenPublishCommand(args)
	case args.BuildTool == GradleCmd && (args.Command == "" || args.Command == "build"):
		logrus.Println("Gradle build start")
		return GetGradleCommandArgs(args)
	case args.BuildTool == GradleCmd && args.Command == Publish:
		logrus.Println("Gradle publish start")
		return GetGradlePublishCommand(args)
	case args.BuildTool == NpmCmd:
		logrus.Println("npm operation start")
		return GetNpmCommandArgs(args)
	default:
		return nil, fmt.Errorf(
			"unsupported build_tool/command combination: %q/%q",
			args.BuildTool,
			args.Command,
		)
	}
}

func GetShellForOs(osName string) (string, string, error) {
	return resolveShell(osName, exec.LookPath, os.Stat)
}

type JsonTagToExeFlagMapStringItem struct {
	FlagName         string
	PluginArgJsonTag string
	IsMandatory      bool
	StopOnError      bool
}

func PopulateArgs(tmpCommandsList *[]string, args *Args,
	jsonTagToExeFlagMapStringItemList []JsonTagToExeFlagMapStringItem) error {

	for _, jsonTagToExeFlagMapStringItem := range jsonTagToExeFlagMapStringItemList {
		flagName := jsonTagToExeFlagMapStringItem.FlagName
		pluginArgJsonTag := jsonTagToExeFlagMapStringItem.PluginArgJsonTag
		pluginArgValue, err := GetFieldAddress[*Args, string](args, pluginArgJsonTag)

		if err != nil {
			if jsonTagToExeFlagMapStringItem.IsMandatory || jsonTagToExeFlagMapStringItem.StopOnError {
				logrus.Println("GetFieldAddress error: ", err)
				return err
			}
			//logrus.Println("GetFieldAddress error: ", err)
			continue
		}

		if pluginArgValue == nil {
			if jsonTagToExeFlagMapStringItem.IsMandatory || jsonTagToExeFlagMapStringItem.StopOnError {
				logrus.Println("missing mandatory field: ", pluginArgJsonTag)
				return fmt.Errorf("missing mandatory field %s", pluginArgJsonTag)
			}
			logrus.Println("missing mandatory field: ", pluginArgJsonTag)
			continue
		}

		if pluginArgValue == nil &&
			jsonTagToExeFlagMapStringItem.IsMandatory || jsonTagToExeFlagMapStringItem.StopOnError {
			logrus.Println("missing mandatory field: ", pluginArgJsonTag)
			return fmt.Errorf("missing mandatory field %s", pluginArgJsonTag)
		}
		AppendStringArg(tmpCommandsList, flagName, pluginArgValue)
	}

	return nil
}

func AppendStringArg(argsList *[]string, argName string, argValue *string) {

	if argsList == nil {
		logrus.Println("argsList is nil")
		return
	}

	if argValue == nil {
		logrus.Println("argValue is nil")
		return
	}

	if len(*argValue) > 0 {
		*argsList = append(*argsList, argName+*argValue)
	}
}

var tagFieldCache sync.Map

func precomputeTagMapping(structType reflect.Type) map[string]int {
	tagMap := make(map[string]int)
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		tag := field.Tag.Get("envconfig")
		if tag != "" {
			tagMap[tag] = i
		}
	}
	return tagMap
}

func getTagMapping(structType reflect.Type) map[string]int {
	if cachedMapping, ok := tagFieldCache.Load(structType); ok {
		return cachedMapping.(map[string]int)
	}

	tagMap := precomputeTagMapping(structType)
	tagFieldCache.Store(structType, tagMap)
	return tagMap
}

func GetFieldAddress[ST, VT any](args ST, argJsonTag string) (*VT, error) {
	v := reflect.ValueOf(args)
	if v.Kind() != reflect.Ptr {
		return nil, fmt.Errorf("args must be a pointer to a struct; got %T", args)
	}
	if v.Elem().Kind() != reflect.Struct {
		return nil, fmt.Errorf("args must point to a struct; got pointer to %s", v.Elem().Kind())
	}

	v = v.Elem()
	t := v.Type()

	tagMap := getTagMapping(t)

	fieldIndex, found := tagMap[argJsonTag]
	if !found {
		return nil, fmt.Errorf("field with tag '%s' not found in struct type '%s'", argJsonTag, t.Name())
	}

	fieldValue := v.Field(fieldIndex)
	if fieldValue.CanAddr() {
		if fieldValue.Type().AssignableTo(reflect.TypeOf((*VT)(nil)).Elem()) {
			return fieldValue.Addr().Interface().(*VT), nil
		}
		return nil, fmt.Errorf("field with tag '%s' in struct '%s' is not of type '%T'; actual type is '%s'",
			argJsonTag, t.Name(), new(VT), fieldValue.Type().String())
	}

	return nil, fmt.Errorf("field with tag '%s' in struct '%s' cannot be addressed", argJsonTag, t.Name())
}

func GetConfigAddConfigCommandArgs(srvConfigStr, userName, password, url,
	accessToken, apiKey string) ([]string, error) {

	if srvConfigStr == "" {
		srvConfigStr = tmpServerId
	}
	platformURL, err := normalizePlatformURL(url)
	if err != nil {
		return nil, err
	}

	authParams, err := setAuthParams([]string{}, Args{Username: userName,
		Password: password, AccessToken: accessToken, APIKey: apiKey})
	if err != nil {
		logrus.Println("setAuthParams error: ", err)
		return []string{""}, err
	}

	cfgCommand := []string{"config", "add", srvConfigStr, "--url=" + platformURL}
	cfgCommand = append(cfgCommand, authParams...)
	cfgCommand = append(cfgCommand, "--interactive=false")
	return cfgCommand, nil
}

func IsBuildDiscardArgs(args Args) bool {
	if len(args.Async) > 0 ||
		len(args.DeleteArtifacts) > 0 ||
		len(args.ExcludeBuilds) > 0 ||
		len(args.MaxBuilds) > 0 ||
		len(args.MaxDays) > 0 {
		return true
	}
	return false
}
