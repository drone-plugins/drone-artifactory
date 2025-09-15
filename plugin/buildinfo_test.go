package plugin

import (
    "os"
    "path/filepath"
    "testing"

    buildinfo "github.com/jfrog/build-info-go/entities"
)

func TestBuildInfoPartialsAggregate_Generic(t *testing.T) {
    // Arrange
    args := Args{BuildName: "b-generic", BuildNumber: "1"}
    bim := NewBuildInfoManager(nil)

    arts := []buildinfo.Artifact{{Name: "a.jar"}, {Name: "b.jar"}}
    deps := []buildinfo.Dependency{{Id: "x.zip"}}

    // Act
    if err := bim.SaveArtifactsPartial(args, arts); err != nil {
        t.Fatalf("SaveArtifactsPartial error: %v", err)
    }
    if err := bim.SaveDependenciesPartial(args, deps); err != nil {
        t.Fatalf("SaveDependenciesPartial error: %v", err)
    }
    bi, err := bim.Aggregate(args)
    if err != nil {
        t.Fatalf("Aggregate error: %v", err)
    }

    // Assert
    if len(bi.Modules) == 0 {
        t.Fatalf("expected at least one module")
    }
    if bi.Modules[0].Id != "generic" {
        t.Fatalf("expected module id 'generic', got %s", bi.Modules[0].Id)
    }
}

func TestBuildInfoPartialsAggregate_MavenDerivedModule(t *testing.T) {
    // Skip if cannot write temp POM
    dir := t.TempDir()
    pom := `<?xml version="1.0" encoding="UTF-8"?>
    <project xmlns="http://maven.apache.org/POM/4.0.0">
      <modelVersion>4.0.0</modelVersion>
      <groupId>com.example</groupId>
      <artifactId>demo</artifactId>
      <version>1.2.3</version>
    </project>`
    pomPath := filepath.Join(dir, "pom.xml")
    if err := os.WriteFile(pomPath, []byte(pom), 0644); err != nil {
        t.Skipf("cannot write pom: %v", err)
    }

    args := Args{BuildName: "b-mvn", BuildNumber: "1", BuildTool: MvnCmd, MvnPomFile: pomPath}
    bim := NewBuildInfoManager(nil)

    arts := []buildinfo.Artifact{{Name: "a.jar"}}
    if err := bim.SaveArtifactsPartial(args, arts); err != nil {
        t.Fatalf("SaveArtifactsPartial error: %v", err)
    }
    bi, err := bim.Aggregate(args)
    if err != nil {
        t.Fatalf("Aggregate error: %v", err)
    }
    if len(bi.Modules) == 0 {
        t.Fatalf("expected at least one module")
    }
    if bi.Modules[0].Id != "com.example:demo:1.2.3" {
        t.Fatalf("expected module id 'com.example:demo:1.2.3', got %s", bi.Modules[0].Id)
    }
}
