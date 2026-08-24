package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// examplePath is the config file shipped with the release. It is the one every
// installation starts from, so its behaviour is part of the product.
func examplePath() string { return filepath.Join("..", "..", "alertloop.example.yaml") }

// The 0.3.0 defect, as a test: a production deployment must not come up on a
// credential published in this repository. The example references
// ${ALERTLOOP_ADMIN_TOKEN} with no fallback, so an unset token stops startup
// with a message naming it.
func TestExampleConfigRefusesWithoutAnAdminToken(t *testing.T) {
	t.Setenv("ALERTLOOP_ADMIN_TOKEN", "")

	_, err := Load(examplePath())
	if err == nil {
		t.Fatal("the example config loaded with no admin token set; a placeholder credential has crept back in")
	}
	if !strings.Contains(err.Error(), "ALERTLOOP_ADMIN_TOKEN") {
		t.Fatalf("error does not name the missing variable, so nobody will know what to set: %v", err)
	}
}

func TestExampleConfigLoadsWithAnAdminToken(t *testing.T) {
	t.Setenv("ALERTLOOP_ADMIN_TOKEN", "a-real-token")

	cfg, err := Load(examplePath())
	if err != nil {
		t.Fatalf("the shipped example config does not load: %v", err)
	}
	if cfg.AdminToken != "a-real-token" {
		t.Fatalf("admin_token = %q, want the value from the environment", cfg.AdminToken)
	}
	if cfg.Database.Driver != "sqlite" {
		t.Fatalf("database.driver = %q, want the sqlite default", cfg.Database.Driver)
	}
	if cfg.RetentionDays != 30 {
		t.Fatalf("retention_days = %d, want 30", cfg.RetentionDays)
	}
}

// The same example file serves a binary install and the Compose postgres
// profile: the profile selects the driver and DSN through the environment
// rather than shipping a second, divergent copy of this file. If these
// references stop working, that consolidation silently breaks and the profile
// runs on SQLite inside a container while its operator is certain it is on
// PostgreSQL.
func TestExampleConfigTakesTheDatabaseFromTheEnvironment(t *testing.T) {
	const dsn = "postgres://alertloop:pw@postgres:5432/alertloop?sslmode=disable"
	t.Setenv("ALERTLOOP_ADMIN_TOKEN", "a-real-token")
	t.Setenv("ALERTLOOP_DB_DRIVER", "postgres")
	t.Setenv("ALERTLOOP_DB_DSN", dsn)

	cfg, err := Load(examplePath())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Database.Driver != "postgres" {
		t.Fatalf("database.driver = %q, want postgres", cfg.Database.Driver)
	}
	if cfg.Database.DSN != dsn {
		t.Fatalf("database.dsn = %q, want the DSN from the environment", cfg.Database.DSN)
	}
}

// notify_on_resolve is a pointer so an absent key can be told from an explicit
// false. With a plain bool the zero value would silently disable recovery
// notices for every config file written before 0.4.0 — the exact class of
// regression that makes an upgrade quietly worse.
func TestNotifyOnResolveDefaultsToOn(t *testing.T) {
	t.Setenv("ALERTLOOP_ADMIN_TOKEN", "a-real-token")

	cfg, err := Load(examplePath())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.ShouldNotifyOnResolve() {
		t.Fatal("the shipped example disables recovery notices")
	}

	// A config that never heard of the setting still gets it.
	if !Default().ShouldNotifyOnResolve() {
		t.Fatal("an absent notify_on_resolve must mean on")
	}

	off := false
	if (Config{NotifyOnResolve: &off}).ShouldNotifyOnResolve() {
		t.Fatal("an explicit false was ignored")
	}
	on := true
	if !(Config{NotifyOnResolve: &on}).ShouldNotifyOnResolve() {
		t.Fatal("an explicit true was ignored")
	}
}
