Multi-Step Build Info Pipeline (Upload → Add Deps → Download → Publish → Cleanup)

Overview
- No JFrog CLI required; the plugin uses the JFrog Go Client SDK.
- Upload, Download, and Add-Build-Dependencies steps save build-info partials locally using build-info-go.
- Publish step aggregates and publishes the full build-info to Artifactory.
- Optional auto-clean and explicit cleanup supported.

Example (Harness CI Plugin steps)

```yaml
# 1) Upload artifacts and accumulate build-info (partials)
- step:
    type: Plugin
    name: UploadArtifacts
    identifier: UploadArtifacts
    spec:
      connectorRef: account.harnessImage
      image: plugins/artifactory:linux-amd64
      settings:
        url: https://URL.jfrog.io/artifactory
        username: user
        password: <+secrets.getValue("jfrog_password")>
        # Either use source/target or a file spec
        source: /harness/build/output/**/*.jar
        target: libs-release-local/path/in/repo/
        build_name: sample-build
        build_number: 123
        # Optional: properties on target
        target_props: key1=value1,key2=value2
        # Do not publish yet (accumulate partials)
        publish_build_info: false

# 2) Add build dependencies (from filesystem). Also accumulates partials
- step:
    type: Plugin
    name: AddBuildDependencies
    identifier: AddBuildDependencies
    spec:
      connectorRef: account.harnessImage
      image: plugins/artifactory:linux-amd64
      settings:
        command: add-build-dependencies
        url: https://URL.jfrog.io
        username: user
        password: <+secrets.getValue("jfrog_password")>
        build_name: sample-build
        build_number: 123
        module: generic-module
        # Collect dependencies from local filesystem
        dependency: /harness/inputs/**/*.zip
        # Or collect from Artifactory:
        # from_rt: true
        # dependency: libs-release-local/**/*.jar

# 3) Download artifacts (records dependencies into build-info partials)
- step:
    type: Plugin
    name: DownloadArtifacts
    identifier: DownloadArtifacts
    spec:
      connectorRef: account.harnessImage
      image: plugins/artifactory:linux-amd64
      settings:
        command: download
        url: https://URL.jfrog.io/artifactory
        username: user
        password: <+secrets.getValue("jfrog_password")>
        build_name: sample-build
        build_number: 123
        module: generic-module
        # Use a file spec (recommended)
        spec: |
          {
            "files": [
              { "pattern": "libs-release-local/com/example/**/artifact-*.jar",
                "target": "./downloads/",
                "recursive": "true",
                "flat": "false"
              }
            ]
          }

# 4) Publish build-info (aggregates all partials). Optionally auto-clean afterwards
- step:
    type: Plugin
    name: PublishBuildInfo
    identifier: PublishBuildInfo
    spec:
      connectorRef: account.harnessImage
      image: plugins/artifactory:linux-amd64
      settings:
        command: publish-build-info
        url: https://URL.jfrog.io
        username: user
        password: <+secrets.getValue("jfrog_password")>
        build_name: sample-build
        build_number: 123
        cleanup_after_publish: true

# 5) (Optional) Explicit cleanup step (only needed if not using cleanup_after_publish)
- step:
    type: Plugin
    name: CleanBuildInfo
    identifier: CleanBuildInfo
    spec:
      connectorRef: account.harnessImage
      image: plugins/artifactory:linux-amd64
      settings:
        command: cleanup
        build_name: sample-build
        build_number: 123
```

Notes
- TLS: Provide custom CA PEM via `pem_file_contents` or `pem_file_path`. To skip verification, set `insecure: true`.
- Access token: You can use `access_token` in place of `username`/`password`.
- If you prefer to publish after each operation, set `publish_build_info: true` on those steps (and optionally `cleanup_after_publish: true`).

