# Testing

This document provides comprehensive testing guidance for the Splunk Universal Forwarder container image.
It covers local development testing, HTTP endpoint validation, Splunk daemon verification, vulnerability scanning, CI/CD pipeline testing, and integration testing in Kubernetes environments.

## Table of Contents

* [Overview](#overview)
* [Prerequisites](#prerequisites)
* [Local Development Testing](#local-development-testing)
* [HTTP Endpoint Testing](#http-endpoint-testing)
* [Splunk Daemon Testing](#splunk-daemon-testing)
* [Vulnerability Scanning](#vulnerability-scanning)
* [CI/CD Pipeline Testing](#cicd-pipeline-testing)
* [Integration Testing](#integration-testing)
* [Testing Improvements](#testing-improvements)

## Overview

### Testing Philosophy

This project emphasizes container-level integration testing rather than traditional unit testing.
The testing approach validates:

* **Container lifecycle:** Startup, health checks, graceful shutdown
* **HTTP endpoints:** Liveness, readiness, metrics exposure
* **Process management:** Splunk daemon auto-restart and monitoring
* **Security:** Vulnerability scanning and non-root execution
* **Integration:** Kubernetes deployment and orchestration

### Testing Levels

* **Local Development:** Build and run container, test endpoints manually
* **Automated Scanning:** Vulnerability detection via Clair integration
* **CI/CD Pipeline:** Automated builds triggered by pull requests and merges
* **Integration:** Kubernetes deployment with health probes and monitoring

## Prerequisites

### Required Tools

* **podman** or **docker** - Container runtime
* **make** - Build automation
* **git** - Version control
* **curl** - HTTP endpoint testing
* **kubectl** (optional) - Kubernetes integration testing

### Version Information

**Splunk Version:** Defined in [.splunk-version][splunk-version-file]

```bash
cat .splunk-version
# Output: 10.2.0
```

**Splunk Build Hash:** Defined in [.splunk-version-hash][splunk-version-hash-file]

```bash
cat .splunk-version-hash
# Output: d749cb17ea65
```

## Local Development Testing

### Build the Container Image

Build using the [Makefile][makefile]:

```bash
make build
```

**What this does:**

* Detects container engine (podman or docker)
* Builds multi-stage [Dockerfile][dockerfile]
* Compiles Go [runner][runner-go] binary
* Installs Splunk Universal Forwarder RPM
* Tags image with version and commit hash

**Expected output:**

```text
podman build . -f build/Dockerfile -t quay.io/app-sre/splunk-forwarder:10.2.0-d749cb17ea65-abc123
STEP 1/20: FROM quay.io/redhat-services-prod/openshift/boilerplate:image-v8.3.2 AS builder
...
Successfully tagged quay.io/app-sre/splunk-forwarder:10.2.0-d749cb17ea65-abc123
```

### Run the Container Locally

Start the container with required environment variables:

```bash
podman run -d \
  --name splunk-forwarder-test \
  -e SPLUNK_ACCEPT_LICENSE=yes \
  -p 8090:8090 \
  quay.io/app-sre/splunk-forwarder:$(cat .splunk-version)-$(cat .splunk-version-hash)-$(git rev-parse --short HEAD)
```

**Required flags:**

* `-e SPLUNK_ACCEPT_LICENSE=yes` - Mandatory license acceptance (container exits without this)
* `-p 8090:8090` - Expose HTTP server port for endpoint testing
* `--name` - Named container for easier log retrieval

**Expected startup logs:**

```text
Generating admin password...
Generated password: <8-char-random>
Enabling Splunk API...
License accepted
Starting HTTP server on :8090
Starting Splunk daemon...
Starting log tail...
```

**Verify container is running:**

```bash
podman ps | grep splunk-forwarder-test
```

### Retrieve Admin Credentials

The runner generates random admin credentials on startup.
Retrieve them from container logs:

```bash
podman logs splunk-forwarder-test 2>&1 | grep -A 1 "generated password"
```

**Example output:**

```text
Generated password: aB3xY9Zq
```

**Credential details:**

* **Username:** admin
* **Password:** 8-character random string (generated via `splunk gen-random-passwd`)
* **Configuration:** Written to `/opt/splunkforwarder/etc/system/local/user-seed.conf`
* **Use case:** Access Splunk API at `http://127.0.0.1:8089` (from inside container)

## HTTP Endpoint Testing

The runner exposes three HTTP endpoints on port 8090.
See [runner.go][runner-go-server] for implementation details.

### Endpoint: GET /metrics

**Purpose:** Prometheus metrics for Splunk component health

**Test command:**

```bash
curl -i http://localhost:8090/metrics
```

**Expected response (200 OK):**

```text
HTTP/1.1 200 OK
Content-Type: text/plain; version=0.0.4
Date: Tue, 13 May 2026 14:23:45 GMT

# HELP splunk_forwarder_component_unhealthy Splunk component health status
# TYPE splunk_forwarder_component_unhealthy gauge
splunk_forwarder_component_unhealthy{component="BatchReader"} 0
splunk_forwarder_component_unhealthy{component="BundlesReplication"} 0
splunk_forwarder_component_unhealthy{component="DispatchManager"} 0
splunk_forwarder_component_unhealthy{component="DispatchManager/SearchScheduler"} 0
splunk_forwarder_component_unhealthy{component="IndexProcessor"} 0
splunk_forwarder_component_unhealthy{component="TailReader"} 0
```

**Metric details:**

* **Metric name:** `splunk_forwarder_component_unhealthy`
* **Metric type:** Gauge
* **Label:** `component` (flattened component path with spaces/hyphens removed, slashes added)
* **Values:**
  * `0` - Component is healthy (green status from Splunk API)
  * `1` - Component is unhealthy (yellow or red status from Splunk API)

**Implementation:** [runner.go:240-255][runner-go-metrics]

**Component flattening behavior:**

Splunk health API returns nested feature tree like:

```json
{
  "Dispatch Manager": {
    "health": "green",
    "features": {
      "Search Scheduler": {
        "health": "green"
      }
    }
  }
}
```

The runner flattens this to Prometheus labels:

* `"Dispatch Manager"` → `component="DispatchManager"`
* `"Dispatch Manager" → "Search Scheduler"` → `component="DispatchManager/SearchScheduler"`

### Endpoint: GET /livez

**Purpose:** Kubernetes liveness probe - detect crashed Splunk daemon

**Test command:**

```bash
curl -i http://localhost:8090/livez
```

**Expected response when healthy (200 OK):**

```text
HTTP/1.1 200 OK
Date: Tue, 13 May 2026 14:23:45 GMT
Content-Length: 3
Content-Type: text/plain; charset=utf-8

ok
```

**Expected response when unhealthy (500 Internal Server Error):**

```text
HTTP/1.1 500 Internal Server Error
Date: Tue, 13 May 2026 14:23:45 GMT
Content-Length: 7
Content-Type: text/plain; charset=utf-8

not ok
```

**Health check logic:**

* **Healthy:** Splunk daemon process (`splunkd`) is still running
* **Unhealthy:** Splunk daemon process has exited (checked via `cmd.ProcessState.Exited()`)

**Implementation:** [runner.go:257-267][runner-go-livez]

**Kubernetes use case:**

Liveness probes detect when a container has entered a broken state and needs to be restarted.
If `/livez` returns 500, Kubernetes will restart the pod.

### Endpoint: GET /healthz

**Purpose:** Kubernetes readiness probe - detect unhealthy Splunk components

**Test command (basic):**

```bash
curl -i http://localhost:8090/healthz
```

**Expected response when all components healthy (200 OK):**

```text
HTTP/1.1 200 OK
Date: Tue, 13 May 2026 14:23:45 GMT
Content-Length: 3
Content-Type: text/plain; charset=utf-8

ok
```

**Expected response when any component unhealthy (500 Internal Server Error):**

```text
HTTP/1.1 500 Internal Server Error
Date: Tue, 13 May 2026 14:23:45 GMT
Content-Length: 7
Content-Type: text/plain; charset=utf-8

not ok
```

**Test command (verbose mode):**

```bash
curl -i http://localhost:8090/healthz?verbose
```

**Expected verbose response (200 OK):**

```text
HTTP/1.1 200 OK
Date: Tue, 13 May 2026 14:23:45 GMT
Content-Type: text/plain; charset=utf-8

[+] BatchReader ok
[+] BundlesReplication ok
[+] DispatchManager ok
[+] DispatchManager/SearchScheduler ok
[+] IndexProcessor ok
[+] TailReader ok
```

**Expected verbose response (500 Internal Server Error):**

```text
HTTP/1.1 500 Internal Server Error
Date: Tue, 13 May 2026 14:23:45 GMT
Content-Type: text/plain; charset=utf-8

[+] BatchReader ok
[+] BundlesReplication not ok
[+] DispatchManager ok
[+] DispatchManager/SearchScheduler ok
[+] IndexProcessor ok
[+] TailReader ok
```

**Health check logic:**

* **Healthy:** All Splunk components report "green" status from health API
* **Unhealthy:** At least one component reports "yellow" or "red" status

**Implementation:** [runner.go:269-287][runner-go-healthz]

**Verbose mode format:** `[+]<component> <ok|not ok>` (one per line)

**Kubernetes use case:**

Readiness probes determine when a container is ready to accept traffic.
If `/healthz` returns 500, Kubernetes will remove the pod from service endpoints until it recovers.

### Health API Details

All health checks query the Splunk internal health API:

**API endpoint:** `http://127.0.0.1:8089/services/server/health/splunkd/details`

**Authentication:** Basic auth with auto-generated admin credentials

**Query parameters:** `output_mode=json`

**Response format:**

```json
{
  "entry": [{
    "content": {
      "health": "green",
      "features": {
        "Component Name": {
          "health": "green",
          "features": {
            "Sub Component": {
              "health": "green"
            }
          }
        }
      }
    }
  }]
}
```

**Status types:**

* **green:** Component is healthy and functioning normally
* **yellow:** Component is degraded but operational (treated as unhealthy)
* **red:** Component has failed (treated as unhealthy)

**Implementation:** [runner.go:295-315][runner-go-health-check]

## Splunk Daemon Testing

### Verify Successful Startup

Check that the Splunk daemon (splunkd) started successfully:

```bash
podman exec splunk-forwarder-test ps aux | grep splunkd
```

**Expected output:**

```text
splunkf+      15  0.0  0.1 123456 12345 ?  Sl   14:23   0:00 splunkd --nodaemon --accept-license --answer-yes
```

**Startup process:** [runner.go:189-215][runner-go-process-mgmt]

### Access Splunk API

The Splunk management API is accessible on localhost:8089 (from inside the container only).

**Test from inside container:**

```bash
podman exec splunk-forwarder-test curl -k -u admin:<password> \
  'http://127.0.0.1:8089/services/server/health/splunkd/details?output_mode=json'
```

Replace `<password>` with the admin password retrieved from logs.

**Security note:** API is restricted to localhost only via `acceptFrom = 127.0.0.1/8` in server.conf.
See [Security Model][architecture-security] in architecture documentation.

### Verify Log Tailing

The runner tails `splunkd.log` to container stderr for visibility.

**View logs:**

```bash
podman logs -f splunk-forwarder-test
```

**Expected output includes Splunk daemon logs:**

```text
INFO  loader - Splunk Enterprise v10.2.0 build d749cb17ea65 starting...
INFO  loader - Module system initialized
INFO  TcpInputProc - Creating fwd data Acceptor for IPv4 port 9997
...
```

**Implementation:** [runner.go:353-356][runner-go-tail]

### Test Auto-Restart Behavior

The runner automatically restarts the Splunk daemon if it exits unexpectedly (every 5 seconds).

**Kill the Splunk daemon:**

```bash
podman exec splunk-forwarder-test pkill splunkd
```

**Watch logs for restart:**

```bash
podman logs -f splunk-forwarder-test
```

**Expected output:**

```text
Splunk daemon exited with code 137, restarting in 5s...
Starting Splunk daemon...
INFO  loader - Splunk Enterprise v10.2.0 build d749cb17ea65 starting...
```

**Auto-restart logic:** [runner.go:344-349][runner-go-splunk-start]

**Restart interval:** 5 seconds (hardcoded)

### Test License Validation

The container requires `SPLUNK_ACCEPT_LICENSE=yes` environment variable.

**Test without license acceptance:**

```bash
podman run --rm \
  quay.io/app-sre/splunk-forwarder:10.2.0-d749cb17ea65-$(git rev-parse --short HEAD)
```

**Expected behavior:**

Container exits immediately with error:

```text
Error: SPLUNK_ACCEPT_LICENSE must be set to 'yes'
```

**License validation:** [runner.go:329-334][runner-go-license]

## Vulnerability Scanning

### Run Vulnerability Check

Execute vulnerability scanning via [Makefile][makefile]:

```bash
make vuln-check
```

**What this does:**

1. Builds the container image (if not already built)
2. Runs [hack/check-image-against-osd-sre-clair.sh][vuln-script]
3. Queries OSD-SRE Clair server for CVE detection
4. Reports vulnerability count and details

**Expected output (no vulnerabilities):**

```text
Scanning image: quay.io/app-sre/splunk-forwarder:10.2.0-d749cb17ea65-abc123
Extracting package list...
Querying Clair server...
No vulnerabilities found.
```

**Expected output (vulnerabilities found):**

```text
Scanning image: quay.io/app-sre/splunk-forwarder:10.2.0-d749cb17ea65-abc123
Extracting package list...
Querying Clair server...
Found 3 vulnerabilities:
  CVE-2024-1234 (High): libfoo-1.2.3
  CVE-2024-5678 (Medium): libbar-2.3.4
  CVE-2024-9012 (Low): libbaz-3.4.5
```

**Script details:**

* **Location:** [hack/check-image-against-osd-sre-clair.sh][vuln-script]
* **Clair server:** `https://clair.apps.osd-v4prod-aws.n2a0.p1.openshiftapps.com`
* **Package managers supported:** RPM (rpmquery), DEB (dpkg-query), APK (apk version)
* **Exit code:** Non-zero if vulnerabilities found

**Manual execution:**

```bash
./hack/check-image-against-osd-sre-clair.sh quay.io/app-sre/splunk-forwarder:10.2.0-d749cb17ea65-abc123
```

## CI/CD Pipeline Testing

### Pipeline Overview

This project uses [Tekton Pipelines][tekton-docs] for CI/CD automation.

**Pipeline configurations:**

* [Pull Request Pipeline][pr-pipeline]: `.tekton/splunk-forwarder-images-pull-request.yaml`
* [Push Pipeline][push-pipeline]: `.tekton/splunk-forwarder-images-push.yaml`

**Boilerplate pipeline:** [boilerplate/pipelines/docker-build-oci-ta/pipeline.yaml][boilerplate-pipeline]

### Pull Request Pipeline

**Trigger:** Pull request events to `master` branch

**Triggered by changes to:**

* `build/Dockerfile`
* `.splunk-version`
* `.splunk-version-hash`
* `.tekton/splunk-forwarder-images-pull-request.yaml`

**Output image:**

```text
quay.io/redhat-user-workloads/splunk-forwarder-images-tenant/openshift/splunk-forwarder-images:on-pr-{{revision}}
```

**Image expiration:** 5 days (automatically cleaned up)

**Max keep runs:** 3

**Cancel behavior:** In-progress runs are cancelled when new commits are pushed

### Push Pipeline

**Trigger:** Push events to `master` branch (after PR merge)

**Triggered by changes to:**

* `build/Dockerfile`
* `.splunk-version`
* `.splunk-version-hash`
* `.tekton/splunk-forwarder-images-push.yaml`

**Output image:**

```text
quay.io/redhat-user-workloads/splunk-forwarder-images-tenant/openshift/splunk-forwarder-images:{{revision}}
```

**Image expiration:** None (production image)

**Cancel behavior:** Disabled (merges always run to completion)

### Common Pipeline Failures

#### Dockerfile syntax errors

* **Symptom:** Pipeline fails during build step
* **Check:** Validate Dockerfile locally with `podman build`
* **Fix:** Correct syntax errors in [build/Dockerfile][dockerfile]

#### Splunk version unavailable

* **Symptom:** Pipeline fails when downloading Splunk RPM
* **Check:** Verify version/hash in [.splunk-version][splunk-version-file] and [.splunk-version-hash][splunk-version-hash-file]
* **Fix:** Update to available Splunk UF version

#### Go build failures

* **Symptom:** Pipeline fails during runner compilation
* **Check:** Test locally with `go build -o runner runner.go`
* **Fix:** Correct Go syntax errors in [runner.go][runner-go]

#### Vulnerability scan failures

* **Symptom:** Pipeline fails after successful build
* **Check:** Run `make vuln-check` locally
* **Fix:** Update base image or Splunk version to address CVEs

### Viewing Pipeline Runs

**Via OpenShift Console:**

1. Navigate to Pipelines → PipelineRuns
2. Filter by repository: `openshift/splunk-forwarder-images`
3. View logs and status for each run

**Via CLI:**

```bash
tkn pipelinerun list
tkn pipelinerun logs <pipelinerun-name> -f
```

## Integration Testing

### Kubernetes Deployment

Example minimal Deployment for testing:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: splunk-forwarder-test
  namespace: test
spec:
  replicas: 1
  selector:
    matchLabels:
      app: splunk-forwarder
  template:
    metadata:
      labels:
        app: splunk-forwarder
    spec:
      containers:
      - name: splunk-forwarder
        image: quay.io/app-sre/splunk-forwarder:10.2.0-d749cb17ea65-abc123
        env:
        - name: SPLUNK_ACCEPT_LICENSE
          value: "yes"
        ports:
        - containerPort: 8090
          name: http
          protocol: TCP
        livenessProbe:
          httpGet:
            path: /livez
            port: 8090
          initialDelaySeconds: 30
          periodSeconds: 10
          timeoutSeconds: 5
          failureThreshold: 3
        readinessProbe:
          httpGet:
            path: /healthz
            port: 8090
          initialDelaySeconds: 10
          periodSeconds: 5
          timeoutSeconds: 3
          failureThreshold: 3
        resources:
          requests:
            memory: "256Mi"
            cpu: "100m"
          limits:
            memory: "512Mi"
            cpu: "500m"
```

**Apply deployment:**

```bash
kubectl apply -f deployment.yaml
```

### Verify Health Probes

**Check liveness probe status:**

```bash
kubectl get pod -l app=splunk-forwarder -o jsonpath='{.items[0].status.conditions[?(@.type=="Ready")].status}'
```

**Expected output:** `True`

**Test endpoints from within cluster:**

```bash
kubectl exec -it deployment/splunk-forwarder-test -- curl http://localhost:8090/livez
kubectl exec -it deployment/splunk-forwarder-test -- curl http://localhost:8090/healthz
kubectl exec -it deployment/splunk-forwarder-test -- curl http://localhost:8090/metrics
```

**View events for probe failures:**

```bash
kubectl get events --field-selector involvedObject.name=<pod-name>
```

### Prometheus Metrics Scraping

Example ServiceMonitor for Prometheus Operator:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: splunk-forwarder-metrics
  namespace: test
spec:
  selector:
    matchLabels:
      app: splunk-forwarder
  endpoints:
  - port: http
    path: /metrics
    interval: 30s
```

**Verify scraping:**

1. Check Prometheus targets: `/targets` endpoint shows `UP` status
2. Query metrics: `splunk_forwarder_component_unhealthy`
3. Alert on unhealthy components: `splunk_forwarder_component_unhealthy > 0`

### Verify Container Logs

**View aggregated logs:**

```bash
kubectl logs -l app=splunk-forwarder --tail=100 -f
```

## Testing Improvements

### Unit Tests

This repository currently has **no unit tests** (`*_test.go` files).
Contributions are welcome for test coverage in the following areas:

#### Health Check Flattening Logic

* Test `Feature.Flatten()` method ([runner.go:109-124][runner-go-flatten])
* Verify component path flattening (spaces/hyphens removed, slashes added)
* Test nested feature tree traversal
* Example: `"Dispatch Manager" → "Search Scheduler"` becomes `"DispatchManager/SearchScheduler"`

#### Status Parsing from Splunk API

* Test `Status.Healthy()` method ([runner.go:87-89][runner-go-status-healthy])
* Verify "green" → healthy, "yellow"/"red" → unhealthy mapping
* Test JSON unmarshaling from Splunk API response

#### Credential Generation

* Test `genPasswd()` function ([runner.go:139-153][runner-go-genPasswd])
* Verify password length (8 characters)
* Test healthURL configuration with credentials

#### Configuration File Writing

* Test `generateUserSeed()` function ([runner.go:154-168][runner-go-generateUserSeed])
* Verify user-seed.conf format and content
* Test `enableSplunkAPI()` function ([runner.go:170-183][runner-go-enableSplunkAPI])
* Verify server.conf format and security settings

[architecture-security]: ARCHITECTURE.md#security-model
[boilerplate-pipeline]: https://github.com/openshift/boilerplate/blob/master/pipelines/docker-build-oci-ta/pipeline.yaml
[dockerfile]: ../build/Dockerfile
[makefile]: ../Makefile
[pr-pipeline]: ../.tekton/splunk-forwarder-images-pull-request.yaml
[push-pipeline]: ../.tekton/splunk-forwarder-images-push.yaml
[runner-go-enableSplunkAPI]: ../runner.go#L170-L183
[runner-go-flatten]: ../runner.go#L109-L124
[runner-go-genPasswd]: ../runner.go#L139-L153
[runner-go-generateUserSeed]: ../runner.go#L154-L168
[runner-go-health-check]: ../runner.go#L295-L315
[runner-go-healthz]: ../runner.go#L269-L287
[runner-go-license]: ../runner.go#L329-L334
[runner-go-livez]: ../runner.go#L257-L267
[runner-go-metrics]: ../runner.go#L240-L255
[runner-go-process-mgmt]: ../runner.go#L189-L215
[runner-go-server]: ../runner.go#L238-L289
[runner-go-splunk-start]: ../runner.go#L344-L349
[runner-go-status-healthy]: ../runner.go#L87-L89
[runner-go-tail]: ../runner.go#L353-L356
[runner-go]: ../runner.go
[splunk-version-file]: ../.splunk-version
[splunk-version-hash-file]: ../.splunk-version-hash
[tekton-docs]: https://tekton.dev/docs/
[vuln-script]: ../hack/check-image-against-osd-sre-clair.sh
