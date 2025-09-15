package plugin

import (
    "bufio"
    "encoding/xml"
    "fmt"
    "os"
    "os/exec"
    "strings"

    artifactory "github.com/jfrog/jfrog-client-go/artifactory"
    build "github.com/jfrog/build-info-go/build"
    buildinfo "github.com/jfrog/build-info-go/entities"
    "github.com/sirupsen/logrus"
)

// BuildInfoManager wraps jfrog-client-go and build-info-go to persist partials across steps
// and aggregate them on publish, mirroring JFrog CLI behavior.
type BuildInfoManager struct {
    rt      artifactory.ArtifactoryServicesManager
    service *build.BuildInfoService
}

func NewBuildInfoManager(rt artifactory.ArtifactoryServicesManager) *BuildInfoManager {
    return &BuildInfoManager{rt: rt, service: build.NewBuildInfoService()}
}

// SaveArtifactsPartial stores artifacts as a partial for later aggregation.
func (m *BuildInfoManager) SaveArtifactsPartial(args Args, artifacts []buildinfo.Artifact) error {
    if args.BuildName == "" || args.BuildNumber == "" {
        return nil
    }
    b, err := m.service.GetOrCreateBuildWithProject(args.BuildName, args.BuildNumber, args.Project)
    if err != nil {
        return err
    }
    modID, modType := computeModule(args)
    partial := &buildinfo.Partial{ModuleId: modID, ModuleType: modType, Artifacts: artifacts}
    return b.SavePartialBuildInfo(partial)
}

// SaveDependenciesPartial stores dependencies as a partial for later aggregation.
func (m *BuildInfoManager) SaveDependenciesPartial(args Args, deps []buildinfo.Dependency) error {
    if args.BuildName == "" || args.BuildNumber == "" {
        return nil
    }
    b, err := m.service.GetOrCreateBuildWithProject(args.BuildName, args.BuildNumber, args.Project)
    if err != nil {
        return err
    }
    modID, modType := computeModule(args)
    partial := &buildinfo.Partial{ModuleId: modID, ModuleType: modType, Dependencies: deps}
    return b.SavePartialBuildInfo(partial)
}

// Aggregate returns the full BuildInfo from cached partials.
func (m *BuildInfoManager) Aggregate(args Args) (*buildinfo.BuildInfo, error) {
    if args.BuildName == "" || args.BuildNumber == "" {
        return nil, fmt.Errorf("build name and number are required")
    }
    b, err := m.service.GetOrCreateBuildWithProject(args.BuildName, args.BuildNumber, args.Project)
    if err != nil {
        return nil, err
    }
    return b.ToBuildInfo()
}

// Publish aggregates partials and publishes to Artifactory.
func (m *BuildInfoManager) PublishAggregated(args Args) error {
    bi, err := m.Aggregate(args)
    if err != nil {
        return err
    }
    _, err = m.rt.PublishBuildInfo(bi, args.Project)
    return err
}

// Clean removes local cached partials for the build.
func (m *BuildInfoManager) Clean(args Args) error {
    if args.BuildName == "" || args.BuildNumber == "" {
        return nil
    }
    b, err := m.service.GetOrCreateBuildWithProject(args.BuildName, args.BuildNumber, args.Project)
    if err != nil {
        return err
    }
    return b.Clean()
}

func moduleIdOrDefault(args Args) string {
    if args.Module != "" {
        return args.Module
    }
    return "generic"
}

// computeModule enriches module id and type similar to CLI extractors for Maven/Gradle.
// It preserves existing behavior by honoring args.Module when provided and falling back to generic otherwise.
func computeModule(args Args) (string, buildinfo.ModuleType) {
    // User-specified module wins and preserves previous behavior
    if args.Module != "" {
        return args.Module, buildinfo.Generic
    }
    // Try Maven if selected
    if strings.EqualFold(args.BuildTool, MvnCmd) {
        if id, ok := deriveMavenModuleId(args); ok {
            return id, buildinfo.Maven
        }
    }
    // Try Gradle if selected
    if strings.EqualFold(args.BuildTool, GradleCmd) {
        if id, ok := deriveGradleModuleId(); ok {
            return id, buildinfo.Gradle
        }
    }
    // Default generic
    return moduleIdOrDefault(args), buildinfo.Generic
}

// deriveMavenModuleId parses pom.xml (or user-specified pom) to construct groupId:artifactId:version.
func deriveMavenModuleId(args Args) (string, bool) {
    pomPath := args.MvnPomFile
    if pomPath == "" {
        pomPath = "pom.xml"
    }
    data, err := os.ReadFile(pomPath)
    if err != nil {
        return "", false
    }
    dec := xml.NewDecoder(strings.NewReader(string(data)))
    var stack []string
    var projGroup, projArtifact, projVersion string
    var parentGroup, parentVersion string
    for {
        tok, err := dec.Token()
        if err != nil {
            break
        }
        switch t := tok.(type) {
        case xml.StartElement:
            stack = append(stack, t.Name.Local)
        case xml.EndElement:
            if len(stack) > 0 {
                stack = stack[:len(stack)-1]
            }
        case xml.CharData:
            if len(stack) == 0 {
                continue
            }
            name := stack[len(stack)-1]
            isParent := false
            for i := range stack {
                if stack[i] == "parent" {
                    isParent = true
                    break
                }
            }
            text := strings.TrimSpace(string([]byte(t)))
            if text == "" {
                continue
            }
            switch name {
            case "groupId":
                if isParent {
                    parentGroup = text
                } else {
                    projGroup = text
                }
            case "artifactId":
                if !isParent {
                    projArtifact = text
                }
            case "version":
                if isParent {
                    parentVersion = text
                } else {
                    projVersion = text
                }
            }
        }
    }
    g := strings.TrimSpace(projGroup)
    if g == "" {
        g = strings.TrimSpace(parentGroup)
    }
    a := strings.TrimSpace(projArtifact)
    v := strings.TrimSpace(projVersion)
    if v == "" {
        v = strings.TrimSpace(parentVersion)
    }
    if g == "" || a == "" || v == "" {
        return "", false
    }
    return fmt.Sprintf("%s:%s:%s", g, a, v), true
}

// deriveGradleModuleId executes `gradle properties -q` and extracts group, name, version.
func deriveGradleModuleId() (string, bool) {
    // Ensure gradle is present
    if _, err := exec.LookPath("gradle"); err != nil {
        logrus.Warn("Gradle module enrichment skipped: 'gradle' not found in PATH. Using 'generic' module.")
        return "", false
    }
    cmd := exec.Command("gradle", "properties", "-q")
    out, err := cmd.StdoutPipe()
    if err != nil {
        return "", false
    }
    if err := cmd.Start(); err != nil {
        logrus.Warnf("Gradle module enrichment skipped: failed to run 'gradle properties': %v. Using 'generic' module.", err)
        return "", false
    }
    defer func() { _ = cmd.Wait() }()
    scanner := bufio.NewScanner(out)
    var group, name, version string
    for scanner.Scan() {
        line := scanner.Text()
        // Lines are typically: group: com.acme
        if strings.HasPrefix(line, "group:") {
            group = strings.TrimSpace(strings.TrimPrefix(line, "group:"))
        } else if strings.HasPrefix(line, "name:") {
            name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
        } else if strings.HasPrefix(line, "version:") {
            version = strings.TrimSpace(strings.TrimPrefix(line, "version:"))
        }
    }
    _ = out.Close()
    // Treat "unspecified" as empty
    if strings.EqualFold(version, "unspecified") {
        version = ""
    }
    if group == "" || name == "" || version == "" {
        logrus.Warn("Gradle module enrichment skipped: could not derive group/name/version. Using 'generic' module.")
        return "", false
    }
    return fmt.Sprintf("%s:%s:%s", group, name, version), true
}
