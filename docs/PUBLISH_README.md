A plugin to publish build-info metadata to JFrog Artifactory (no JFrog CLI required).

Run the following script to install git-leaks support to this repo.
```
chmod +x ./git-hooks/install.sh
./git-hooks/install.sh
```

# Building

Build the plugin binary:

```text
scripts/build.sh
```

Build the plugin image:

```text
docker build -t plugins/artifactory  -f docker/Dockerfile .
```

#  Publish build info to JFrog Artifactory
This step publishes build-info metadata to Artifactory. The plugin accumulates build-info partials from previous steps (upload, download, add-build-dependencies) and aggregates them for publish.

### Publish build-info metadata
```yaml
- step:
    type: Plugin
    name: PublishStep
    identifier: PublishStep
    spec:
      connectorRef: account.harnessImage
      image: plugins/artifactory:linux-amd64
      settings:
        command: publish-build-info
        url: https://URL.jfrog.io
        username: user
        password: <+secrets.getValue("jfrog_user")>
        build_name: gol-01
        build_number: 0.03.01
        cleanup_after_publish: true

### Cleanup local build-info cache
Use the cleanup command to remove local cached build-info for a build.

```yaml
- step:
    type: Plugin
    name: CleanBuildInfo
    identifier: CleanBuildInfo
    spec:
      connectorRef: account.harnessImage
      image: plugins/artifactory:linux-amd64
      settings:
        command: cleanup
        build_name: gol-01
        build_number: 0.03.01
```
```

## Community and Support
[Harness Community Slack](https://join.slack.com/t/harnesscommunity/shared_invite/zt-y4hdqh7p-RVuEQyIl5Hcx4Ck8VCvzBw) - Join the #drone slack channel to connect with our engineers and other users running Drone CI.

[Harness Community Forum](https://community.harness.io/) - Ask questions, find answers, and help other users.

[Report and Track A Bug](https://community.harness.io/c/bugs/17) - Find a bug? Please report in our forum under Drone Bugs. Please provide screenshots and steps to reproduce. 

[Events](https://www.meetup.com/harness/) - Keep up to date with Drone events and check out previous events [here](https://www.youtube.com/watch?v=Oq34ImUGcHA&list=PLXsYHFsLmqf3zwelQDAKoVNmLeqcVsD9o).
