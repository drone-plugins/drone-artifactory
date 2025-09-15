package plugin

import (
    "fmt"
    "os"
    "path/filepath"
    "strings"

    "github.com/sirupsen/logrus"

    artifactory "github.com/jfrog/jfrog-client-go/artifactory"
    "github.com/jfrog/jfrog-client-go/artifactory/auth"
    jfconfig "github.com/jfrog/jfrog-client-go/config"
)

// createServiceManager builds an Artifactory services manager configured from Args.
// It handles URL sanitization, auth, TLS (custom cert path and insecure), retries and proxy envs.
func createServiceManager(args Args) (artifactory.ArtifactoryServicesManager, func(), error) {
    // Ensure URL is set
    if args.URL == "" {
        return nil, nil, fmt.Errorf("artifactory URL must be provided")
    }

    // URL: For Artifactory operations, sanitize to include /artifactory/.
    // For Xray scan, use URL as-is (docs expect https://.../xray).
    sanitizedURL := args.URL
    var err error
    if strings.ToLower(args.Command) != "scan" {
        sanitizedURL, err = sanitizeURL(args.URL)
        if err != nil {
            return nil, nil, err
        }
    }

    // Propagate proxy envs if requested (same behavior as current plugin)
    enableProxy := parseBoolOrDefault(false, args.EnableProxy)
    if enableProxy {
        setSecureConnectProxies()
    }

    // Prepare Artifactory details
    details := auth.NewArtifactoryDetails()
    details.SetUrl(sanitizedURL)

    // Auth precedence: access token -> user/pass -> api key
    switch {
    case args.AccessToken != "":
        details.SetAccessToken(args.AccessToken)
    case args.Username != "" && args.Password != "":
        details.SetUser(args.Username)
        details.SetPassword(args.Password)
    case args.APIKey != "":
        details.SetApiKey(args.APIKey)
    default:
        return nil, nil, fmt.Errorf("authentication not provided: set access_token, or username/password, or api_key")
    }

    // TLS settings
    insecure := parseBoolOrDefault(false, args.Insecure)
    // Handle PEM path (write if needed) - passed via config builder below
    var certsDir string
    if !insecure && (args.PEMFileContents != "" || args.PEMFilePath != "") {
        certFilePath, err := ensurePEMCertOnDisk(args)
        if err != nil {
            return nil, nil, err
        }
        if certFilePath != "" {
            certsDir = filepath.Dir(certFilePath)
        }
    }

    // Build JFrog client config
    cfgBuilder := jfconfig.NewConfigBuilder()
    cfgBuilder.SetServiceDetails(details)
    if certsDir != "" {
        cfgBuilder.SetCertificatesPath(certsDir)
    }
    if insecure {
        cfgBuilder.SetInsecureTls(true)
    }
    if args.Retries > 0 {
        cfgBuilder.SetHttpRetries(args.Retries)
    }
    if args.Threads > 0 {
        cfgBuilder.SetThreads(args.Threads)
    }
    cfg, err := cfgBuilder.Build()
    if err != nil {
        return nil, nil, err
    }

    // Create Artifactory services manager
    rtManager, err := artifactory.New(cfg)
    if err != nil {
        return nil, nil, err
    }

    // No temporary resources to cleanup for now
    cleanup := func() {}
    return rtManager, cleanup, nil
}

// ensurePEMCertOnDisk writes PEMFileContents to the desired path when needed and returns the PEM path to use.
func ensurePEMCertOnDisk(args Args) (string, error) {
    if args.PEMFileContents == "" && args.PEMFilePath == "" {
        return "", nil
    }

    var path string
    if args.PEMFilePath != "" {
        path = args.PEMFilePath
    } else {
        // Default paths match current behavior
        // Windows path handled by WriteKnownGoodServerCertsForTls as well
        path = defaultPEMPath()
    }

    // Create dir and write file if contents provided and file not present
    if args.PEMFileContents != "" {
        if _, err := os.Stat(path); os.IsNotExist(err) {
            dir := filepath.Dir(path)
            if mkErr := os.MkdirAll(dir, 0700); mkErr != nil {
                return "", fmt.Errorf("error creating pem folder: %w", mkErr)
            }
            if wrErr := os.WriteFile(path, []byte(args.PEMFileContents), 0600); wrErr != nil {
                return "", fmt.Errorf("error writing pem file: %w", wrErr)
            }
            logrus.Printf("Created PEM certificate at %q\n", path)
        }
    }
    return path, nil
}

func defaultPEMPath() string {
    // Match defaults used elsewhere in the plugin
    if isWindows() {
        return "C:/users/ContainerAdministrator/.jfrog/security/certs/cert.pem"
    }
    return "/root/.jfrog/security/certs/cert.pem"
}

func isWindows() bool {
    // Small helper to avoid importing runtime in every file
    return os.PathSeparator == '\\'
}
