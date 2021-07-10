# Release Process

1. Once a change has been pushed to `main`, the [publish GitHub action](https://github.com/lightstep/opentelemetry-prometheus-sidecar/blob/main/.github/workflows/publish.yml) automatically publishes a new Docker image. See an example [here](https://github.com/lightstep/opentelemetry-prometheus-sidecar/actions/runs/654707395).
2. Validate the changes by testing the new image.
3. Update [VERSION](https://github.com/lightstep/opentelemetry-prometheus-sidecar/blob/main/VERSION) and [CHANGELOG.md](https://github.com/lightstep/opentelemetry-prometheus-sidecar/blob/main/CHANGELOG.md) to the updated version.
4. Create a pull request for the update to the new version
5. When the pull request is merged, create a tag for the new release. This will trigger the [release GitHub action](https://github.com/lightstep/opentelemetry-prometheus-sidecar/blob/main/.github/workflows/release.yml) which tags the build with the correct version number.
    `git tag v0.1.2 && git push --tag`
6. Copy and paste the CHANGELOG section for the new version into the [release](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.19.0).

### Automated deplpoyment to Lightstep staging and public enviroments 
Both the publish Github Action and the Release Github Action initiate the [staging-approval-public Github Action](https://github.com/lightstep/opentelemetry-prometheus-sidecar/actions/workflows/staging-approval-public.yml). If the initiation is from the publish Github Action the OTEL sidecar image tag will be set to the commit sha, if the initiation is from the release Github Action the OTEL sidecar image tag will be set to the tag. 


The staging-approval-public Github Action initiates deployment to staging and then waits for approval before deploying to public (see Approval for deployment to public below) . The deployments themselves are carried out by the Codefresh listed pipelines below, i.e the staging-approval-public Github Action simply calls the Codefresh pipelines. 
* staging Codefresh Pipeline: https://github.com/lightstep/lightstep/blob/main/codefresh/static_yaml/prom-stack/deploy-operator-to-stg.yaml
* public (production) Codefresh Pipeline: https://github.com/lightstep/lightstep/blob/main/codefresh/static_yaml/prom-stack/deploy-operator-to-pub.yaml

### Approval for deployment to public
The approval is implemented using a Github Environment protection rule [here](https://github.com/lightstep/opentelemetry-prometheus-sidecar/settings/environments).  Owners of this repo can modify the approvers list be navigating to Settings > Enviroments > public
