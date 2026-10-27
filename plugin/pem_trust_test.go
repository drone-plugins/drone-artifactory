package plugin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testCertificatePEM(t *testing.T, commonName string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestWriteKnownGoodServerCertsUsesJFrogCLIHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JFROG_CLI_HOME_DIR", home)
	customPath := filepath.Join(t.TempDir(), "customer-ca.pem")

	if err := WriteKnownGoodServerCertsForTls(Args{PEMFileContents: "certificate", PEMFilePath: customPath}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(home, "security", "certs", "cert.pem"), customPath} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("PEM was not written to %s: %v", path, err)
		}
		if string(content) != "certificate" {
			t.Fatalf("unexpected PEM at %s: %q", path, content)
		}
	}
}

func TestPinJFrogCLIHomeUsesTheCertificateHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JFROG_CLI_HOME_DIR", home)
	environment := pinJFrogCLIHome([]string{"JFROG_CLI_HOME_DIR=stale", "PATH=original"})
	if got := environmentValue(environment, "JFROG_CLI_HOME_DIR"); got != home {
		t.Fatalf("JFROG_CLI_HOME_DIR=%q, want %q", got, home)
	}
	if !strings.HasPrefix(jfrogCLICertificatePath(), home) {
		t.Fatalf("certificate path %q is outside the pinned home %q", jfrogCLICertificatePath(), home)
	}
}

func TestParsePEMCertificatesRejectsNonCertificateData(t *testing.T) {
	for name, contents := range map[string]string{
		"plain text": "not a certificate",
		"private key": string(pem.EncodeToMemory(&pem.Block{
			Type:  "PRIVATE KEY",
			Bytes: []byte("not a key"),
		})),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parsePEMCertificates(contents); err == nil {
				t.Fatal("expected invalid pem_file_contents to fail")
			}
		})
	}
}

func TestPrepareNpmSystemTrustImportsOnlyMissingWindowsRoots(t *testing.T) {
	bundle := testCertificatePEM(t, "Already trusted CA") + testCertificatePEM(t, "Temporary customer CA")
	originalCommand := windowsCertificateStoreCommand
	defer func() { windowsCertificateStoreCommand = originalCommand }()

	var calls [][]string
	var importedPath string
	lookups := 0
	windowsCertificateStoreCommand = func(action, thumbprint, certificatePath string) ([]byte, error) {
		calls = append(calls, []string{action, thumbprint, certificatePath})
		switch action {
		case "contains":
			lookups++
			if lookups == 1 {
				return nil, nil
			}
			return nil, errors.New("certificate not found")
		case "add":
			importedPath = certificatePath
			if _, err := os.Stat(importedPath); err != nil {
				t.Fatalf("temporary certificate was not written: %v", err)
			}
			return nil, nil
		case "remove":
			return nil, nil
		default:
			t.Fatalf("unexpected certificate store action: %s", action)
			return nil, nil
		}
	}

	cleanup, err := prepareNpmSystemTrustForOS(
		Args{BuildTool: NpmCmd, PEMFileContents: bundle},
		"windows",
	)
	if err != nil {
		t.Fatal(err)
	}
	if importedPath == "" {
		t.Fatal("missing certificate was not imported")
	}
	cleanup()
	if _, err := os.Stat(filepath.Dir(importedPath)); !os.IsNotExist(err) {
		t.Fatalf("temporary trust directory was not removed: %v", err)
	}

	var addCount, deleteCount int
	for _, call := range calls {
		if call[0] == "add" {
			addCount++
		}
		if call[0] == "remove" {
			deleteCount++
		}
	}
	if addCount != 1 || deleteCount != 1 {
		t.Fatalf("certificate store add/remove calls = %d/%d, want 1/1: %v", addCount, deleteCount, calls)
	}
}

func TestPrepareNpmSystemTrustSkipsOtherPaths(t *testing.T) {
	originalCommand := windowsCertificateStoreCommand
	defer func() { windowsCertificateStoreCommand = originalCommand }()
	windowsCertificateStoreCommand = func(action, thumbprint, certificatePath string) ([]byte, error) {
		t.Fatalf("Windows certificate store command must not run: %s %s %s", action, thumbprint, certificatePath)
		return nil, nil
	}
	certificate := testCertificatePEM(t, "Customer CA")
	for name, testCase := range map[string]struct {
		args Args
		goos string
	}{
		"non-windows": {Args{BuildTool: NpmCmd, PEMFileContents: certificate}, "linux"},
		"non-npm":     {Args{BuildTool: GradleCmd, PEMFileContents: certificate}, "windows"},
		"insecure":    {Args{BuildTool: NpmCmd, PEMFileContents: certificate, Insecure: "true"}, "windows"},
		"no-pem":      {Args{BuildTool: NpmCmd}, "windows"},
	} {
		t.Run(name, func(t *testing.T) {
			cleanup, err := prepareNpmSystemTrustForOS(testCase.args, testCase.goos)
			if err != nil {
				t.Fatal(err)
			}
			cleanup()
		})
	}
}

func TestPrepareGradleEnvironmentImportsCustomCA(t *testing.T) {
	keytool, err := exec.LookPath("keytool")
	if err != nil {
		t.Skip("keytool is unavailable")
	}
	resolved, err := filepath.EvalSymlinks(keytool)
	if err != nil {
		t.Fatal(err)
	}
	javaHome := filepath.Dir(filepath.Dir(resolved))
	if _, err := os.Stat(filepath.Join(javaHome, "lib", "security", "cacerts")); err != nil {
		t.Skipf("JDK truststore is unavailable under %s", javaHome)
	}

	bundle := testCertificatePEM(t, "Customer Root CA") + testCertificatePEM(t, "Customer Issuing CA")
	environment, cleanup, err := prepareGradleEnvironment(
		[]string{"jf", "gradle", "clean", "artifactoryDeploy"},
		Args{PEMFileContents: bundle},
		[]string{"JAVA_HOME=" + javaHome, "JAVA_TOOL_OPTIONS=-Xms64m -Xmx1024m"},
	)
	if err != nil {
		t.Fatal(err)
	}

	options := environmentValue(environment, "JAVA_TOOL_OPTIONS")
	if !strings.HasPrefix(options, "-Xms64m -Xmx1024m ") {
		t.Fatalf("existing JAVA_TOOL_OPTIONS were not preserved: %q", options)
	}
	var trustStore string
	for _, option := range strings.Fields(options) {
		if value, found := strings.CutPrefix(option, "-Djavax.net.ssl.trustStore="); found {
			trustStore = value
		}
	}
	if trustStore == "" {
		t.Fatalf("Gradle truststore was not configured: %q", options)
	}

	listing, err := exec.Command(resolved, "-list", "-keystore", trustStore, "-storepass", gradleTrustStorePassword).CombinedOutput()
	if err != nil {
		t.Fatalf("cannot list Gradle truststore: %v: %s", err, listing)
	}
	for _, alias := range []string{"drone-artifactory-ca-0", "drone-artifactory-ca-1"} {
		if !strings.Contains(string(listing), alias) {
			t.Fatalf("Gradle truststore is missing %s", alias)
		}
	}
	if !strings.Contains(string(listing), "trustedCertEntry") || strings.Count(string(listing), "trustedCertEntry") < 3 {
		t.Fatal("Gradle truststore did not retain the JDK public roots")
	}

	cleanup()
	if _, err := os.Stat(trustStore); !os.IsNotExist(err) {
		t.Fatalf("Gradle truststore was not cleaned up: %v", err)
	}
}

func TestPrepareGradleEnvironmentRespectsExplicitTrustStore(t *testing.T) {
	original := []string{"JAVA_TOOL_OPTIONS=-Djavax.net.ssl.trustStoreType=Windows-ROOT"}
	environment, cleanup, err := prepareGradleEnvironment(
		[]string{"jf", "gradle", "artifactoryDeploy"},
		Args{PEMFileContents: "certificate"},
		append([]string(nil), original...),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if environmentValue(environment, "JAVA_TOOL_OPTIONS") != "-Djavax.net.ssl.trustStoreType=Windows-ROOT" {
		t.Fatalf("explicit truststore configuration was replaced: %v", environment)
	}
}

func TestPrepareGradleEnvironmentSkipsUnrelatedOrInsecureCommands(t *testing.T) {
	for name, testCase := range map[string]struct {
		command []string
		args    Args
	}{
		"npm":      {[]string{"jf", "npm", "install"}, Args{PEMFileContents: "certificate"}},
		"config":   {[]string{"jf", "gradle-config"}, Args{PEMFileContents: "certificate"}},
		"insecure": {[]string{"jf", "gradle", "publish"}, Args{PEMFileContents: "certificate", Insecure: "true"}},
		"no-pem":   {[]string{"jf", "gradle", "publish"}, Args{}},
	} {
		t.Run(name, func(t *testing.T) {
			environment, cleanup, err := prepareGradleEnvironment(testCase.command, testCase.args, []string{"PATH=original"})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if environmentValue(environment, "JAVA_TOOL_OPTIONS") != "" {
				t.Fatalf("unexpected Gradle trust configuration: %v", environment)
			}
		})
	}
}

func TestPrepareGradleEnvironmentContinuesWithoutJDK(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	environment, cleanup, err := prepareGradleEnvironment(
		[]string{"jf", "gradle", "publish"},
		Args{PEMFileContents: testCertificatePEM(t, "Customer Root CA")},
		[]string{"PATH=" + os.Getenv("PATH")},
	)
	if err != nil {
		t.Fatalf("missing JDK must not fail the step: %v", err)
	}
	defer cleanup()
	if environmentValue(environment, "JAVA_TOOL_OPTIONS") != "" {
		t.Fatalf("unexpected Gradle trust configuration without a JDK: %v", environment)
	}
}

func TestPrepareGradleEnvironmentRejectsNonPEMContents(t *testing.T) {
	_, _, err := prepareGradleEnvironment(
		[]string{"jf", "gradle", "publish"},
		Args{PEMFileContents: "not a certificate"},
		[]string{"JAVA_HOME=" + t.TempDir()},
	)
	if err == nil || !strings.Contains(err.Error(), "does not contain a PEM certificate") {
		t.Fatalf("expected a PEM validation error, got %v", err)
	}
}
