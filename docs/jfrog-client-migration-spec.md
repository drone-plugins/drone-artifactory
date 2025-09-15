# Migration from JFrog CLI to JFrog Go Client SDK

This document specifies how to migrate the Drone Artifactory plugin from using the JFrog CLI to the JFrog Go Client SDK (`github.com/jfrog/jfrog-client-go`) while preserving all existing functionality, ensuring containerless compatibility, and maintaining custom certificate behavior.

## 1. Overview

- Goal: Replace all JFrog CLI usage with `jfrog-client-go` (and `github.com/jfrog/build-info-go` for build-info), remove CLI installation from Dockerfiles and plugin.yml, and keep functional parity.
- Scope: Upload, download, promote, publish build info, cleanup, discard builds, add build dependencies, Xray scan, Maven/Gradle build+publish helpers, proxy handling, TLS/certs, URL sanitation.
- Non-goals: Change user-visible arguments or semantics; only implementation and packaging changes.

## 2. Current State (CLI usage to replace)

Files and commands:
- `plugin/plugin.go`
  - Upload via: `jf rt u` with flags (`--url`, `--retries`, auth, `--flat`, `--threads`, `--insecure-tls`, `--build-name/--build-number`, `--spec`, `--spec-vars`, `--target-props`).
  - Optional `jf rt build-publish`.
  - TLS: writes PEM to `~/.jfrog/security/certs/cert.pem` (Windows/Linux default paths).
  - URL sanitized to include `/artifactory/`.
- `plugin/rt_commands.go`
  - Executes all composed CLI commands via shell (`getJfrogBin()`, pwsh/powershell/sh).
  - Writes PEM certificates (as above).
- `plugin/mvn.go`
  - Sequence: `jf config add`, `jf mvn-config ...`, `mvn ...`, `jf rt build-publish`.
- `plugin/gradle.go`
  - Sequence: `jf config add`, `jf gradle-config ...`, `gradle ...`, `jf rt build-publish`.
- `plugin/rt_download_cleanup.go`
  - `jf rt download` (spec path or inline spec), `jf rt build-clean`.
- `plugin/rt_scan_build_info_promote.go`
  - `jf build-scan`, `jf rt build-publish`, `jf rt build-promote`, `jf rt build-add-dependencies`.
- `plugin/rt_build_discard.go`
  - `jf rt build-discard`.
- Packaging:
  - `plugin.yml` installs JFrog CLI in `deps.run`.
  - `docker/Dockerfile.*` install/copy CLI (Linux and Windows variants).

## 3. Target Architecture (SDK-based)

- One-time manager: Create an Artifactory Services Manager per run using `auth.NewArtifactoryDetails()` and `config.NewConfigBuilder()`.
  - URL: sanitize to include `/artifactory/` where needed (Artifactory base). Use Xray base for scan.
  - Auth precedence: Access Token → Username/Password → API Key.
  - TLS:
    - If `PLUGIN_PEM_FILE_CONTENTS` provided, write file (default path preserved) and call `rtDetails.SetCertificatesPath(path)`.
    - If `PLUGIN_INSECURE` true, call `rtDetails.SetInsecureTls(true)`.
  - Retries: `SetHttpRetries(args.Retries)` on config builder.
  - Threads: Configure per-service (upload/download `SetThreads(args.Threads)`).
  - Proxy: Preserve existing env propagation; SDK honors Go proxy envs. Can optionally supply a custom `http.Client` with `http.ProxyFromEnvironment`.
  - Project key: Pass `args.Project` to APIs that accept it (e.g., `PublishBuildInfo`, `DiscardBuilds`).
- Build-info handling: Use `github.com/jfrog/build-info-go` to collect and persist partial build-info across steps and publish it later.
  - Upload/Download/Build actions add entries when `BuildName/BuildNumber` provided.
  - `publish-build-info` publishes cached build-info via `rtManager.PublishBuildInfo`.
  - `cleanup` clears local build-info cache for the provided build.
- File Specs: Support inline `spec` and `spec_path`. Parse JSON and map to SDK params.

## 4. CLI → SDK API Mapping

1) Upload (`jf rt u`):
- SDK: `rtManager.UploadFilesWithSummary(uploadServiceOptions, params...)`.
- Map:
  - `pattern` → `UploadParams.Pattern`
  - `target` → `UploadParams.Target`
  - `props`/`--target-props` → `UploadParams.TargetProps = utils.NewProperties().ParseAndAddProperties(...)`
  - `flat` → `UploadParams.Flat`
  - `recursive` → `UploadParams.Recursive`
  - `archive`/`explode` → matching fields if used
  - `threads` → upload service `SetThreads(args.Threads)`
- Build-info: From upload summary, append artifacts to build-info. Publish if `args.PublishBuildInfo`.

2) Download (`jf rt download`):
- SDK: `rtManager.DownloadFilesWithResultReader(params)` or `DownloadFiles(params...)`.
- Map:
  - `pattern`, `target`, `recursive`, `flat`, `build-name/number/module`.
- Build-info: Convert downloaded artifacts to dependencies for `module` and persist partials.

3) Publish Build Info (`jf rt build-publish`):
- SDK: `rtManager.PublishBuildInfo(buildInfo, args.Project)`.
- Build-info source: From in-memory aggregation in current process or load from local cache for standalone publish.

4) Cleanup (`jf rt build-clean`):
- Behavior: Local cache cleanup only (no server call). Use build-info-go to delete cached data for the build.

5) Discard Builds (`jf rt build-discard`):
- SDK: `services.NewDiscardBuildsParams()` + `rtManager.DiscardBuilds(params)`.
- Map: `BuildName`, `MaxDays`, `MaxBuilds`, `ExcludeBuilds`, `DeleteArtifacts`, `Async`, optional `Project`.

6) Promote Build (`jf rt build-promote`):
- SDK: `services.NewPromotionParams()` + `rtManager.PromoteBuild(params)`.
- Map: `BuildName`, `BuildNumber`, `TargetRepo`, `Copy`, optional `Project`.

7) Xray Build Scan (`jf build-scan`):
- SDK: `services.NewXrayScanParams()` + `rtManager.XrayScanBuild(params)`.
- Map: `BuildName`, `BuildNumber`, optional `Project`. Base URL for Xray is provided via details when creating the manager for scan.

8) Add Build Dependencies (`jf rt build-add-dependencies`):
- Local FS mode: Expand `args.DependencyPattern` (respect `Exclusions`, `Recursive`), compute checksums, and add `buildinfo.Dependency` entries to the module.
- From Artifactory (`args.FromRt`): Use `rtManager.SearchFiles(...)` (or AQL) and convert with `artifactory/services/utils.ConvertArtifactsDetailsToBuildInfoDependencies`, then append to build-info module.
- Publish build-info if requested in this step.

## 5. Maven and Gradle Flows

Replace `jf mvn-config` / `jf gradle-config` with runtime-generated configuration files, then run the native tool, and collect/publish build-info.

- Maven:
  - Generate a `settings.xml` with a `<server id>` (from `ResolverId` / `DeployerId`) and credentials (user+password or user+access-token), plus repositories for `resolve_*` and `deploy_*`.
  - Invoke `mvn <goals> -s <settings.xml>`.
  - Build-info: Use build-info-go to create module/artifact entries based on the build output. Publish using `rtManager.PublishBuildInfo`.

- Gradle:
  - Generate an `init.gradle` applying the JFrog Gradle plugin with resolve/deploy repos (`RepoResolve`/`RepoDeploy`) and credentials.
  - Invoke `gradle <tasks> -I <init.gradle>`.
  - Build-info: Same approach as Maven.

Notes:
- Keep Java/Maven/Gradle tools in Docker images. Only remove the CLI.
- On Windows, ensure generated paths and quoting are PowerShell-safe.

## 6. Auth, TLS/Certs, Proxies, URL

- Auth (in `createServiceManager(args)`):
  - Access Token: `rtDetails.SetAccessToken(args.AccessToken)`.
  - Username/Password: `rtDetails.SetUser(args.Username); rtDetails.SetPassword(args.Password)`.
  - API Key: `rtDetails.SetApiKey(args.APIKey)` (if used).
- TLS / Certs:
  - If `PLUGIN_PEM_FILE_CONTENTS` provided, write to `args.PEMFilePath` or default path, then `rtDetails.SetCertificatesPath(path)`.
  - If `PLUGIN_INSECURE` true, set `rtDetails.SetInsecureTls(true)`.
  - Default cert paths preserved:
    - Linux: `/root/.jfrog/security/certs/cert.pem`
    - Windows: `C:\users\ContainerAdministrator\.jfrog\security\certs\cert.pem`
  - The plugin already creates parent directories when needed.
- Proxies:
  - Keep `setSecureConnectProxies()` to copy Harness proxy env vars to standard `HTTP_PROXY`, etc.
  - The SDK uses Go’s proxy resolution; a custom `http.Client` can also be provided if needed.
- URL:
  - Preserve `sanitizeURL()` to ensure Artifactory URLs end with `/artifactory/`.
  - Use Xray base URL for `scan`.

## 7. Code Changes (by file)

- New: `plugin/rt_client.go`
  - `createServiceManager(args) (artifactory.ArtifactoryServicesManager, cleanup func(), err error)`
  - Handles URL, auth, proxies, retries, TLS, cert path.

- New: `plugin/spec.go`
  - Parse inline JSON spec or `spec_path` into Go structs.
  - Map spec to `services.UploadParams` / `services.DownloadParams` arrays.

- New: `plugin/buildinfo.go`
  - Wrap build-info-go to:
    - Append artifacts/dependencies for `BuildName/BuildNumber/Module/Project`.
    - Persist partials to local cache (multi-step behavior matching CLI).
    - Load and publish via `rtManager.PublishBuildInfo`.
    - Clean local cache for `cleanup`.

- Update: `plugin/plugin.go`
  - Replace `jf rt u` logic with SDK upload flows.
  - Handle `spec`/`spec_path` or `source/target` into params.
  - Remove shell/JFrog CLI usage and `getJfrogBin()`.
  - Keep URL sanitize and proxy handling.

- Update: `plugin/rt_commands.go`
  - Replace `ExecCommand` dispatch with direct SDK-backed functions:
    - download → sdk download + build-info
    - cleanup → local build-info cache removal
    - scan → Xray scan
    - publish-build-info → publish from cache/aggregate
    - promote → sdk promote
    - add-build-dependencies → fs or Artifactory source
    - build-discard → `DiscardBuilds`
  - Keep PEM write helper (used by manager creation).

- Update: `plugin/mvn.go`, `plugin/gradle.go`
  - Remove `GetConfigAddConfigCommandArgs`, and CLI-based `mvn-config` / `gradle-config`.
  - Generate `settings.xml` / `init.gradle` from `Args` and run native tools.
  - Build-info aggregation and publish via SDK.

- Update: `plugin/rt_download_cleanup.go`, `plugin/rt_scan_build_info_promote.go`, `plugin/rt_build_discard.go`
  - Replace composed CLI calls with SDK invocation per the mappings.

## 8. Dockerfiles and Plugin Packaging

- `docker/Dockerfile.linux.amd64` and `docker/Dockerfile.linux.arm64`:
  - Remove JFrog CLI installation:
    - Delete lines running `curl -fL https://getcli.jfrog.io/v2-jf | sh ...`, `mv ./jf ...`, `chmod +x ...`.
  - Keep `ca-certificates`, `openjdk`, `maven`, `gradle`, `docker` tooling.
  - No other changes required for certificates; plugin manages PEM paths.

- `docker/Dockerfile.windows.amd64.ltsc2022` and `docker/Dockerfile.windows.amd64.1809`:
  - Remove JFrog CLI download and `COPY` operations.
  - Keep JDK, Maven, Gradle setup and PATH.
  - Keep creation of `C:\users\ContainerAdministrator\.jfrog\security\certs` only if desired; the plugin will ensure directories exist when writing PEMs.

- `plugin.yml`:
  - Remove `deps.run` step that installs JFrog CLI (`curl -fL https://install-cli.jfrog.io | sh /dev/stdin ...`).

## 9. Risks & Mitigations

- Spec compatibility: Ensure the spec parser covers fields used by our docs. Validate against example specs in `docs/`.
- Maven/Gradle parity: Generated configs may not cover all CLI nuances. Provide overrides (custom `settings.xml` / `init.gradle` path) and test provided examples.
- Cross-step build-info: Ensure build-info-go cache paths and semantics mirror CLI behavior so `publish-build-info` works in later steps or containerless modes.
- Windows cert trust: SDK uses provided PEM via `SetCertificatesPath`; does not use Windows cert store. Our PEM handling continues to work as before.

## 10. Testing & Validation

- Unit tests:
  - Spec parsing and mapping to SDK params.
  - URL sanitize.
  - Auth precedence and TLS/cert path handling.
  - Build-info aggregation for upload/download.
- Integration (optional, behind env guards):
  - Upload/download to test Artifactory.
  - Publish build-info, promote, discard, add deps, xray scan.
- Backward compatibility: Validate examples in `docs/*.md` continue to run with the new images.

## 11. Rollout Plan

1) Add dependencies: `jfrog-client-go`, `build-info-go`.
2) Implement `createServiceManager`, spec parser, build-info wrapper.
3) Replace upload in `plugin/plugin.go`.
4) Replace RT command flows across `plugin/rt_*`.
5) Implement Maven/Gradle config writers and update `mvn.go` / `gradle.go`.
6) Remove shell/JFrog CLI scaffolding and env usage.
7) Update Dockerfiles and `plugin.yml` to remove CLI steps.
8) Validate with repo docs; update any documentation if behavior notes are needed.

## 12. Acceptance Criteria

- All features work without JFrog CLI present.
- Custom certs and insecure TLS behavior unchanged and effective through SDK.
- Containerless usage supported (no external binary dependency).
- Docker images and plugin.yml no longer install or bundle JFrog CLI.
- Examples from `docs/` complete successfully on Linux and Windows variants.

## 13. References

- JFrog Go Client (Artifactory): https://pkg.go.dev/github.com/jfrog/jfrog-client-go/artifactory
- JFrog Go Client (repo): https://github.com/jfrog/jfrog-client-go
- Build Info Go: https://github.com/jfrog/build-info-go
- JFrog File Specs: https://jfrog.com/help/r/jfrog-integrations-documentation/using-file-specs

## 14. JFrog CLI Internal Mapping to Libraries

The JFrog CLI is implemented in Go and delegates most operations to two libraries: `jfrog-cli-core` (command wiring, spec handling, build-info integration) and `jfrog-client-go` (services API). Below is a mapping of the CLI commands used by this plugin to their underlying libraries and the equivalent SDK calls we will use.

- Upload (`jf rt u`)
  - CLI: `jfrog-cli-core/v2/commands/generic/upload` → uses `artifactory` service manager
  - SDK: `artifactory.ArtifactoryServicesManager.UploadFilesWithSummary(uploadServiceOptions, ...services.UploadParams)`

- Download (`jf rt download`)
  - CLI: `jfrog-cli-core/v2/commands/generic/download`
  - SDK: `artifactory.ArtifactoryServicesManager.DownloadFilesWithResultReader(...services.DownloadParams)` or `DownloadFiles`

- Publish Build Info (`jf rt build-publish`)
  - CLI: `jfrog-cli-core/v2/commands/buildinfo/publish` → leverages `build-info-go`
  - SDK: `artifactory.ArtifactoryServicesManager.PublishBuildInfo(buildinfo.BuildInfo, projectKey)`

- Cleanup local build data (`jf rt build-clean`)
  - CLI: local build-info cache cleanup (no server API)
  - SDK: use `build-info-go` cache removal helpers (local filesystem)

- Discard Old Builds (`jf rt build-discard`)
  - CLI: `jfrog-cli-core/v2/commands/build/discard`
  - SDK: `artifactory.ArtifactoryServicesManager.DiscardBuilds(services.DiscardBuildsParams)`

- Promote Build (`jf rt build-promote`)
  - CLI: `jfrog-cli-core/v2/commands/build/promote`
  - SDK: `artifactory.ArtifactoryServicesManager.PromoteBuild(services.PromotionParams)`

- Xray Build Scan (`jf build-scan`)
  - CLI: `jfrog-cli-core/v2/commands/buildinfo/scan`
  - SDK: `artifactory.ArtifactoryServicesManager.XrayScanBuild(services.XrayScanParams)`

- Add Build Dependencies (`jf rt build-add-dependencies`)
  - CLI: `jfrog-cli-core/v2/commands/buildinfo/adddependencies` → uses `build-info-go` (FS and Artifactory sources)
  - SDK approach here: combine `build-info-go` for FS scanning and `artifactory.SearchFiles`/AQL for Artifactory-origin dependencies, then publish via `PublishBuildInfo`.

- Maven/Gradle config & run (`jf mvn-config`, `jf mvn`, `jf gradle-config`, `jf gradle`)
  - CLI: `jfrog-cli-core/v2/commands/mvn` and `.../gradle` generate config and wrap tool execution with build-info extractors
  - Plugin migration: generate `settings.xml` / `init.gradle` ourselves and execute native tools; use `build-info-go` to aggregate and publish build-info.

Useful `jfrog-cli-core` helpers we may optionally leverage:
- `github.com/jfrog/jfrog-cli-core/v2/spec`: File spec structs and parsing.
- `github.com/jfrog/jfrog-cli-core/artifactory/utils`: service manager creation, search params helpers, repo utilities.

## 15. Dependency Decision

- Required dependencies (minimal, recommended):
  - `github.com/jfrog/jfrog-client-go` (modules used: `artifactory`, `artifactory/services`, `auth`, `config`)
  - `github.com/jfrog/build-info-go` (build-info collection/persistence)

- Optional dependencies (to reduce reimplementation effort):
  - `github.com/jfrog/jfrog-cli-core/v2/spec` – parse and validate File Specs consistently with CLI
  - `github.com/jfrog/jfrog-cli-core/artifactory/utils` – convenience helpers for creating managers and mapping spec to params

Trade-offs:
- Using `jfrog-cli-core` eases spec parsing and some build-info flows but increases the dependency surface and binary size.
- Sticking to `jfrog-client-go` + a lightweight internal spec parser keeps the plugin lean with a bit more implementation effort.

Recommendation:
- Start with the minimal set (client-go + build-info-go). If spec edge cases or parity gaps surface, introduce `jfrog-cli-core/v2/spec` narrowly for spec parsing without pulling other heavy modules.

## 16. Plan Update Summary

- Added explicit mapping between CLI commands and underlying libraries.
- Decided on a minimal dependency set with an optional path to include `jfrog-cli-core` spec parsing if needed.
- Implementation tasks remain the same, but spec parsing can either use an internal mapper (primary plan) or `jfrog-cli-core/v2/spec` (fallback) depending on test results.
