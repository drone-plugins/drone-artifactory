package plugin

import (
    "crypto/md5"
    "crypto/sha1"
    "crypto/sha256"
    "encoding/hex"
    "errors"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "regexp"
    "strings"

    buildinfo "github.com/jfrog/build-info-go/entities"
    services "github.com/jfrog/jfrog-client-go/artifactory/services"
    svcutils "github.com/jfrog/jfrog-client-go/artifactory/services/utils"
    "github.com/sirupsen/logrus"
)

func GetScanCommandArgs(args Args) ([][]string, error) {
	var cmdList [][]string

	if args.BuildName == "" || args.BuildNumber == "" {
		return cmdList, errors.New("Valid BuildName and BuildNumber are required")
	}

	authParams, err := setAuthParams([]string{}, Args{Username: args.Username,
		Password: args.Password, AccessToken: args.AccessToken, APIKey: args.APIKey})
	if err != nil {
		return cmdList, err
	}

	scanCommandArgs := []string{
		"build-scan", args.BuildName, args.BuildNumber}
	scanCommandArgs = append(scanCommandArgs, "--url="+args.URL)
	scanCommandArgs = append(scanCommandArgs, authParams...)
	cmdList = append(cmdList, scanCommandArgs)

	return cmdList, nil
}

func GetBuildInfoPublishCommandArgs(args Args) ([][]string, error) {
	var cmdList [][]string

	tmpServerId := tmpServerId
	jfrogConfigAddConfigCommandArgs, err := GetConfigAddConfigCommandArgs(tmpServerId,
		args.Username, args.Password, args.URL, args.AccessToken, args.APIKey)
	if err != nil {
		logrus.Println("GetConfigAddConfigCommandArgs error: ", err)
		return cmdList, err
	}
	buildInfoCommandArgs := []string{"rt", "build-publish", args.BuildName, args.BuildNumber}
	err = PopulateArgs(&buildInfoCommandArgs, &args, nil)
	if err != nil {
		return cmdList, err
	}
	cmdList = append(cmdList, jfrogConfigAddConfigCommandArgs)
	cmdList = append(cmdList, buildInfoCommandArgs)
	return cmdList, nil
}

func GetPromoteCommandArgs(args Args) ([][]string, error) {
	var cmdList [][]string

	promoteCommandArgs := []string{"rt", "build-promote"}
	if args.Copy != "" {
		promoteCommandArgs = append(promoteCommandArgs, "--copy="+args.Copy)
	}
	promoteCommandArgs = append(promoteCommandArgs, "--url="+args.URL)
	promoteCommandArgs = append(promoteCommandArgs, args.BuildName, args.BuildNumber, args.Target)
	authParams, err := setAuthParams([]string{}, Args{Username: args.Username, Password: args.Password, AccessToken: args.AccessToken, APIKey: args.APIKey})
	if err != nil {
		return cmdList, err
	}
	promoteCommandArgs = append(promoteCommandArgs, authParams...)
	cmdList = append(cmdList, promoteCommandArgs)
	return cmdList, nil
}

var AddDependenciesCmdJsonToExeFlagMapItemList = []JsonTagToExeFlagMapStringItem{
	{"--exclusions=", "PLUGIN_EXCLUSIONS", false, false},
	{"--from-rt=", "PLUGIN_FROM_RT", false, false},
	{"--module=", "PLUGIN_MODULE", false, false},
	{"--project=", "PLUGIN_PROJECT", false, false},
	{"--recursive=", "PLUGIN_RECURSIVE", false, false},
	{"--server-id=", "PLUGIN_SERVER_ID", false, false},
	{"--spec=", "PLUGIN_SPEC_PATH", false, false},
}

func GetAddDependenciesCommandArgs(args Args) ([][]string, error) {
	var cmdList [][]string

	jfrogConfigAddConfigCommandArgs, err := GetConfigAddConfigCommandArgs(tmpServerId,
		args.Username, args.Password, args.URL, args.AccessToken, args.APIKey)
	if err != nil {
		return cmdList, err
	}

	addDependenciesCommandArgs := []string{"rt", "build-add-dependencies"}
	err = PopulateArgs(&addDependenciesCommandArgs, &args, AddDependenciesCmdJsonToExeFlagMapItemList)
	if err != nil {
		return cmdList, err
	}
	addDependenciesCommandArgs = append(addDependenciesCommandArgs, "--server-id="+tmpServerId)

	addDependenciesCommandArgs = append(addDependenciesCommandArgs, args.BuildName, args.BuildNumber)
	if args.DependencyPattern != "" {
		addDependenciesCommandArgs = append(addDependenciesCommandArgs, args.DependencyPattern)
	}

	buildInfoCommandArgs := []string{"rt", "build-publish", args.BuildName, args.BuildNumber}
	err = PopulateArgs(&buildInfoCommandArgs, &args, nil)
	if err != nil {
		return cmdList, err
	}

	cmdList = append(cmdList, jfrogConfigAddConfigCommandArgs)
	cmdList = append(cmdList, addDependenciesCommandArgs)
	cmdList = append(cmdList, buildInfoCommandArgs)

	return cmdList, nil
}

// --- SDK implementations ---

func runScan(args Args) error {
    if args.BuildName == "" || args.BuildNumber == "" {
        return errors.New("Valid BuildName and BuildNumber are required")
    }
    rt, cleanup, err := createServiceManager(args)
    if err != nil {
        return err
    }
    defer cleanup()
    p := services.NewXrayScanParams()
    p.BuildName = args.BuildName
    p.BuildNumber = args.BuildNumber
    if _, err := rt.XrayScanBuild(p); err != nil {
        return err
    }
    logrus.Printf("Xray scan triggered for %s/%s", args.BuildName, args.BuildNumber)
    return nil
}

func runPromote(args Args) error {
    if args.BuildName == "" || args.BuildNumber == "" || args.Target == "" {
        return fmt.Errorf("promote requires build_name, build_number and target")
    }
    rt, cleanup, err := createServiceManager(args)
    if err != nil {
        return err
    }
    defer cleanup()

    params := services.NewPromotionParams()
    params.BuildName = args.BuildName
    params.BuildNumber = args.BuildNumber
    params.TargetRepo = args.Target
    // Copy flag can be "true"/"false" string
    params.Copy = parseBoolOrDefault(false, args.Copy)
    if err := rt.PromoteBuild(params); err != nil {
        return err
    }
    logrus.Printf("Promoted %s/%s to %s", args.BuildName, args.BuildNumber, args.Target)
    return nil
}

func runAddDependencies(args Args) error {
    if args.BuildName == "" || args.BuildNumber == "" {
        return fmt.Errorf("add-build-dependencies requires build_name and build_number")
    }
    if args.Module == "" {
        args.Module = "generic"
    }

    rt, cleanup, err := createServiceManager(args)
    if err != nil {
        return err
    }
    defer cleanup()

    module := buildinfo.Module{Id: moduleIdOrDefault(args)}

    if !parseBoolOrDefault(false, args.FromRt) {
        // Local filesystem dependencies
        if args.DependencyPattern == "" && args.SpecPath == "" {
            return fmt.Errorf("local dependency collection requires 'dependency' pattern or 'spec_path'")
        }
        var patterns []string
        if args.DependencyPattern != "" {
            patterns = []string{args.DependencyPattern}
        } else if args.SpecPath != "" {
            fs, err := parseFileSpecFromPath(args.SpecPath)
            if err != nil {
                return err
            }
            for _, f := range fs.Files {
                if strings.TrimSpace(f.Pattern) != "" {
                    patterns = append(patterns, f.Pattern)
                }
            }
        }
        for _, pat := range patterns {
            matches, err := globDoubleStar(pat)
            if err != nil {
                return err
            }
            // Apply exclusions if provided on args.Exclusions
            exList := splitCSV(args.Exclusions)
            for _, path := range matches {
                if isExcluded(path, exList) {
                    continue
                }
                sha1Hex, md5Hex, sha256Hex, err := fileChecksums(path)
                if err != nil {
                    return err
                }
                dep := buildinfo.Dependency{
                    Id:   filepath.ToSlash(path),
                    Type: "file",
                    Checksum: buildinfo.Checksum{
                        Sha1:   sha1Hex,
                        Md5:    md5Hex,
                        Sha256: sha256Hex,
                    },
                }
                module.Dependencies = append(module.Dependencies, dep)
            }
        }
    } else {
        // Collect dependencies from Artifactory using search pattern
        if args.DependencyPattern == "" && args.SpecPath == "" {
            return fmt.Errorf("from_rt=true requires 'dependency' pattern or 'spec_path'")
        }
        var patterns []string
        if args.DependencyPattern != "" {
            patterns = []string{args.DependencyPattern}
        } else if args.SpecPath != "" {
            fs, err := parseFileSpecFromPath(args.SpecPath)
            if err != nil {
                return err
            }
            for _, f := range fs.Files {
                if strings.TrimSpace(f.Pattern) != "" {
                    patterns = append(patterns, f.Pattern)
                }
            }
        }
        for _, pat := range patterns {
            sp := services.NewSearchParams()
            sp.Pattern = pat
            sp.Recursive = parseBoolOrDefault(true, args.Recursive)
            reader, err := rt.SearchFiles(sp)
            if err != nil {
                return err
            }
            for item := new(svcutils.ResultItem); reader.NextRecord(item) == nil; item = new(svcutils.ResultItem) {
                dep := item.ToDependency()
                // Prepend repo/path for Id if desired: default 'item.Name' used by ToDependency
                if item.Path != "." {
                    dep.Id = filepath.ToSlash(filepath.Join(item.Repo, item.Path, item.Name))
                } else {
                    dep.Id = filepath.ToSlash(filepath.Join(item.Repo, item.Name))
                }
                module.Dependencies = append(module.Dependencies, dep)
            }
            if err := reader.GetError(); err != nil {
                return err
            }
            reader.Close()
        }
    }

    // Save partial
    bim := NewBuildInfoManager(rt)
    if err := bim.SaveDependenciesPartial(args, module.Dependencies); err != nil {
        return err
    }
    // Optionally publish now if requested
    if args.PublishBuildInfo {
        if err := bim.PublishAggregated(args); err != nil {
            return err
        }
        if parseBoolOrDefault(false, args.CleanupAfterPublish) {
            if err := bim.Clean(args); err != nil {
                logrus.Printf("warning: failed to cleanup build-info cache: %v", err)
            }
        }
    }
    logrus.Printf("Added %d dependencies to %s/%s (module: %s)", len(module.Dependencies), args.BuildName, args.BuildNumber, module.Id)
    return nil
}

// --- helpers for local dependency collection ---

// globDoubleStar supports simple ** patterns by converting to a regex and walking the filesystem.
func globDoubleStar(pattern string) ([]string, error) {
    // Normalize to slash
    pat := filepath.ToSlash(pattern)
    root := extractStaticRoot(pat)
    if root == "" {
        root = "."
    }
    rx, err := globToRegex(pat)
    if err != nil {
        return nil, err
    }
    matches := []string{}
    err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
        if err != nil {
            return err
        }
        if d.IsDir() {
            return nil
        }
        norm := filepath.ToSlash(path)
        if rx.MatchString(norm) {
            matches = append(matches, path)
        }
        return nil
    })
    if err != nil {
        return nil, err
    }
    return matches, nil
}

func extractStaticRoot(pattern string) string {
    // Up to first meta char
    metas := []rune{'*', '?', '[', ']'}
    idx := len(pattern)
    for _, m := range metas {
        if i := strings.IndexRune(pattern, m); i >= 0 && i < idx {
            idx = i
        }
    }
    root := pattern[:idx]
    // Trim to directory boundary
    if i := strings.LastIndex(root, "/"); i >= 0 {
        root = root[:i]
    }
    return root
}

func globToRegex(pat string) (*regexp.Regexp, error) {
    // Escape regex special chars
    esc := regexp.QuoteMeta(pat)
    // Undo escapes for our glob tokens and translate
    esc = strings.ReplaceAll(esc, "\\*\\*", ".*")        // ** -> .*
    esc = strings.ReplaceAll(esc, "\\*", "[^/]*")          // *  -> [^/]*
    esc = strings.ReplaceAll(esc, "\\?", ".")             // ?  -> .
    esc = "^" + esc + "$"
    return regexp.Compile(esc)
}

func splitCSV(s string) []string {
    if strings.TrimSpace(s) == "" {
        return nil
    }
    parts := strings.Split(s, ",")
    out := make([]string, 0, len(parts))
    for _, p := range parts {
        p = strings.TrimSpace(p)
        if p != "" {
            out = append(out, p)
        }
    }
    return out
}

func isExcluded(path string, patterns []string) bool {
    if len(patterns) == 0 {
        return false
    }
    p := filepath.ToSlash(path)
    for _, pat := range patterns {
        rx, err := globToRegex(filepath.ToSlash(pat))
        if err != nil {
            continue
        }
        if rx.MatchString(p) {
            return true
        }
    }
    return false
}

func fileChecksums(path string) (sha1Hex, md5Hex, sha256Hex string, err error) {
    f, err := os.Open(path)
    if err != nil {
        return "", "", "", err
    }
    defer f.Close()
    h1 := sha1.New()
    h5 := md5.New()
    h256 := sha256.New()
    mw := io.MultiWriter(h1, h5, h256)
    if _, err = io.Copy(mw, f); err != nil {
        return "", "", "", err
    }
    sha1Hex = hex.EncodeToString(h1.Sum(nil))
    md5Hex = hex.EncodeToString(h5.Sum(nil))
    sha256Hex = hex.EncodeToString(h256.Sum(nil))
    return
}

func runBuildDiscard(args Args) error {
    if args.BuildName == "" {
        return fmt.Errorf("build-discard requires build_name")
    }
    rt, cleanup, err := createServiceManager(args)
    if err != nil {
        return err
    }
    defer cleanup()

    p := services.NewDiscardBuildsParams()
    p.BuildName = args.BuildName
    p.MaxDays = args.MaxDays
    p.MaxBuilds = args.MaxBuilds
    p.ExcludeBuilds = args.ExcludeBuilds
    p.DeleteArtifacts = parseBoolOrDefault(false, args.DeleteArtifacts)
    p.Async = parseBoolOrDefault(false, args.Async)
    p.ProjectKey = args.Project
    if err := rt.DiscardBuilds(p); err != nil {
        return err
    }
    logrus.Printf("Discard builds request sent for %s", args.BuildName)
    return nil
}
