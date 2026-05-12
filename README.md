# splunk-forwarder-images

Build and push container images for Splunk Universal Forwarder.

## Project Description

This project provides a containerized Splunk Universal Forwarder with a custom Go runner that manages splunkd as a child process.
The runner provides health checks, Prometheus metrics, and automatic restart capabilities designed for OpenShift and Kubernetes environments.

**Key features:**

* Health check endpoints for Kubernetes liveness and readiness probes
* Prometheus metrics for monitoring Splunk component health
* Automatic restart of Splunk daemon on failure
* Auto-generated admin credentials on first startup
* Log aggregation via stdout/stderr

## Architecture Overview

```mermaid
graph TD
    A[Container] --> B[Runner - Main Process]
    B --> C[Splunk Daemon - Child Process]
    B --> D[HTTP Server :8090]
    D --> E[/metrics - Prometheus]
    D --> F[/livez - Liveness Probe]
    D --> G[/healthz - Readiness Probe]
    C --> H[Splunk API :8089]
```

The runner serves as the main container process, managing:

* Splunk daemon lifecycle (start, monitor, restart)
* HTTP endpoints on port 8090 for health and metrics
* Graceful shutdown on SIGTERM/SIGINT signals
* Log tailing for container visibility

## Prerequisites

* Go 1.25.7+ (for building the runner binary)
* Docker or Podman
* Make
* Git

## Quick Start

```bash
# Clone repository
git clone https://github.com/openshift/splunk-forwarder-images
cd splunk-forwarder-images

# Build image
make build-forwarder

# Run container (must accept Splunk license)
podman run -e SPLUNK_ACCEPT_LICENSE=yes \
  -p 8090:8090 \
  quay.io/app-sre/splunk-forwarder:10.2.0-d749cb17ea65-$(git rev-parse --short HEAD)

# Test endpoints
curl http://localhost:8090/healthz
curl http://localhost:8090/livez
curl http://localhost:8090/metrics
```

## HTTP Endpoints

The runner exposes three HTTP endpoints on port 8090:

* `GET /metrics` - Prometheus metrics for component health
* `GET /livez` - Liveness probe (process running check)
* `GET /healthz` - Readiness probe (Splunk health check, supports `?verbose`)

For detailed API documentation, see [docs/API.md][api-docs].

## Environment Variables

### SPLUNK_ACCEPT_LICENSE

**Required:** Yes

**Value:** Must be set to `yes` to indicate acceptance of Splunk licensing terms

The container will fail to start without this variable set.

**See:** [Splunk Software License Agreement][splunk-license]

### SPLUNK_HOME

**Required:** No (automatically set to `/opt/splunkforwarder`)

## Configuration

The runner automatically configures Splunk on first startup:

* Admin credentials are auto-generated and logged to stdout
* Splunk API is enabled on `localhost:8089` (non-SSL for internal use)
* Configuration files are written to `/opt/splunkforwarder/etc/system/local/`

**Retrieving admin credentials:**

```bash
podman logs <container-id> 2>&1 | grep -A 1 "generated password"
```

## Local Build/Test

The following `make` variables affect building:

* `IMAGE_REGISTRY` (default `quay.io`)
* `IMAGE_REPOSITORY` (default `app-sre`)
* `FORWARDER_NAME` (default `splunk-forwarder`)

Images are tagged as `${IMAGE_REGISTRY}/${IMAGE_REPOSITORY}/${FORWARDER_NAME}:${VERSION}-${HASH}-${COMMIT}`, where `${VERSION}` and `${HASH}` are gleaned from [.splunk-version][splunk-version] and [.splunk-version-hash][splunk-hash], respectively, and `${COMMIT}` is the 7 char short current commit hash of this repository.

**Build images:**

```bash
make build-forwarder
```

**Build with custom repository:**

```bash
make build-forwarder \
  IMAGE_REGISTRY=quay.io \
  IMAGE_REPOSITORY=myusername \
  FORWARDER_NAME=splunk-forwarder
```

**Run vulnerability checks:**

```bash
make vuln-check
```

This uses Clair to scan the container image for known CVEs in installed packages.

## Versioning and Tagging

This repository builds container images around the Splunk Universal Forwarder at the version and hash specified in the [.splunk-version][splunk-version] and [.splunk-version-hash][splunk-hash] files, respectively.

**Current version:** 10.2.0 (hash: d749cb17ea65)

To build around a new version:

1. Visit the [Splunk Universal Forwarder downloads page][splunk-downloads] (requires login with a free Splunk account)
2. Find the desired Linux x86_64 RPM version
3. Extract the version number and hash from the download URL
4. Update [.splunk-version][splunk-version] with the version number (e.g., `10.2.0`)
5. Update [.splunk-version-hash][splunk-hash] with the hash (e.g., `d749cb17ea65`)
6. Commit both files in a PR

## CICD

This repository uses Tekton pipelines for continuous integration and delivery.
All image pushing is handled by CI/CD pipelines.

**Pipeline triggers:**

* **Pull Request:** Runs when a PR modifies the Dockerfile, version files, or pipeline configuration
* **Push to master:** Runs after PR merge to build and push production images

**Image destinations:**

* **PR builds:** `quay.io/redhat-user-workloads/.../<namespace>/on-pr-<revision>`
* **Push builds:** `quay.io/redhat-user-workloads/.../<namespace>:<revision>`
* **Production:** `quay.io/app-sre/splunk-forwarder` (via app-sre integration)

**PR image expiration:** Images from PR builds expire after 5 days

## Contributing

We welcome contributions!
Please see [CONTRIBUTING.md][contributing] for development setup, coding standards, and the pull request process.

**Quick links:**

* [Architecture documentation][architecture-docs]
* [Testing guide][testing-docs]
* [API reference][api-docs]
* [OWNERS file][owners] - Review and approval process

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE][license] file for details.

Using this container image requires acceptance of the [Splunk Software License Agreement][splunk-license], indicated by setting the `SPLUNK_ACCEPT_LICENSE` environment variable to `yes`.

[api-docs]: docs/API.md
[architecture-docs]: docs/ARCHITECTURE.md
[contributing]: CONTRIBUTING.md
[license]: LICENSE
[owners]: OWNERS
[splunk-downloads]: https://www.splunk.com/en_us/download/universal-forwarder.html
[splunk-hash]: .splunk-version-hash
[splunk-license]: https://www.splunk.com/en_us/legal/splunk-software-license-agreement.html
[splunk-version]: .splunk-version
[testing-docs]: docs/TESTING.md
