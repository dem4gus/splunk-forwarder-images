# Troubleshooting

## Table of Contents

* [Overview](#overview)
* [Common Issues and Solutions](#common-issues-and-solutions)
  * [Container Exits Immediately](#container-exits-immediately)
  * [Health Check Failures](#health-check-failures)
  * [Splunk Daemon Crashes or Restarts](#splunk-daemon-crashes-or-restarts)
  * [Metrics Not Appearing](#metrics-not-appearing)
  * [Performance Problems](#performance-problems)
* [Log Analysis Guide](#log-analysis-guide)
* [Debugging Techniques](#debugging-techniques)
* [Getting Support](#getting-support)

## Overview

This guide focuses on troubleshooting the Splunk Universal Forwarder container running as a pod in OpenShift clusters.

## Common Issues and Solutions

### Container Exits Immediately

#### Symptom

Pod status shows `CrashLoopBackOff` or `Error`.
Container restarts repeatedly within seconds of starting.

```bash
oc get pods -l app=splunk-forwarder
```

```text
NAME                               READY   STATUS             RESTARTS   AGE
splunk-forwarder-6d8f4c9b7-x7k2q   0/1     CrashLoopBackOff   5          3m
```

#### Cause

The `SPLUNK_ACCEPT_LICENSE` environment variable is not set to `yes`.
The runner validates license acceptance during startup and exits if the variable is missing or has any value other than `yes`.

**Code reference:** [runner.go:329-334][runner-go-license]

#### Solution

Add the environment variable to your Deployment or Pod specification:

```yaml
spec:
  containers:
  - name: splunk-forwarder
    image: quay.io/app-sre/splunk-forwarder:10.2.0-d749cb17ea65-abc123
    env:
    - name: SPLUNK_ACCEPT_LICENSE
      value: "yes"
```

#### Verification

Check pod logs for successful license acceptance:

```bash
kubectl logs -l app=splunk-forwarder
```

**Expected output:**

```text
splunk license agreement has been accepted
```

The pod should transition to `Running` status within 30 seconds.

### Health Check Failures

### Symptom

Readiness probe failing, pod shows `0/1` in READY column.
Pod is running but not sending logs.

```bash
oc get pods -l app=splunk-forwarder
```

```text
NAME                               READY   STATUS    RESTARTS   AGE
splunk-forwarder-6d8f4c9b7-x7k2q   0/1     Running   0          5m
```

#### Cause

One or more Splunk components report unhealthy status (yellow or red) from the health API.
The `/healthz` endpoint returns 500 when any component is not green.

**Code reference:** [runner.go:269-287][runner-go-healthz]

#### Solution

Use verbose mode to identify which component is unhealthy:

```bash
oc exec <pod-name> -- curl -s http://localhost:8090/healthz?verbose
```

**Example output (unhealthy):**

```text
[+]BatchReader ok
[+]BundlesReplication ok
[+]DispatchManager ok
[+]DispatchManager/SearchScheduler ok
[+]IndexProcessor not ok
[+]TailReader ok
```

**Diagnosis based on component:**

* **IndexProcessor:** Check disk space, verify volume mounts, review index configuration
* **TailReader:** Verify hostPath mounts for log inputs, check file permissions
* **DispatchManager:** Review scheduled search configuration, check for resource exhaustion

**Check pod events for additional context:**

```bash
oc get events --field-selector involvedObject.name=<pod-name>
```

#### Verification

Healthy status returns all components as `ok`:

```bash
oc exec <pod-name> -- curl -s http://localhost:8090/healthz?verbose
```

```text
[+]BatchReader ok
[+]BundlesReplication ok
[+]DispatchManager ok
[+]DispatchManager/SearchScheduler ok
[+]IndexProcessor ok
[+]TailReader ok
```

The readiness probe succeeds and pod transitions to `1/1` READY.

### Splunk Daemon Crashes or Restarts

#### Symptom

Pod logs show repeated restart messages.
Container remains running but Splunk daemon exits periodically.

```text
splunkd exited, restarting in 5 seconds
Starting Splunk daemon...
```

#### Cause

**Common causes:**

* **Missing volumes:** Required hostPath mounts not configured
* **Configuration errors:** Invalid Splunk configuration files
* **Resource exhaustion:** CPU throttling or disk space issues

**Code reference:** [runner.go:344-349][runner-go-splunk-start]

The runner automatically restarts the Splunk daemon every 5 seconds when it exits unexpectedly.
This auto-restart behavior keeps the container running but indicates an underlying problem.

#### Solution

**Verify volume mounts:**

```bash
oc describe pod <pod-name> | grep -A 10 Mounts
```

Ensure hostPath volumes for log inputs are mounted correctly:

```yaml
volumeMounts:
- name: host-logs
  mountPath: /host/var/log
  readOnly: true
volumes:
- name: host-logs
  hostPath:
    path: /var/log
```

**Check Splunk daemon exit code:**

```bash
oc logs <pod-name> | grep "splunkd exited"
```

**Common exit codes:**

* `137` - SIGKILL (usually OOMKilled or liveness probe timeout)
* `1` - Configuration error or startup failure
* `143` - SIGTERM (graceful shutdown, expected during pod termination)

**Review Splunk configuration:**

```bash
oc exec <pod-name> -- ls -la /opt/splunkforwarder/etc/system/local/
oc exec <pod-name> -- cat /opt/splunkforwarder/etc/system/local/server.conf
```

Verify auto-generated configurations are correct (see [Architecture documentation][architecture-docs]).

#### Verification

Daemon runs continuously without restart messages:

```bash
kubectl logs <pod-name> --tail=50 -f
```

No `splunkd exited, restarting` messages should appear during normal operation.

### Metrics Not Appearing

#### Symptom

Prometheus not scraping metrics or `/metrics` endpoint returns errors.
No `splunk_forwarder_component_unhealthy` metrics visible in Prometheus.

#### Cause

**Common causes:**

* **Service not exposing port 8090:** Service definition missing or incorrect port mapping
* **NetworkPolicy blocking traffic:** Prometheus unable to reach pod
* **ServiceMonitor misconfigured:** Selector doesn't match Service labels
* **Prometheus target down:** Pod not healthy or not selected by ServiceMonitor

**Code reference:** [runner.go:240-255][runner-go-metrics]

#### Solution

**Step 1: Verify Service exposes port 8090**

```bash
kubectl get service splunk-forwarder -o yaml
```

**Expected configuration:**

```yaml
apiVersion: v1
kind: Service
metadata:
  name: splunk-forwarder
  labels:
    app: splunk-forwarder
spec:
  selector:
    app: splunk-forwarder
  ports:
  - name: http
    port: 8090
    protocol: TCP
    targetPort: 8090
```

**Step 2: Test metrics endpoint directly**

```bash
kubectl exec <pod-name> -- curl -s http://localhost:8090/metrics
```

**Expected output:**

```text
# HELP splunk_forwarder_component_unhealthy 
# TYPE splunk_forwarder_component_unhealthy gauge
splunk_forwarder_component_unhealthy{component="BatchReader"} 0
splunk_forwarder_component_unhealthy{component="DispatchManager"} 0
splunk_forwarder_component_unhealthy{component="IndexProcessor"} 0
```

**Step 3: Verify ServiceMonitor configuration**

```bash
kubectl get servicemonitor splunk-forwarder-metrics -o yaml
```

**Example configuration:**

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: splunk-forwarder-metrics
spec:
  selector:
    matchLabels:
      app: splunk-forwarder
  endpoints:
  - port: http
    path: /metrics
    interval: 30s
```

**Step 4: Check Prometheus targets**

Navigate to Prometheus UI → Status → Targets.
Search for `splunk-forwarder` and verify status is `UP`.

**If target is DOWN:**

* Check pod is running and healthy
* Verify NetworkPolicy allows Prometheus namespace to reach pod
* Confirm ServiceMonitor namespace matches Prometheus serviceMonitorNamespaceSelector

#### Verification

Metrics appear in Prometheus and can be queried:

```promql
splunk_forwarder_component_unhealthy
```

All components should show value `0` (healthy) under normal operation.

### Performance Problems

#### Symptom

Slow response times from health endpoints.
High CPU or memory usage.
Pod shows `OOMKilled` in status or events.

#### Cause

**Common causes:**

* **Insufficient resource requests/limits:** Pod starved for CPU/memory
* **CPU throttling:** Hitting CPU limits during normal operation
* **hostPath volume issues:** Disk I/O bottleneck on host filesystem
* **Large log volume from hostPath:** Processing excessive log data from mounted volumes

**Note:** Logs are mounted directly from hostPath volumes (typically `/var/log` from the host).
The Splunk daemon processes these logs, not logs generated within the container.

#### Solution

**Step 1: Check current resource usage**

```bash
kubectl top pod <pod-name>
```

**Example output:**

```text
NAME                               CPU(cores)   MEMORY(bytes)
splunk-forwarder-6d8f4c9b7-x7k2q   450m         480Mi
```

Compare against configured limits:

```bash
kubectl describe pod <pod-name> | grep -A 5 "Limits:"
```

**Step 2: Review resource configuration**

**Minimum recommended resources:**

```yaml
resources:
  requests:
    cpu: "100m"
    memory: "256Mi"
  limits:
    cpu: "500m"
    memory: "512Mi"
```

**For high-volume log processing:**

```yaml
resources:
  requests:
    cpu: "250m"
    memory: "512Mi"
  limits:
    cpu: "1000m"
    memory: "1Gi"
```

**Step 3: Verify hostPath mount performance**

```bash
kubectl exec <pod-name> -- df -h /host
kubectl exec <pod-name> -- ls -lh /host/var/log | head -20
```

Check for:

* Disk space availability on host filesystem
* Extremely large log files being processed
* Correct permissions on mounted directories

**Step 4: Check for CPU throttling**

```bash
kubectl describe pod <pod-name> | grep -i throttl
```

If throttling occurs frequently, increase CPU limits.

**Step 5: Review OOMKill history**

```bash
kubectl get events --field-selector reason=OOMKilling,involvedObject.name=<pod-name>
```

If OOMKilled events exist, increase memory limits incrementally and monitor.

#### Verification

Resource usage stays within acceptable ranges:

```bash
kubectl top pod <pod-name>
```

No OOMKilled events or throttling warnings in pod events.
Health endpoints respond within 500ms.

## Log Analysis Guide

### Log Architecture

The runner process tails `splunkd.log` to stderr automatically, making all Splunk daemon output visible via standard pod logging mechanisms.

**Code reference:** [runner.go:353-356][runner-go-tail]

Logs from hostPath volumes mounted into the container are processed by the Splunk daemon and forwarded according to Splunk configuration.
These logs are not stored within the container filesystem.

### Startup Sequence

A successful startup follows this 7-step sequence (from [Architecture documentation][architecture-docs]):

1. Generate admin credentials
2. Configure Splunk API
3. Validate license acceptance
4. Start HTTP server on port 8090
5. Start Splunk daemon
6. Start log tail
7. Begin processing logs from hostPath mounts

**Example successful startup logs:**

```text
Generating admin password...
Generated password: aB3xY9Zq
Enabling Splunk API...
splunk license agreement has been accepted
Starting HTTP server on :8090
Starting Splunk daemon...
Starting log tail...
INFO  loader - Splunk Enterprise v10.2.0 build d749cb17ea65 starting...
INFO  loader - Module system initialized
INFO  TcpInputProc - Creating fwd data Acceptor for IPv4 port 9997
```

### Common Log Patterns

#### License Failure

```text
you must accept the terms of the Splunk licensing agreement before using this software.
set the variable SPLUNK_ACCEPT_LICENSE to 'yes' to signal your acceptance of the licensing terms
```

**Action:** Set `SPLUNK_ACCEPT_LICENSE=yes` environment variable.

#### Component Unhealthy

```text
WARN  HealthReporter - Component IndexProcessor transitioning to yellow state
ERROR HealthReporter - Component IndexProcessor transitioning to red state: disk space below threshold
```

**Action:** Investigate specific component (disk space, configuration, permissions).

#### Auto-Restart After Crash

```text
splunkd exited, restarting in 5 seconds
Starting Splunk daemon...
INFO  loader - Splunk Enterprise v10.2.0 build d749cb17ea65 starting...
```

**Action:** Determine why splunkd is exiting (check earlier logs for crash reason, review pod events for OOMKilled).

#### OOMKilled Indicator

When the pod is OOMKilled, logs end abruptly without graceful shutdown messages.
Use pod events to confirm:

```bash
kubectl get events --field-selector involvedObject.name=<pod-name>,reason=OOMKilling
```

#### Graceful Shutdown (Expected)

```text
INFO  ShutdownHandler - Shutting down splunkd
INFO  ServerConfig - Shutdown complete
```

This is normal during pod termination or rolling updates.

## Debugging Techniques

### Using Verbose Health Checks

The `/healthz` endpoint supports a `?verbose` query parameter that shows per-component health status:

```bash
kubectl exec <pod-name> -- curl -s http://localhost:8090/healthz?verbose
```

**Healthy output:**

```text
[+]BatchReader ok
[+]BundlesReplication ok
[+]DispatchManager ok
[+]DispatchManager/SearchScheduler ok
[+]IndexProcessor ok
[+]TailReader ok
```

**Unhealthy output:**

```text
[+]BatchReader ok
[+]BundlesReplication ok
[+]DispatchManager ok
[+]DispatchManager/SearchScheduler not ok
[+]IndexProcessor ok
[+]TailReader ok
```

Use this to identify exactly which component is causing readiness probe failures.

**Code reference:** [runner.go:281-285][runner-go-healthz-verbose]

### Checking Pod Events

Events provide critical diagnostic information:

```bash
kubectl get events --field-selector involvedObject.name=<pod-name> --sort-by='.lastTimestamp'
```

**Common events to look for:**

* `FailedScheduling` - Node resource constraints
* `Unhealthy` - Liveness or readiness probe failures
* `OOMKilling` - Memory limit exceeded
* `BackOff` - CrashLoopBackOff restart delays
* `Pulled` / `Failed` - Image pull issues

**Filter to specific event types:**

```bash
kubectl get events --field-selector involvedObject.name=<pod-name>,reason=Unhealthy
```

### Verifying Resource Constraints

Check current resource allocation and usage:

```bash
kubectl describe pod <pod-name> | grep -A 10 "Limits:"
kubectl top pod <pod-name>
```

**Example:**

```text
Limits:
  cpu:     500m
  memory:  512Mi
Requests:
  cpu:        100m
  memory:     256Mi
```

```text
NAME                               CPU(cores)   MEMORY(bytes)
splunk-forwarder-6d8f4c9b7-x7k2q   120m         280Mi
```

If usage approaches limits, consider increasing resource allocation.

### Testing Endpoints from Inside Pod

Test all three HTTP endpoints to verify runner functionality:

**Liveness probe:**

```bash
kubectl exec <pod-name> -- curl -i http://localhost:8090/livez
```

**Expected:** `200 OK` with body `ok` when splunkd process is running.

**Readiness probe:**

```bash
kubectl exec <pod-name> -- curl -i http://localhost:8090/healthz
```

**Expected:** `200 OK` with body `ok` when all components healthy.

**Metrics:**

```bash
kubectl exec <pod-name> -- curl -s http://localhost:8090/metrics | head -20
```

**Expected:** Prometheus-formatted metrics showing component health (0=healthy, 1=unhealthy).

### Inspecting Splunk Configuration

The runner auto-generates configuration files during startup.
Verify they exist and contain correct settings:

**Check user-seed.conf (admin credentials):**

```bash
kubectl exec <pod-name> -- cat /opt/splunkforwarder/etc/system/local/user-seed.conf
```

**Expected format:**

```ini
[user_info]
USERNAME = admin
PASSWORD = <8-char-random>
```

**Check server.conf (API configuration):**

```bash
kubectl exec <pod-name> -- cat /opt/splunkforwarder/etc/system/local/server.conf
```

**Expected format:**

```ini
[sslConfig]
enableSplunkdSSL = false
[httpServer]
mgmtMode = tcp
acceptFrom = 127.0.0.1/8
```

These files are created by [generateUserSeed()][runner-go-generateUserSeed] and [enableSplunkAPI()][runner-go-enableSplunkAPI] functions.

### Accessing Splunk Health API Directly

The Splunk health API is available on localhost:8089 within the container.
Retrieve admin credentials from logs and query directly:

**Get admin password:**

```bash
kubectl logs <pod-name> | grep "Generated password"
```

**Query health API:**

```bash
kubectl exec <pod-name> -- curl -s -u admin:<password> \
  'http://127.0.0.1:8089/services/server/health/splunkd/details?output_mode=json' \
  | jq .
```

This returns the raw Splunk health JSON that the runner processes to determine component status.

## Getting Support

### Before Requesting Support

Gather the following diagnostic information:

**1. Pod description:**

```bash
kubectl describe pod <pod-name> > pod-description.txt
```

**2. Pod logs:**

```bash
kubectl logs <pod-name> --previous > pod-logs-previous.txt
kubectl logs <pod-name> > pod-logs-current.txt
```

**3. Pod events:**

```bash
kubectl get events --field-selector involvedObject.name=<pod-name> > pod-events.txt
```

**4. Deployment/StatefulSet YAML:**

```bash
kubectl get deployment splunk-forwarder -o yaml > deployment.yaml
```

**5. Verbose health check output:**

```bash
kubectl exec <pod-name> -- curl -s http://localhost:8090/healthz?verbose > healthz-verbose.txt
```

### Reporting Issues

**GitHub Issues:** [openshift/splunk-forwarder-images/issues][github-issues]

**Include in issue report:**

* Symptom and expected behavior
* Steps to reproduce
* Diagnostic files collected above
* Kubernetes version (`kubectl version`)
* Container image tag used
* Any custom configuration or environment variables

### Maintainer Contact

This repository is maintained by Red Hat SRE teams.
See [OWNERS file][owners] for current maintainers and approval groups:

* srep-functional-team-security
* srep-functional-team-rocket
* srep-functional-team-fedramp

**Internal Red Hat users:**

Reach out via internal SRE channels for urgent production issues.

### Related Documentation

* [Architecture documentation][architecture-docs] - Runner design, health check implementation
* [Testing guide][testing-docs] - Validating builds, endpoint testing, CI/CD pipelines
* [README][readme] - Quick start, configuration, versioning
* [Splunk Documentation][splunk-docs] - Universal Forwarder configuration and troubleshooting

[architecture-docs]: ARCHITECTURE.md
[github-issues]: https://github.com/openshift/splunk-forwarder-images/issues
[owners]: ../OWNERS
[readme]: ../README.md
[runner-go-enableSplunkAPI]: ../runner.go#L170-L183
[runner-go-generateUserSeed]: ../runner.go#L154-L168
[runner-go-healthz]: ../runner.go#L269-L287
[runner-go-healthz-verbose]: ../runner.go#L281-L285
[runner-go-license]: ../runner.go#L329-L334
[runner-go-metrics]: ../runner.go#L240-L255
[runner-go-splunk-start]: ../runner.go#L344-L349
[runner-go-tail]: ../runner.go#L353-L356
[splunk-docs]: https://docs.splunk.com/Documentation/Forwarder/latest/Forwarder/Abouttheuniversalforwarder
[testing-docs]: TESTING.md
