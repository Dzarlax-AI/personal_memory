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

func TestMaintenanceScopeDefaultsAndCatalogLimits(t *testing.T) {
	c := Off()
	c.Defaults()
	if c.Maintenance.Scope != "facts_and_catalog" || c.Maintenance.ProjectPolicy != "explicit" || c.Maintenance.MaxProjectsPerRun != 4 || c.Maintenance.Enabled {
		t.Fatalf("unexpected compatibility defaults: %#v", c.Maintenance)
	}
	c.CatalogDir, c.StateDir = "catalog", "state"
	c.Maintenance = Maintenance{Enabled: true, Profile: "m", IntervalSeconds: 60, MaxFactsPerRun: 50, Scope: "catalog_only", MaxProjectsPerRun: 32, CatalogPublish: "review", GroupingApply: "manual"}
	c.Profiles = map[string]Profile{"m": {Protocol: "openai-compatible-chat", Model: "operator/custom", Endpoint: "http://localhost:11434/v1", Local: true}}
	c.Limits = Limits{DailyCalls: 10, DailyInputTokens: 2_000_000, ReservationTokens: 131_072}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Maintenance.MaxProjectsPerRun = 33
	if err := c.Validate(); err == nil {
		t.Fatal("project batch limit above 32 accepted")
	}
}

func TestMaintenanceRegisteredProjectPolicyIsReviewOnly(t *testing.T) {
	c := Off()
	c.CatalogDir, c.StateDir = "catalog", "state"
	c.Maintenance = Maintenance{Enabled: true, Profile: "m", ProjectPolicy: "registered", IntervalSeconds: 60, MaxFactsPerRun: 50, Scope: "catalog_only", MaxProjectsPerRun: 4, CatalogPublish: "review", GroupingApply: "manual"}
	c.Profiles = map[string]Profile{"m": {Protocol: "openai-compatible-chat", Model: "operator/custom", Endpoint: "http://localhost:11434/v1", Local: true}}
	c.Egress = Egress{AllowedNamespaces: []string{"projects"}}
	c.Limits = Limits{DailyCalls: 10, DailyInputTokens: 2_000_000, ReservationTokens: 131_072}
	if err := c.Validate(); err != nil {
		t.Fatalf("registered review-only config rejected: %v", err)
	}
	c.Maintenance.ProjectPolicy = "automatic"
	if err := c.Validate(); err == nil {
		t.Fatal("unknown maintenance project policy accepted")
	}
	for _, combination := range []struct{ scope, publish string }{{"facts_and_catalog", "review"}, {"catalog_only", "evidence_bounded"}} {
		c.Maintenance.ProjectPolicy = "registered"
		c.Maintenance.Scope, c.Maintenance.CatalogPublish = combination.scope, combination.publish
		if err := c.Validate(); err == nil {
			t.Fatalf("unsafe registered maintenance combination accepted: %#v", combination)
		}
	}
	// An omitted policy stays compatible with the previous explicit behavior.
	c.Maintenance.ProjectPolicy = ""
	c.Maintenance.Scope, c.Maintenance.CatalogPublish = "facts_and_catalog", "evidence_bounded"
	if err := c.Validate(); err != nil {
		t.Fatalf("omitted policy lost legacy compatibility: %v", err)
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
			c.Write = WriteFeature{Mode: "on", Provider: "decisions", Profile: "d"}
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

func TestRegisteredProjectEgress(t *testing.T) {
	t.Run("default preserves explicit behavior", func(t *testing.T) {
		c := Off()
		c.Defaults()
		if c.Egress.ProjectPolicy != "explicit" {
			t.Fatalf("project policy default = %q", c.Egress.ProjectPolicy)
		}
		e := Egress{AllowedNamespaces: []string{"projects"}, UnassignedNamespaces: []string{"projects"}}
		registered := map[string]bool{"known": true}
		if !e.AllowsRegistered("projects", nil, registered) || e.AllowsRegistered("projects", []string{"known"}, registered) {
			t.Fatal("empty project policy must retain explicit-only behavior")
		}
	})
	t.Run("unknown policy rejected while AI is off", func(t *testing.T) {
		c := Off()
		c.Egress.ProjectPolicy = "automatic"
		if err := c.Validate(); err == nil {
			t.Fatal("unknown policy accepted")
		}
	})
	t.Run("registered tags and denial precedence", func(t *testing.T) {
		e := Egress{
			AllowedNamespaces:  []string{"projects"},
			AllowedProjectTags: []string{"static"},
			ProjectPolicy:      "registered",
			DeniedProjectTags:  []string{"blocked"},
		}
		registered := map[string]bool{"known": true, "blocked": true}
		if !e.Allows("projects", []string{"static"}) {
			t.Fatal("static explicit grant should remain allowed")
		}
		if e.AllowsRegistered("projects", []string{"unknown"}, registered) {
			t.Fatal("unregistered tag accepted")
		}
		if e.AllowsRegistered("projects", []string{"blocked"}, registered) {
			t.Fatal("denied tag overrode registered grant")
		}
		if !e.AllowsRegistered("projects", []string{"known"}, registered) {
			t.Fatal("registered tag rejected")
		}
	})
	t.Run("every mixed tag must be allowed", func(t *testing.T) {
		e := Egress{AllowedNamespaces: []string{"projects"}, AllowedProjectTags: []string{"static"}, ProjectPolicy: "registered"}
		registered := map[string]bool{"known": true}
		if !e.AllowsRegistered("projects", []string{"static", "known"}, registered) {
			t.Fatal("static and registered grants should compose")
		}
		if e.AllowsRegistered("projects", []string{"static", "unknown"}, registered) {
			t.Fatal("mixed tags with an ungranted tag accepted")
		}
	})
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
			c.Write = WriteFeature{Mode: "on", Provider: "decisions", Profile: "d"}
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

func TestWriteAdmissionConfiguration(t *testing.T) {
	for _, mode := range []string{"", "operator_policy", "client_opt_in", "invalid"} {
		c := Off()
		c.Write.Admission = mode
		err := c.Validate()
		if (err != nil) != (mode == "invalid") {
			t.Fatalf("admission %q: %v", mode, err)
		}
	}
	for _, value := range []string{`"client_opt_in"`, `""`, `null`} {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "config.json")
		raw := `{"schema_version":1,"read":{"mode":"off","admission":` + value + `}}`
		if err = os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = Load(path); err == nil {
			t.Fatal("read admission accepted")
		}
	}
}

func TestScopedPreparationRefusesUnassignedWrites(t *testing.T) {
	raw, err := os.ReadFile("../../docs/examples/optional-ai/decisions-scoped-shadow-preparation.json")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Write.Admission != "client_opt_in" || c.Egress.Allows("projects", nil) || len(c.Egress.AllowedProjectTags) != 0 {
		t.Fatal("preparation configuration permits unapproved unassigned writes")
	}
}

func TestCatalogMaintenanceExampleLoadsDisabledAndReviewOnly(t *testing.T) {
	raw, err := os.ReadFile("../../docs/examples/catalog-maintenance.json")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "catalog-maintenance.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Maintenance.Enabled || c.Maintenance.ProjectPolicy != "registered" || c.Maintenance.Scope != "catalog_only" || c.Maintenance.CatalogPublish != "review" || len(c.Egress.AllowedProjectTags) != 0 {
		t.Fatalf("catalog maintenance example is not disabled registered review mode: %#v", c.Maintenance)
	}
}

func TestWriteShadowTimeoutBoundsAndDefault(t *testing.T) {
	c := Off()
	if c.Write.ShadowTimeout() != time.Second {
		t.Fatal("default changed")
	}
	for _, value := range []int{-1, 30001} {
		c.Write.ShadowTimeoutMS = value
		if c.Validate() == nil {
			t.Fatalf("invalid timeout accepted: %d", value)
		}
	}
	for _, value := range []int{0, 1, 10000, 30000} {
		c.Write.ShadowTimeoutMS = value
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	c.Write.ShadowTimeoutMS = 10000
	if c.Write.ShadowTimeout() != 10*time.Second {
		t.Fatal("configured timeout ignored")
	}
}
