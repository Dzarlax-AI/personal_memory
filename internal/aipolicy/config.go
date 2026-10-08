// Package aipolicy defines operator-controlled optional inference configuration.
package aipolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
)

type Feature struct {
	Mode     string `json:"mode"`
	Provider string `json:"provider"`
	Profile  string `json:"profile"`
}
type Registration struct {
	Mode string `json:"mode"`
}

func (r Registration) Active() bool { return r.Mode == "client_declared" }

type Maintenance struct {
	Enabled         bool   `json:"enabled"`
	Profile         string `json:"profile"`
	IntervalSeconds int    `json:"interval_seconds"`
	MaxFactsPerRun  int    `json:"max_facts_per_run"`
	CatalogPublish  string `json:"catalog_publish"`
	GroupingApply   string `json:"grouping_apply"`
}
type Profile struct {
	Protocol   string `json:"protocol"`
	Model      string `json:"model"`
	Endpoint   string `json:"endpoint"`
	KeyFile    string `json:"key_file"`
	Local      bool   `json:"local"`
	TimeoutMS  int    `json:"timeout_ms"`
	OutputMode string `json:"output_mode,omitempty"`
}
type Egress struct {
	AllowedNamespaces    []string `json:"allowed_namespaces"`
	AllowedProjectTags   []string `json:"allowed_project_tags"`
	UnassignedNamespaces []string `json:"unassigned_namespaces"`
}
type Limits struct {
	DailyCalls        int64 `json:"daily_calls"`
	DailyInputTokens  int64 `json:"daily_input_tokens"`
	ReservationTokens int64 `json:"reservation_tokens"`
}
type Config struct {
	SchemaVersion int                `json:"schema_version"`
	CatalogDir    string             `json:"catalog_dir"`
	StateDir      string             `json:"state_dir"`
	Registration  Registration       `json:"registration"`
	Maintenance   Maintenance        `json:"maintenance"`
	Write         Feature            `json:"write"`
	Read          Feature            `json:"read"`
	Profiles      map[string]Profile `json:"profiles"`
	Egress        Egress             `json:"egress"`
	Limits        Limits             `json:"limits"`
}

func Off() Config {
	return Config{SchemaVersion: 1, Registration: Registration{Mode: "off"}, Write: Feature{Mode: "off", Provider: "none"}, Read: Feature{Mode: "off", Provider: "none"}}
}
func (f Feature) Active() bool { return f.Mode == "on" || f.Mode == "shadow" }
func (c Config) Active() bool  { return c.Write.Active() || c.Read.Active() || c.Maintenance.Enabled }
func Load(path string) (Config, error) {
	if path == "" {
		return Off(), nil
	}
	b, err := ReadPrivate(path, 1<<20)
	if err != nil {
		return Config{}, errors.New("cannot read optional AI configuration")
	}
	c := Off()
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return Config{}, errors.New("invalid optional AI configuration")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return Config{}, errors.New("invalid optional AI configuration ending")
	}
	c.Defaults()
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}
func (c *Config) Defaults() {
	if c.Registration.Mode == "" {
		c.Registration.Mode = "off"
	}
	if c.Write.Mode == "" {
		c.Write.Mode = "off"
	}
	if c.Read.Mode == "" {
		c.Read.Mode = "off"
	}
	if c.Write.Provider == "" {
		c.Write.Provider = "none"
	}
	if c.Read.Provider == "" {
		c.Read.Provider = "none"
	}
	if c.Maintenance.MaxFactsPerRun == 0 {
		c.Maintenance.MaxFactsPerRun = 50
	}
	if c.Maintenance.CatalogPublish == "" {
		c.Maintenance.CatalogPublish = "review"
	}
	if c.Maintenance.GroupingApply == "" {
		c.Maintenance.GroupingApply = "manual"
	}
	if c.Limits.DailyCalls == 0 {
		c.Limits.DailyCalls = 1000
	}
	if c.Limits.DailyInputTokens == 0 {
		c.Limits.DailyInputTokens = 2_000_000
	}
	if c.Limits.ReservationTokens == 0 {
		c.Limits.ReservationTokens = 65_536
		if c.Maintenance.Enabled {
			c.Limits.ReservationTokens = 131_072
		}
	}
}
func (c Config) Validate() error {
	if c.SchemaVersion != 1 {
		return errors.New("unsupported optional AI configuration schema")
	}
	if c.Registration.Mode != "" && c.Registration.Mode != "off" && c.Registration.Mode != "client_declared" {
		return errors.New("invalid project registration mode")
	}
	if c.Registration.Active() && strings.TrimSpace(c.CatalogDir) == "" {
		return errors.New("project registration requires catalog directory")
	}
	for _, f := range []Feature{c.Write, c.Read} {
		if f.Mode != "off" && f.Mode != "shadow" && f.Mode != "on" {
			return errors.New("invalid optional AI feature mode")
		}
		if !f.Active() {
			continue
		}
		if f.Provider != "decisions" && f.Provider != "jev" {
			return errors.New("active judgment requires decisions or jev")
		}
		p, ok := c.Profiles[f.Profile]
		if !ok || f.Profile == "" {
			return errors.New("active judgment profile missing")
		}
		if p.Protocol != f.Provider {
			return errors.New("judgment profile protocol mismatch")
		}
		if err := p.Validate(); err != nil {
			return err
		}
	}
	if c.Maintenance.Enabled {
		p, ok := c.Profiles[c.Maintenance.Profile]
		if !ok || c.Maintenance.Profile == "" {
			return errors.New("active maintenance profile missing")
		}
		if p.Protocol != "openai-responses" && p.Protocol != "openai-compatible-chat" {
			return errors.New("maintenance requires generative profile")
		}
		if err := p.Validate(); err != nil {
			return err
		}
		if c.Maintenance.IntervalSeconds < 1 || c.Maintenance.IntervalSeconds > 604800 || c.Maintenance.MaxFactsPerRun < 1 || c.Maintenance.MaxFactsPerRun > 100 {
			return errors.New("invalid maintenance interval or batch limit")
		}
		if (c.Maintenance.CatalogPublish != "review" && c.Maintenance.CatalogPublish != "evidence_bounded") || c.Maintenance.GroupingApply != "manual" {
			return errors.New("automatic catalog publication and grouping application unsupported")
		}
	}
	if c.Active() && (c.CatalogDir == "" || c.StateDir == "" || c.CatalogDir == c.StateDir) {
		return errors.New("active AI requires separate catalog and state directories")
	}
	if c.Maintenance.Enabled && c.Limits.ReservationTokens < 131_072 {
		return errors.New("maintenance requires at least 131072 reserved input tokens")
	}
	if c.Active() && (c.Limits.DailyCalls < 1 || c.Limits.DailyInputTokens < 1 || c.Limits.ReservationTokens < 65_536 || c.Limits.ReservationTokens > c.Limits.DailyInputTokens) {
		return errors.New("invalid optional AI budget")
	}
	for _, ns := range append(append([]string{}, c.Egress.AllowedNamespaces...), c.Egress.UnassignedNamespaces...) {
		if !namespace(ns) {
			return errors.New("invalid egress namespace")
		}
	}
	return nil
}
func (p Profile) Validate() error {
	if p.OutputMode != "" && p.OutputMode != "schema" && p.OutputMode != "json" {
		return errors.New("invalid maintenance output mode")
	}
	if p.OutputMode != "" && p.Protocol != "openai-responses" && p.Protocol != "openai-compatible-chat" {
		return errors.New("output mode only applies to maintenance profiles")
	}
	if strings.TrimSpace(p.Model) == "" || len(p.Model) > 255 || strings.ContainsAny(p.Model, "\r\n") {
		return errors.New("inference model must be explicit")
	}
	u, err := url.Parse(p.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid inference endpoint")
	}
	if u.Scheme != "https" && !(p.Local && u.Scheme == "http") {
		return errors.New("inference endpoint requires HTTPS or explicit local HTTP")
	}
	if !p.Local && p.KeyFile == "" {
		return errors.New("hosted inference profile requires key file")
	}
	if p.Protocol == "decisions" && (p.Model != "gpt-6-luna" || p.Endpoint != "https://api.openai.com/v1/decisions" || p.Local) {
		return errors.New("unsupported Decisions profile")
	}
	if p.Protocol == "jev" && (p.Model != "jev-1.13.0" || p.Endpoint != "https://api.typesafe.ai/v1/systemone" || p.Local) {
		return errors.New("Jev requires pinned hosted profile")
	}
	if p.TimeoutMS < 0 || p.TimeoutMS > 60_000 {
		return errors.New("invalid inference timeout")
	}
	return nil
}
func (p Profile) Timeout(fallback time.Duration) time.Duration {
	if p.TimeoutMS > 0 {
		return time.Duration(p.TimeoutMS) * time.Millisecond
	}
	return fallback
}
func namespace(s string) bool {
	return s == "personal" || s == "work" || s == "projects" || s == "job-search" || s == "tech"
}
func contains(a []string, s string) bool {
	for _, v := range a {
		if s == v {
			return true
		}
	}
	return false
}

// Allows checks declared storage scope, never a model-predicted project.
func (e Egress) Allows(ns string, tags []string) bool {
	if !contains(e.AllowedNamespaces, ns) {
		return false
	}
	if len(tags) == 0 {
		return contains(e.UnassignedNamespaces, ns)
	}
	for _, tag := range tags {
		if !contains(e.AllowedProjectTags, tag) {
			return false
		}
	}
	return true
}

// ReadPrivate refuses symlinks and files readable by other users.
func ReadPrivate(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private regular file required")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	var f *os.File
	if err == nil {
		f = os.NewFile(uintptr(fd), path)
	}
	if err != nil {
		return nil, errors.New("cannot open private file")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("private file changed")
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(b)) > max {
		return nil, errors.New("private file exceeds limit")
	}
	return b, nil
}
func (p Profile) ReadKey() (string, error) {
	if p.KeyFile == "" && p.Local {
		return "", nil
	}
	b, err := ReadPrivate(p.KeyFile, 16<<10)
	if err != nil {
		return "", errors.New("cannot read inference key")
	}
	k := strings.TrimSpace(string(b))
	if k == "" || strings.ContainsAny(k, "\r\n") {
		return "", errors.New("invalid inference key")
	}
	return k, nil
}
