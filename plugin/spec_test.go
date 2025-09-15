package plugin

import (
    "encoding/json"
    "testing"
)

func TestParseFileSpecToUploadParams(t *testing.T) {
    spec := FileSpec{Files: []File{
        {Pattern: "repo-a/*.jar", Target: "target-a/", Props: "k1=v1", Recursive: "true", Flat: "false"},
        {Pattern: "repo-b/**/*.zip", Target: "target-b/", TargetProps: "k2=v2", Recursive: "", Flat: ""},
    }}
    b, _ := json.Marshal(spec)
    fs, err := parseFileSpecFromString(string(b))
    if err != nil {
        t.Fatalf("parse error: %v", err)
    }
    params, err := fs.ToUploadParams()
    if err != nil {
        t.Fatalf("map error: %v", err)
    }
    if len(params) != 2 {
        t.Fatalf("expected 2 params, got %d", len(params))
    }
    if !params[0].Recursive || params[0].Flat {
        t.Errorf("unexpected recursive/flat for first param: %+v", params[0])
    }
    if params[0].Target != "target-a/" || params[0].Pattern != "repo-a/*.jar" {
        t.Errorf("unexpected mapping for first param: %+v", params[0])
    }
    if params[1].Target != "target-b/" || params[1].Pattern != "repo-b/**/*.zip" {
        t.Errorf("unexpected mapping for second param: %+v", params[1])
    }
}

func TestApplySpecVars(t *testing.T) {
    content := `{"files":[{"pattern":"repo/${team}/**/*.jar","target":"out/${env}/"}]}`
    vars := "team=backend;env=prod"
    out := applySpecVars(content, vars)
    if out == content {
        t.Fatalf("expected substitution to change content")
    }
    if want := `{"files":[{"pattern":"repo/backend/**/*.jar","target":"out/prod/"}]}`; out != want {
        t.Fatalf("unexpected result: %s", out)
    }
}
