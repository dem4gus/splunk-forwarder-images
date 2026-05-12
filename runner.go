// Package main implements the main container process for Splunk Universal Forwarder containers.
//
// The runner manages the complete lifecycle of the Splunk daemon as a child process, including:
//   - Auto-generation of admin credentials on first start
//   - Configuration of the Splunk management API
//   - Health check endpoint exposure for Kubernetes probes
//   - Prometheus metrics for monitoring component health
//   - Automatic restart of the Splunk daemon on failure
//   - Log aggregation by tailing splunkd.log to stderr
//
// HTTP endpoints are exposed on port 8090:
//   - /metrics: Prometheus format metrics showing component health (0=healthy, 1=unhealthy)
//   - /livez: Liveness probe checking if splunkd process is running
//   - /healthz: Readiness probe querying Splunk's health API (supports ?verbose parameter)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	// healthEndpoint is the Splunk API path for retrieving detailed health status.
	healthEndpoint = "/services/server/health/splunkd/details"

	// HTTP response bodies for health checks.
	notOk = "not ok\n"
	ok    = "ok\n"

	// Splunk configuration file paths use $SPLUNK_HOME environment variable expansion.
	serverConfigPath = "${SPLUNK_HOME}/etc/system/local/server.conf"
	splunkCACert     = "${SPLUNK_HOME}/etc/auth/cacert.pem"
	splunkPasswdPath = "${SPLUNK_HOME}/etc/passwd"
	userSeedPath     = "${SPLUNK_HOME}/etc/system/local/user-seed.conf"
	splunkdLogPath   = "${SPLUNK_HOME}/var/log/splunk/splunkd.log"

	// Splunk binary path and command-line flags.
	splunkPath              = "${SPLUNK_HOME}/bin/splunk"
	splunkFlagAcceptLicense = "--accept-license"

	// Splunk API configuration: management API listens on localhost only for security.
	splunkHost = "127.0.0.1:8089"
	splunkUser = "admin"

	// splunkLicenseEnv is the environment variable for license acceptance validation.
	splunkLicenseEnv = "SPLUNK_ACCEPT_LICENSE"

	// serverConfigContent is written to server.conf to configure the management API.
	// SSL is disabled for simplicity and performance within the container.
	// TCP mode is used for the HTTP server.
	// API access is restricted to localhost only (127.0.0.1/8) for security.
	serverConfigContent = `[sslConfig]
enableSplunkdSSL = false
[httpServer]
mgmtMode = tcp
acceptFrom = 127.0.0.1/8
`
)

// Status represents the health state of a Splunk component.
// Health can be "green" (healthy), "yellow" (degraded), or "red" (failed).
// When unhealthy, the Reasons field provides diagnostic information about the failure.
type Status struct {
	Health  string
	Reasons *struct {
		Red struct {
			Primary struct {
				Indicator string
				Reason    string
			} `json:"1"`
		}
	} `json:"reasons,omitempty"`
}

// Healthy returns true if this component's health status is "green".
func (s Status) Healthy() bool {
	return s.Health == "green"
}

// Feature represents a Splunk component that can contain nested sub-components.
// The Features map creates a hierarchy (e.g., "Dispatch Manager" → "Search Scheduler").
type Feature struct {
	Status
	Features map[string]Feature `json:"features,omitempty"`
}

// Flatten converts the nested feature tree into a flat map of component paths to statuses.
// Component names are sanitized by removing spaces and hyphens, then joined with slashes.
// For example, "Dispatch Manager" → "Search Scheduler" becomes "DispatchManager/SearchScheduler".
// This flattened format is used for Prometheus metric labels.
func (s Feature) Flatten(prefix ...string) map[string]Status {
	out := map[string]Status{}
	for k, v := range s.Features {
		k = strings.ReplaceAll(k, " ", "")
		k = strings.ReplaceAll(k, "-", "")
		out[strings.Join(append(prefix, k), "/")] = v.Status
		for k2, v2 := range v.Flatten(append(prefix, k)...) {
			out[k2] = v2
		}
	}
	return out
}

// SplunkHealth is the top-level health status returned by Splunk's health API.
type SplunkHealth Feature

// Flatten converts the top-level Splunk health structure to a flat map of component statuses.
func (s SplunkHealth) Flatten() map[string]Status {
	return (Feature)(s).Flatten()
}

// healthURL is the URL for the Splunk health API endpoint with JSON output mode.
// The User field is populated with admin credentials after password generation.
var healthURL = &url.URL{
	Scheme:   "http",
	Host:     splunkHost,
	Path:     healthEndpoint,
	RawQuery: url.Values{"output_mode": []string{"json"}}.Encode(),
}

// genPasswd generates a random 8-character password for the Splunk admin user.
// It uses Splunk's built-in gen-random-passwd command and removes any existing passwd file.
// The password is logged to stdout and configured in the healthURL for API authentication.
// Returns the password bytes and any error encountered.
func genPasswd() ([]byte, error) {
	os.Remove(os.ExpandEnv(splunkPasswdPath))
	passwd := new(bytes.Buffer)
	if output, err := exec.Command(os.ExpandEnv(splunkPath), "gen-random-passwd").Output(); err != nil {
		log.Fatal(err)
		return nil, err
	} else {
		passwd.Write(output[:8])
	}

	log.Println(passwd.String())
	healthURL.User = url.UserPassword(splunkUser, passwd.String())
	return passwd.Bytes(), nil
}

// generateUserSeed creates the user-seed.conf file that Splunk reads during first startup.
// The file contains the admin username and auto-generated password.
// Splunk will create the admin account based on this seed file.
// Returns any error encountered during file creation or password generation.
func generateUserSeed() error {
	if seedFile, err := os.OpenFile(os.ExpandEnv(userSeedPath), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644); err != nil {
		return err
	} else if passwd, err := genPasswd(); err != nil {
		return err
	} else {
		defer seedFile.Close()
		_, err = fmt.Fprintf(seedFile, "[user_info]\nUSERNAME = %s\nPASSWORD = %s\n", splunkUser, string(passwd))
		return err
	}
}

// enableSplunkAPI writes the server.conf file to configure Splunk's management API.
// This disables SSL for performance, sets TCP mode, and restricts API access to localhost only.
// The configuration must be in place before Splunk starts.
// Returns any error encountered during file creation or writing.
func enableSplunkAPI() error {
	if serverConfigFile, err := os.OpenFile(os.ExpandEnv(serverConfigPath), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644); err != nil {
		return err
	} else {
		defer serverConfigFile.Close()
		_, err = fmt.Fprintf(serverConfigFile, serverConfigContent)

		return err
	}
}

// cmd is the global reference to the running Splunk daemon process.
// Used by the liveness probe to check if the process has exited.
var cmd *exec.Cmd

// RunSplunk starts the Splunk daemon in foreground mode (--nodaemon).
// It accepts the license, answers all prompts with "yes", and pipes output to stderr.
// Returns true if the daemon should restart (context not cancelled), false otherwise.
// This allows the caller to implement auto-restart with a delay.
func RunSplunk(ctx context.Context) bool {
	args := []string{"start", splunkFlagAcceptLicense, "--answer-yes", "--nodaemon"}
	args = append(args, os.Args[1:]...)
	cmd = exec.CommandContext(ctx, os.ExpandEnv(splunkPath), args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	_ = cmd.Start()
	_ = cmd.Wait()
	return ctx.Err() == nil
}

// TailFile continuously follows the splunkd.log file and outputs to stderr.
// This provides log visibility without requiring users to exec into the container.
// Returns true if tail should restart (context not cancelled), false otherwise.
func TailFile(ctx context.Context) bool {
	args := []string{"-F", os.ExpandEnv(splunkdLogPath)}
	tail := exec.CommandContext(ctx, "/usr/bin/tail", args...)
	tail.Stdout = os.Stderr
	tail.Stderr = os.Stderr
	_ = tail.Start()
	_ = tail.Wait()
	return ctx.Err() == nil
}

// StartServer initializes and starts the HTTP server on port 8090.
// It registers three endpoints:
//   - /metrics: Prometheus metrics showing component health (0=healthy, 1=unhealthy)
//   - /livez: Liveness probe checking if splunkd process is running
//   - /healthz: Readiness probe querying Splunk's health API (supports ?verbose)
//
// This function blocks and never returns.
func StartServer() {
	health := &SplunkHealth{}

	gaugeVec := *prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "splunk_forwarder",
		Subsystem: "component",
		Name:      "unhealthy",
	}, []string{"component"})

	reg := prometheus.NewRegistry()
	reg.MustRegister(gaugeVec)

	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		Registry: reg,
	})

	// /metrics: Query Splunk health API and convert component statuses to Prometheus metrics.
	// Metric values: 0 = healthy, 1 = unhealthy.
	http.Handle("/metrics", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if health.Check() {
			gaugeVec.Reset()
		}
		for k, v := range health.Flatten() {
			gauge := gaugeVec.WithLabelValues(k)
			if v.Healthy() {
				gauge.Set(0)
			} else {
				gauge.Set(1)
			}
		}
		handler.ServeHTTP(w, r)
	}))

	// /livez: Liveness check verifying the splunkd process is still running.
	// Returns 500 if process has exited, indicating container should restart.
	http.Handle("/livez", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(notOk))
		} else {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(ok))
		}
	}))

	// /healthz: Readiness check querying Splunk's health API.
	// Returns 200 only if all components report green health status.
	// Optional ?verbose parameter shows per-component health status.
	http.Handle("/healthz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if health.Check() {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(ok))
		} else {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(notOk))
		}
		nok := map[bool]string{false: "not ok", true: "ok"}
		if r.URL.Query().Has("verbose") {
			for k, v := range health.Flatten() {
				w.Write([]byte("\n[+]" + k + " " + nok[v.Healthy()]))
			}
			w.Write([]byte("\n"))
		}
	}))

	http.ListenAndServe("0.0.0.0:8090", nil)
}

// Check queries the Splunk health API and updates this SplunkHealth instance.
// It makes an HTTP GET request to the health endpoint with admin credentials.
// Returns true if all components report "green" health status, false otherwise.
func (h *SplunkHealth) Check() bool {
	res, err := http.Get(healthURL.String())
	if err != nil {
		log.Println("health endpoint request failed: ", err.Error())
		return false
	}
	obj := struct {
		Entry []struct{ Content *SplunkHealth }
	}{}
	if err := json.NewDecoder(res.Body).Decode(&obj); err != nil {
		log.Println("failed parsing health endpoint response: ", err.Error())
		return false
	}
	for i := range obj.Entry {
		if obj.Entry[i].Content != nil {
			*h = *(obj.Entry[i].Content)
			return h.Healthy()
		}
	}
	return false
}

func main() {
	// Step 1: Generate admin user credentials (required before Splunk starts).
	if err := generateUserSeed(); err != nil {
		log.Fatal("couldn't generate admin user seed: ", err.Error())
	}

	// Step 2: Configure Splunk API (disable SSL, localhost-only access).
	if err := enableSplunkAPI(); err != nil {
		log.Fatal("couldn't enable splunk api: ", err.Error())
	}

	// Step 3: Validate that user has accepted Splunk license agreement.
	if os.Getenv(splunkLicenseEnv) == "yes" {
		log.Println("splunk license agreement has been accepted")
	} else {
		log.Println("you must accept the terms of the Splunk licensing agreement before using this software.")
		log.Fatalf("set the variable %s to 'yes' to signal your acceptance of the licensing terms", splunkLicenseEnv)
	}

	// Step 4: Set up signal handling for graceful shutdown (SIGINT, SIGTERM).
	ctx, _ := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

	// Step 5: Start HTTP server for health checks and metrics (non-blocking).
	go StartServer()

	// Step 6: Start Splunk daemon with auto-restart on failure (non-blocking).
	// Restarts every 5 seconds if the daemon exits unexpectedly.
	go func() {
		for RunSplunk(ctx) {
			log.Println("splunkd exited, restarting in 5 seconds")
			time.Sleep(time.Second * 5)
		}
	}()

	// Step 7: Tail Splunk logs to stderr with auto-restart (blocks main goroutine).
	// This provides log visibility and keeps the main process running.
	for TailFile(ctx) {
		log.Println("tail exited, restarting in 5 seconds")
		time.Sleep(time.Second * 5)
	}
}
