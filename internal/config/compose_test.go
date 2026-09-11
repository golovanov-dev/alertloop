package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// docker-compose.yml is not Go, but it is what most installations run, and a
// mistake in it fails nobody's build — only somebody's install. These tests
// read the shipped file.

type composeService struct {
	Image       string            `yaml:"image"`
	Command     []string          `yaml:"command"`
	Ports       []string          `yaml:"ports"`
	Environment map[string]string `yaml:"environment"`
	EnvFile     any               `yaml:"env_file"`
	Healthcheck *struct {
		Test []string `yaml:"test"`
	} `yaml:"healthcheck"`
	DependsOn map[string]struct {
		Condition string `yaml:"condition"`
	} `yaml:"depends_on"`
}

func loadCompose(t *testing.T) map[string]composeService {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}
	var doc struct {
		Services map[string]composeService `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse docker-compose.yml: %v", err)
	}
	if len(doc.Services) == 0 {
		t.Fatal("docker-compose.yml has no services")
	}
	return doc.Services
}

// alertloopServices are the services that run the AlertLoop image.
var alertloopServices = []string{"alertloop", "api", "worker"}

// A busy port 8080 used to have no remedy but an override file with a tag that
// older Compose does not know. The host side of the mapping is a variable now,
// in both services that publish it, and still bound to loopback only.
func TestComposePublishesAConfigurableLoopbackPort(t *testing.T) {
	services := loadCompose(t)
	const want = "127.0.0.1:${ALERTLOOP_PORT:-8080}:8080"
	for _, name := range []string{"alertloop", "api"} {
		ports := services[name].Ports
		if len(ports) != 1 || ports[0] != want {
			t.Errorf("service %s publishes %v, want exactly [%q]", name, ports, want)
		}
	}
	if ports := services["worker"].Ports; len(ports) != 0 {
		t.Errorf("the worker publishes %v; it serves no HTTP and must not claim a port", ports)
	}
}

// ALERTLOOP_PORT is for Compose alone. Handed to the container it would look
// like configuration, and since 0.3.0 the environment configures nothing the
// config file does not ask for — so it must not be passed in, and AlertLoop
// must not trip over it where it is set anyway (a binary install whose shell
// exports it, say).
func TestAlertLoopPortStaysOutOfTheContainers(t *testing.T) {
	for name, svc := range loadCompose(t) {
		if svc.EnvFile != nil {
			t.Errorf("service %s has env_file: every variable in .env, ALERTLOOP_PORT included, would reach the container", name)
		}
		for k, v := range svc.Environment {
			if k == "ALERTLOOP_PORT" || strings.Contains(v, "ALERTLOOP_PORT") {
				t.Errorf("service %s passes ALERTLOOP_PORT into the container (%s: %s)", name, k, v)
			}
		}
	}
	if _, ok := legacyEnvVars["ALERTLOOP_PORT"]; ok {
		t.Fatal("ALERTLOOP_PORT is listed as a legacy variable; setting it for Compose would stop AlertLoop")
	}

	t.Setenv("ALERTLOOP_ADMIN_TOKEN", "a-real-token")
	t.Setenv("ALERTLOOP_PORT", "8090")
	cfg, err := Load(examplePath())
	if err != nil {
		t.Fatalf("the example config does not load with ALERTLOOP_PORT set: %v", err)
	}
	if len(cfg.Warnings) != 0 {
		t.Fatalf("ALERTLOOP_PORT produced startup warnings: %v", cfg.Warnings)
	}
}

// A failed start must be visible outside the logs. `docker compose up -d`
// succeeds as soon as the containers exist, so a process restarting in a loop
// looks like a running one. Every AlertLoop container carries a health check
// so that `up --wait` and `docker compose ps` can tell.
func TestEveryAlertLoopServiceHasAHealthCheck(t *testing.T) {
	services := loadCompose(t)
	for _, name := range alertloopServices {
		hc := services[name].Healthcheck
		if hc == nil || len(hc.Test) == 0 {
			t.Errorf("service %s has no healthcheck; a failed start would look like a running service", name)
		}
	}

	// The HTTP services probe readiness, which includes the database.
	for _, name := range []string{"alertloop", "api"} {
		if hc := services[name].Healthcheck; hc != nil && !strings.Contains(strings.Join(hc.Test, " "), "/health/ready") {
			t.Errorf("service %s checks %v; want /health/ready, which fails when the database does", name, hc.Test)
		}
	}

	// The worker has no listener: an HTTP probe (which is what the image's own
	// HEALTHCHECK is) would mark it unhealthy forever. It runs check-db, with
	// the config file its command runs on.
	worker := services["worker"]
	if hc := worker.Healthcheck; hc != nil {
		test := strings.Join(hc.Test, " ")
		if !strings.Contains(test, "check-db") {
			t.Errorf("the worker's healthcheck is %v; want the check-db subcommand", hc.Test)
		}
		if strings.Contains(test, "http") {
			t.Errorf("the worker's healthcheck probes HTTP (%v), and the worker serves none", hc.Test)
		}
		cfgFlag := func(args []string) string {
			for i := 0; i+1 < len(args); i++ {
				if args[i] == "--config" {
					return args[i+1]
				}
			}
			return ""
		}
		if got, want := cfgFlag(hc.Test), cfgFlag(worker.Command); got != want {
			t.Errorf("the healthcheck reads config %q but the worker runs on %q", got, want)
		}
	}

	// Started, not healthy: a dependency on the api's health makes a plain
	// `docker compose up -d` (no --wait-timeout) wait forever when the api
	// restarts in a loop — trading a silent failure for a hang.
	if c := worker.DependsOn["api"].Condition; c != "service_started" {
		t.Errorf("worker depends on api with condition %q, want service_started", c)
	}
}

// changelogTopVersions returns the version of the topmost CHANGELOG section and
// of the newest released (dated) one. They are the same unless the top section
// is still Unreleased.
func changelogTopVersions(t *testing.T) (top, released string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	if err != nil {
		t.Fatalf("read CHANGELOG.md: %v", err)
	}
	heading := regexp.MustCompile(`(?m)^## (\d+\.\d+\.\d+) - (\S+)`)
	for _, m := range heading.FindAllStringSubmatch(string(data), -1) {
		if top == "" {
			top = m[1]
		}
		if regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(m[2]) {
			released = m[1]
			break
		}
	}
	if top == "" || released == "" {
		t.Fatalf("CHANGELOG.md: no version sections found (top %q, released %q)", top, released)
	}
	return top, released
}

// .env.example suggested pinning the image to v0.4.0 two releases after a
// vulnerability was fixed in 0.4.1 — the file people copy pointed at the one
// version nobody should run. Tied to the CHANGELOG, it cannot fall behind by
// more than the release in progress: while the top section is Unreleased the
// example may name it or the last release; once the top section has a date,
// only that version passes, so the release commit has to carry it.
func TestEnvExamplePinsTheCurrentImage(t *testing.T) {
	data, err := os.ReadFile(envExamplePath())
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`ALERTLOOP_IMAGE=ghcr\.io/golovanov-dev/alertloop:v(\d+\.\d+\.\d+)`).FindStringSubmatch(string(data))
	if m == nil {
		t.Fatal(".env.example no longer shows how to pin ALERTLOOP_IMAGE to a version")
	}
	top, released := changelogTopVersions(t)
	if m[1] != top && m[1] != released {
		t.Fatalf(".env.example pins v%s; the CHANGELOG is at %s (last release %s). Update the example.", m[1], top, released)
	}
}

// `openssl rand -base64 32` produces "/", which broke the URL DSN the compose
// file used to build. The DSN no longer cares, but a password generated for a
// URL elsewhere — a binary install, a managed database — still would, and hex
// is safe everywhere. No shipped file recommends base64 for a secret.
func TestShippedFilesDoNotSuggestBase64Secrets(t *testing.T) {
	for _, name := range []string{".env.example", "README.md", "OPERATIONS.md", "alertloop.example.yaml", "docker-compose.yml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(data), "rand -base64") {
			t.Errorf("%s suggests `openssl rand -base64`; use `openssl rand -hex 32`", name)
		}
	}
}
