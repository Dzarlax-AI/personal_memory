package aipolicy

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOffDoesNotReadProfile(t *testing.T) {
	c := Off()
	c.Profiles = map[string]Profile{"unused": {KeyFile: "/missing", Endpoint: "bad"}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestEightCombinations(t *testing.T) {
	for mask := 0; mask < 8; mask++ {
		c := Off()
		c.CatalogDir = "catalog"
		c.StateDir = "state"
		c.Defaults()
		c.Profiles = map[string]Profile{"d": {Protocol: "decisions", Model: "gpt-6-luna", Endpoint: "https://api.openai.com/v1/decisions", KeyFile: "/missing"}, "m": {Protocol: "openai-compatible-chat", Model: "operator-chosen", Endpoint: "http://localhost:11434/v1/chat/completions", Local: true}}
		if mask&1 != 0 {
			c.Write = Feature{Mode: "on", Provider: "decisions", Profile: "d"}
		}
		if mask&2 != 0 {
			c.Read = Feature{Mode: "shadow", Provider: "decisions", Profile: "d"}
		}
		if mask&4 != 0 {
			c.Maintenance.Enabled = true
			c.Maintenance.Profile = "m"
			c.Maintenance.IntervalSeconds = 60
			c.Limits.ReservationTokens = 131_072
		}
		if err := c.Validate(); err != nil {
			t.Fatalf("mask%d: %v", mask, err)
		}
	}
}
func TestEgress(t *testing.T) {
	e := Egress{AllowedNamespaces: []string{"projects"}, AllowedProjectTags: []string{"personal-memory"}, UnassignedNamespaces: []string{"projects"}}
	if !e.Allows("projects", nil) || !e.Allows("projects", []string{"personal-memory"}) || e.Allows("projects", []string{"other"}) || e.Allows("personal", nil) {
		t.Fatal("egress scope")
	}
}
func TestBudgetPersistenceUnknownAndCapacity(t *testing.T) {
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lim := Limits{DailyCalls: 2, DailyInputTokens: 131072, ReservationTokens: 65536}
	b, err := NewBudget(d, lim)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r, err := b.Begin(now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Begin(now); err == nil {
		t.Fatal("queue must reject")
	}
	r.Finish(0, false)
	b2, err := NewBudget(d, lim)
	if err != nil {
		t.Fatal(err)
	}
	r, err = b2.Begin(now)
	if err != nil {
		t.Fatal(err)
	}
	r.Finish(10, true)
	if _, err = b2.Begin(now); err == nil {
		t.Fatal("call limit must persist")
	}
	r, err = b2.Begin(now.Add(24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	r.Finish(10, true)
}
func TestPrivateFilesAndSymlinks(t *testing.T) {
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "key")
	os.WriteFile(p, []byte("secret"), 0644)
	if _, err := ReadPrivate(p, 100); err == nil {
		t.Fatal("public file")
	}
	os.Chmod(p, 0600)
	l := filepath.Join(d, "link")
	os.Symlink(p, l)
	if _, err := ReadPrivate(l, 100); err == nil {
		t.Fatal("symlink")
	}
	if err := AtomicWrite(d, "link", []byte("new")); err == nil {
		t.Fatal("symlink write")
	}
}

func TestBudgetCorruptionAndExcessUsageFailClosed(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	limits := Limits{DailyCalls: 10, DailyInputTokens: 1000, ReservationTokens: 100}
	if err := os.WriteFile(filepath.Join(dir, "budget.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewBudget(dir, limits); err == nil {
		t.Fatal("empty existing ledger accepted")
	}
	os.Remove(filepath.Join(dir, "budget.json"))
	b, err := NewBudget(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	r, err := b.Begin(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r.Finish(9223372036854775807, true)
	if _, err := b.Begin(time.Now()); err == nil {
		t.Fatal("excess usage bypassed quota")
	}
	if _, err := NewBudget(dir, limits); err != nil {
		t.Fatal(err)
	}
}

func TestRegistrationIndependentFromInference(t *testing.T) {
	c := Off()
	c.Registration.Mode = "client_declared"
	c.CatalogDir = "registry"
	c.Profiles = map[string]Profile{"unused": {Endpoint: "bad", KeyFile: "/missing"}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Active() || !c.Registration.Active() {
		t.Fatal("registration enabled inference")
	}
	c.CatalogDir = ""
	if err := c.Validate(); err == nil {
		t.Fatal("missing registry path accepted")
	}
	c.CatalogDir = "registry"
	c.Registration.Mode = "on"
	if err := c.Validate(); err == nil {
		t.Fatal("invalid registry mode accepted")
	}
	c = Off()
	c.Defaults()
	if c.Registration.Mode != "off" || c.Maintenance.CatalogPublish != "review" {
		t.Fatal("unsafe defaults")
	}
}

func TestRegistrationAndAllIndependentContours(t *testing.T) {
	for mask := 0; mask < 16; mask++ {
		c := Off()
		c.CatalogDir = "catalog"
		c.StateDir = "state"
		c.Profiles = map[string]Profile{"d": {Protocol: "decisions", Model: "gpt-6-luna", Endpoint: "https://api.openai.com/v1/decisions", KeyFile: "/missing"}, "m": {Protocol: "openai-compatible-chat", Model: "explicit", Endpoint: "http://localhost:11434/v1/chat/completions", Local: true}}
		if mask&1 != 0 {
			c.Registration.Mode = "client_declared"
		}
		if mask&2 != 0 {
			c.Write = Feature{Mode: "on", Provider: "decisions", Profile: "d"}
		}
		if mask&4 != 0 {
			c.Read = Feature{Mode: "shadow", Provider: "decisions", Profile: "d"}
		}
		if mask&8 != 0 {
			c.Maintenance = Maintenance{Enabled: true, Profile: "m", IntervalSeconds: 60, CatalogPublish: "evidence_bounded"}
		}
		c.Defaults()
		if err := c.Validate(); err != nil {
			t.Fatalf("mask %d: %v", mask, err)
		}
	}
}

func TestMaintenanceResponseModelAllowlist(t *testing.T) {
	p := Profile{Model: "alias", ResponseModels: []string{"snapshot"}}
	if !p.AcceptsResponseModel("alias") || !p.AcceptsResponseModel("snapshot") || p.AcceptsResponseModel("unexpected") {
		t.Fatal("response allowlist mismatch")
	}
}
