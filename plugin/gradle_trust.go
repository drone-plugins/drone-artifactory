package plugin

import (
	"bytes"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sirupsen/logrus"
)

const gradleTrustStorePassword = "changeit"

var runKeytool = func(keytool string, arguments ...string) error {
	var output bytes.Buffer
	cmd := exec.Command(keytool, arguments...)
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(output.String()))
	}
	return nil
}

func pinJFrogCLIHome(environment []string) []string {
	return replaceEnvironmentValue(environment, "JFROG_CLI_HOME_DIR", jfrogCLIHome())
}

// prepareGradleEnvironment makes pem_file_contents visible to the Gradle JVM.
// JAVA_TOOL_OPTIONS reaches both the Gradle client and any daemon it forks,
// unlike GRADLE_OPTS, which only configures the client.
func prepareGradleEnvironment(command []string, args Args, environment []string) ([]string, func(), error) {
	noop := func() {}
	if len(command) < 2 || command[1] != GradleCmd {
		return environment, noop, nil
	}
	if args.PEMFileContents == "" || parseBoolOrDefault(false, args.Insecure) {
		return environment, noop, nil
	}
	for _, name := range []string{"JAVA_TOOL_OPTIONS", "GRADLE_OPTS", "JAVA_OPTS"} {
		if strings.Contains(environmentValue(environment, name), "javax.net.ssl.trustStore") {
			return environment, noop, nil
		}
	}

	certificates := splitPEMCertificates(args.PEMFileContents)
	if len(certificates) == 0 {
		return nil, nil, fmt.Errorf("pem_file_contents does not contain a PEM certificate")
	}

	javaHome := resolveJavaHome(environment)
	sourceStore := filepath.Join(javaHome, "lib", "security", "cacerts")
	keytool, err := findKeytool(javaHome)
	if javaHome == "" || err != nil {
		logrus.Warnf("pem_file_contents was not added to the Gradle truststore: no JDK with keytool was found")
		return environment, noop, nil
	}
	if _, err := os.Stat(sourceStore); err != nil {
		logrus.Warnf("pem_file_contents was not added to the Gradle truststore: %v", err)
		return environment, noop, nil
	}

	directory, err := os.MkdirTemp("", "drone-artifactory-gradle-trust-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create Gradle truststore: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(directory) }

	trustStore := filepath.Join(directory, "truststore.p12")
	if err := copyFile(sourceStore, trustStore); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("copy JDK truststore %q: %w", sourceStore, err)
	}
	for index, certificate := range certificates {
		certificatePath := filepath.Join(directory, fmt.Sprintf("ca-%d.pem", index))
		if err := os.WriteFile(certificatePath, certificate, 0600); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("write Gradle CA certificate: %w", err)
		}
		err := runKeytool(keytool,
			"-importcert", "-noprompt", "-trustcacerts",
			"-alias", fmt.Sprintf("drone-artifactory-ca-%d", index),
			"-file", certificatePath,
			"-keystore", trustStore,
			"-storepass", gradleTrustStorePassword,
		)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("import pem_file_contents into the Gradle truststore: %w", err)
		}
	}

	options := strings.Join([]string{
		"-Djavax.net.ssl.trustStore=" + trustStore,
		"-Djavax.net.ssl.trustStorePassword=" + gradleTrustStorePassword,
		"-Djavax.net.ssl.trustStoreType=PKCS12",
	}, " ")
	if existing := strings.TrimSpace(environmentValue(environment, "JAVA_TOOL_OPTIONS")); existing != "" {
		options = existing + " " + options
	}
	return replaceEnvironmentValue(environment, "JAVA_TOOL_OPTIONS", options), cleanup, nil
}

func splitPEMCertificates(contents string) [][]byte {
	var certificates [][]byte
	rest := []byte(contents)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return certificates
		}
		if block.Type == "CERTIFICATE" {
			certificates = append(certificates, pem.EncodeToMemory(block))
		}
	}
}

// resolveJavaHome falls back to the java executable on PATH because the Linux
// images install the distribution JDK without setting JAVA_HOME.
func resolveJavaHome(environment []string) string {
	if javaHome := environmentValue(environment, "JAVA_HOME"); javaHome != "" {
		return javaHome
	}
	java, err := exec.LookPath("java")
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(java)
	if err != nil {
		return ""
	}
	return filepath.Dir(filepath.Dir(resolved))
}

func findKeytool(javaHome string) (string, error) {
	name := "keytool"
	if runtime.GOOS == "windows" {
		name = "keytool.exe"
	}
	if javaHome != "" {
		candidate := filepath.Join(javaHome, "bin", name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	path, err := exec.LookPath("keytool")
	if err != nil {
		return "", fmt.Errorf("keytool is required to add pem_file_contents to the Gradle truststore: %w", err)
	}
	return path, nil
}

func environmentValue(environment []string, name string) string {
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if found && strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func copyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}
