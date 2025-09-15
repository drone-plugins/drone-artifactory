package plugin

import (
    "encoding/json"
    "fmt"
    "os"
    "strconv"
    "strings"

    services "github.com/jfrog/jfrog-client-go/artifactory/services"
    artutils "github.com/jfrog/jfrog-client-go/artifactory/services/utils"
)

// Minimal File Spec structures for upload/download parity.
// Covers the fields used by this plugin's docs and common cases.

type FileSpec struct {
    Files []File `json:"files"`
}

type File struct {
    Pattern       string `json:"pattern,omitempty"`
    Aql           any    `json:"aql,omitempty"` // not handled in mapping for now
    Target        string `json:"target,omitempty"`
    Props         string `json:"props,omitempty"`
    TargetProps   string `json:"targetProps,omitempty"`
    ExcludeProps  string `json:"excludeProps,omitempty"`
    Recursive     string `json:"recursive,omitempty"`
    Flat          string `json:"flat,omitempty"`
    Exclusions    string `json:"exclusions,omitempty"`
    ArchiveEntries string `json:"archiveEntries,omitempty"`
    Build         string `json:"build,omitempty"`
    Bundle        string `json:"bundle,omitempty"`
    SortBy        string `json:"sortBy,omitempty"`
    SortOrder     string `json:"sortOrder,omitempty"`
    Limit         int    `json:"limit,omitempty"`
    Offset        int    `json:"offset,omitempty"`
}

func parseFileSpecFromString(s string) (*FileSpec, error) {
    var fs FileSpec
    if err := json.Unmarshal([]byte(s), &fs); err != nil {
        return nil, fmt.Errorf("failed parsing file spec: %w", err)
    }
    return &fs, nil
}

func parseFileSpecFromPath(path string) (*FileSpec, error) {
    b, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("failed reading file spec: %w", err)
    }
    return parseFileSpecFromString(string(b))
}

// applySpecVars replaces ${key} tokens in the spec with values from vars string.
// vars format supports either semicolon or comma separators: key1=val1;key2=val2 or key1=val1,key2=val2
func applySpecVars(specContent, vars string) string {
    if strings.TrimSpace(vars) == "" {
        return specContent
    }
    // Split on ';' or ','
    separators := []rune{';', ','}
    pairs := []string{vars}
    for _, sep := range separators {
        if strings.ContainsRune(vars, sep) {
            pairs = strings.Split(vars, string(sep))
            break
        }
    }
    m := map[string]string{}
    for _, p := range pairs {
        p = strings.TrimSpace(p)
        if p == "" {
            continue
        }
        kv := strings.SplitN(p, "=", 2)
        if len(kv) != 2 {
            continue
        }
        key := strings.TrimSpace(kv[0])
        val := strings.TrimSpace(kv[1])
        if key == "" {
            continue
        }
        m[key] = val
    }
    // Replace ${key} occurrences
    out := specContent
    for k, v := range m {
        token := "${" + k + "}"
        out = strings.ReplaceAll(out, token, v)
    }
    return out
}

// Map spec to UploadParams. Only 'pattern' based specs are supported here.
func (fs *FileSpec) ToUploadParams() ([]services.UploadParams, error) {
    var out []services.UploadParams
    for _, f := range fs.Files {
        if strings.TrimSpace(f.Pattern) == "" {
            // Skip entries without pattern (AQL not handled by this mapper)
            continue
        }
        p := services.NewUploadParams()
        p.Pattern = f.Pattern
        p.Target = f.Target

        // Properties: prefer TargetProps when present, else Props
        props := strings.TrimSpace(coalesce(f.TargetProps, f.Props))
        if props != "" {
            pr := artutils.NewProperties()
            // Ignore parse errors to be lenient like CLI
            _ = pr.ParseAndAddProperties(props)
            p.TargetProps = pr
        }

        // Flags with defaults aligned to JFrog behavior: recursive=true, flat=false
        p.Recursive = parseBoolDefaultTrue(f.Recursive)
        p.Flat = parseBoolDefaultFalse(f.Flat)

        // Exclusions (comma-separated patterns)
        if ex := strings.TrimSpace(f.Exclusions); ex != "" {
            p.Exclusions = strings.Split(ex, ",")
        }

        out = append(out, p)
    }
    if len(out) == 0 {
        return nil, fmt.Errorf("file spec did not include any usable upload entries")
    }
    return out, nil
}

// Map spec to DownloadParams. Only 'pattern' based specs are supported here.
func (fs *FileSpec) ToDownloadParams() ([]services.DownloadParams, error) {
    var out []services.DownloadParams
    for _, f := range fs.Files {
        if strings.TrimSpace(f.Pattern) == "" {
            continue
        }
        p := services.NewDownloadParams()
        p.Pattern = f.Pattern
        p.Target = f.Target
        p.Recursive = parseBoolDefaultTrue(f.Recursive)
        p.Flat = parseBoolDefaultFalse(f.Flat)
        if ex := strings.TrimSpace(f.Exclusions); ex != "" {
            p.Exclusions = strings.Split(ex, ",")
        }
        out = append(out, p)
    }
    if len(out) == 0 {
        return nil, fmt.Errorf("file spec did not include any usable download entries")
    }
    return out, nil
}

func parseBoolDefaultTrue(s string) bool {
    if s == "" {
        return true
    }
    v, err := strconv.ParseBool(s)
    if err != nil {
        return true
    }
    return v
}

func parseBoolDefaultFalse(s string) bool {
    if s == "" {
        return false
    }
    v, err := strconv.ParseBool(s)
    if err != nil {
        return false
    }
    return v
}

func coalesce(values ...string) string {
    for _, v := range values {
        if strings.TrimSpace(v) != "" {
            return v
        }
    }
    return ""
}
