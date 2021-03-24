# Release Process

1. Once a change has been pushed to `main`, the [publish GitHub action](https://github.com/lightstep/opentelemetry-prometheus-sidecar/blob/main/.github/workflows/publish.yml) automatically publishes a new Docker image. See an example [here](https://github.com/lightstep/opentelemetry-prometheus-sidecar/actions/runs/654707395).
2. Validate the changes by testing the new image.
3. Update [VERSION](https://github.com/lightstep/opentelemetry-prometheus-sidecar/blob/main/VERSION) and [CHANGELOG.md](https://github.com/lightstep/opentelemetry-prometheus-sidecar/blob/main/CHANGELOG.md) to the updated version.
4. Create a pull request for the update to the new version
5. When the pull request is merged, create a tag for the new release. This will trigger the [release GitHub action](https://github.com/lightstep/opentelemetry-prometheus-sidecar/blob/main/.github/workflows/release.yml) which tags the build with the correct version number.
    `git tag v0.1.2 && git push --tag`
6. Copy and paste the CHANGELOG section for the new version into the [release](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.19.0).
