package integrationbundle

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestBundleGettersDoNotAliasAuthority(t *testing.T) {
	b := loadTestBundle(t)
	cfg := CapabilityConfig{Memory: CapabilityAvailable, Documents: CapabilityDisabled, Todoist: CapabilityUnavailable}
	before, err := b.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m := b.Manifest()
	m.Clients[0].ID = "corrupted"
	m.Clients[0].Artifacts[0].Path = "corrupted/path"
	m.Clients[0].OverridePaths[0] = "corrupted/override"
	m.OptionalCapabilities[0].Tools[0] = "corrupted_tool"
	m.SourceAssets[0].Path = "corrupted/source"
	p := b.Policy()
	p.RuleGroups[0].Rules[0].Text = "store everything"
	p.RuleGroups[0].Rules[0].Capabilities[0] = "corrupted"
	p.RetryRules[0].Operations[0] = "corrupted"
	p.Telemetry.Allowlist[0] = "prompt"
	p.ScenarioMappings[0].PolicyRefs[0] = "corrupted.rule"
	ids := b.ClientIDs()
	ids[0] = "corrupted"
	after, err := b.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("public getters alias authoritative bundle state")
	}
}

func TestRenderedSetsBindOneCapabilityConfig(t *testing.T) {
	b := loadTestBundle(t)
	aCfg := CapabilityConfig{Memory: CapabilityAvailable, Documents: CapabilityDisabled, Todoist: CapabilityUnavailable}
	bCfg := CapabilityConfig{Memory: CapabilityDisabled, Documents: CapabilityAvailable, Todoist: CapabilityAvailable}
	a, _ := b.Render(aCfg)
	other, _ := b.Render(bCfg)
	mixed := cloneArtifactSets(a)
	mixed[0].Artifacts[0] = other[0].Artifacts[0]
	refreshArtifactSetDigests(mixed)
	if err := b.ValidateRendered(mixed); err == nil || !strings.Contains(err.Error(), "capability configuration") {
		t.Fatalf("mixed config artifacts accepted: %v", err)
	}
	tampered := cloneArtifactSets(a)
	tampered[0].CapabilityConfig = bCfg
	if err := b.ValidateRendered(tampered); err == nil || !strings.Contains(err.Error(), "capability") {
		t.Fatalf("tampered config accepted: %v", err)
	}
	tampered = cloneArtifactSets(a)
	tampered[0].CapabilityConfigSHA256 = strings.Repeat("0", 64)
	if err := b.ValidateRendered(tampered); err == nil || !strings.Contains(err.Error(), "capability") {
		t.Fatalf("tampered config digest accepted: %v", err)
	}
}

func TestPortableRelativeArtifactPaths(t *testing.T) {
	invalid := []string{"", ".", "..", "a/../b", "a//b", "/a", `\\server\share`, `C:\foo`, "C:/foo", "C:foo", "a:b", "a\\b", "a\x00b", "a\nb", "a/./b", "CON", "con.txt", "dir/AUX.json", "NUL.md", "COM1", "lpt9.log", "a. ", "a./b", "dir /b"}
	for _, value := range invalid {
		value = strings.ReplaceAll(value, `\x00`, "\x00")
		value = strings.ReplaceAll(value, `\n`, "\n")
		if safeRelativePath(value) {
			t.Errorf("accepted unsafe path %q", value)
		}
	}
	for _, value := range []string{"codex/AGENTS.md", "generic-mcp/policy.json", "overrides/client/local.json"} {
		if !safeRelativePath(value) {
			t.Errorf("rejected portable path %q", value)
		}
	}
}

func TestPathCasefoldCollisionsAndOverlaps(t *testing.T) {
	paths := map[string]string{}
	if err := addSafePath(paths, "Client/File.md", "owned"); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{"client/file.md", "CLIENT", "client/file.md/child"} {
		if err := addSafePath(paths, candidate, "owned"); err == nil {
			t.Errorf("accepted casefold collision/overlap %q", candidate)
		}
	}
}

func TestPrivacyLintHighConfidenceValues(t *testing.T) {
	invalid := []string{"prefix postgresql://db.internal/name suffix", "ssh://host/path", "git+ssh://host/repo", "postgres://user:pass@host/db", `api_key: "abcd/efghijkl"`, `password='abc!def/12345'`, "/root", "/home", "/Users"}
	for _, value := range invalid {
		if err := validateSourcePrivacy("test", []byte(value)); err == nil {
			t.Errorf("privacy lint accepted %q", value)
		}
	}
	for _, prose := range []string{"Never store secrets or credentials.", "Private endpoints must not be logged.", "Reject password values and API tokens."} {
		if err := validateSourcePrivacy("test", []byte(prose)); err != nil {
			t.Errorf("privacy lint false positive %q: %v", prose, err)
		}
	}
}

func TestArtifactSetDigestUsesUnambiguousFraming(t *testing.T) {
	left := [][]byte{[]byte("a"), []byte("b\x00c")}
	right := [][]byte{[]byte("a\x00b"), []byte("c")}
	legacyFrame := func(fields [][]byte) []byte {
		var out bytes.Buffer
		for _, field := range fields {
			out.Write(field)
			out.WriteByte(0)
		}
		return out.Bytes()
	}
	lengthPrefixedFrame := func(fields [][]byte) []byte {
		var out bytes.Buffer
		for _, field := range fields {
			writeFramed(&out, field)
		}
		return out.Bytes()
	}
	if !bytes.Equal(legacyFrame(left), legacyFrame(right)) {
		t.Fatal("fixture does not reproduce the legacy NUL-delimited collision")
	}
	if bytes.Equal(lengthPrefixedFrame(left), lengthPrefixedFrame(right)) {
		t.Fatal("uint64 length-prefix framing failed to distinguish colliding legacy fields")
	}
}
