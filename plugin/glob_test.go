package plugin

import (
    "os"
    "path/filepath"
    "testing"
)

func TestGlobDoubleStar(t *testing.T) {
    tmp, err := os.MkdirTemp("", "globtest-")
    if err != nil {
        t.Fatal(err)
    }
    defer os.RemoveAll(tmp)

    // Create structure
    mustWrite := func(p string) {
        if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
            t.Fatal(err)
        }
        if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
            t.Fatal(err)
        }
    }
    mustWrite(filepath.Join(tmp, "a", "b", "c", "one.jar"))
    mustWrite(filepath.Join(tmp, "a", "x", "two.jar"))
    mustWrite(filepath.Join(tmp, "a", "x", "two.java"))

    // Match all jars under tmp
    pattern := filepath.ToSlash(filepath.Join(tmp, "**", "*.jar"))
    matches, err := globDoubleStar(pattern)
    if err != nil {
        t.Fatalf("glob error: %v", err)
    }
    if len(matches) != 2 {
        t.Fatalf("expected 2 matches, got %d: %v", len(matches), matches)
    }
}

