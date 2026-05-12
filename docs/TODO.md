# Documentation TODO

This file tracks documentation work that needs to be completed for the splunk-forwarder-images repository.

## Completed

* [x] Create docs/ directory
* [x] Enhance README.md with comprehensive documentation
* [x] Add godoc comments to runner.go
* [x] Create docs/ARCHITECTURE.md
* [x] Create docs/TESTING.md

## Pending

### Medium Priority

* [x] **docs/TESTING.md**
  * Local testing procedures
  * Build image: `make build`
  * Run container locally with SPLUNK_ACCEPT_LICENSE
  * Test all three HTTP endpoints with curl examples
  * Testing Splunk daemon (verify startup, get credentials, test auto-restart)
  * Vulnerability scanning: `make vuln-check`
  * CI/CD pipeline testing (PR triggers, common failures)
  * Manual integration testing (deploy to Kubernetes, verify health checks)
  * Note about future unit tests (contributions welcome)

* [ ] **docs/TROUBLESHOOTING.md**
  * Common issues and solutions:
    * Container exits immediately (SPLUNK_ACCEPT_LICENSE not set)
    * Health checks failing (debug with ?verbose mode)
    * Splunk daemon crashes/restarts (log analysis)
    * Metrics not showing up (Prometheus scraping issues)
    * Performance problems (resource constraints)
  * Log analysis guide (where to find logs, what to look for)
  * Getting support (GitHub issues, maintainer contact from OWNERS file)

### Low Priority

* [ ] **docs/DEPLOYMENT.md**
  * Note: Deployment is handled by openshift/splunk-forwarder-operator
  * Link to operator repository
  * Example Kubernetes Deployment YAML for testing/development
  * Health probe configuration best practices
  * Resource requests/limits recommendations
  * Prometheus ServiceMonitor example for metrics scraping
  * Example Prometheus alerting rules for unhealthy components

## Notes

* All documentation should use reference-style links at bottom of documents
* Code examples should be tested and working
* Maintain consistency with existing documentation style
* Follow markdownlint rules (unordered lists use `*`, semantic line breaks)
* Target audience is familiar with Kubernetes, containers, and Go development
