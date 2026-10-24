// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/config"
)

func TestConfigDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "0"},
		{5 * time.Second, "5s"},
		{60 * time.Second, "1m"},
		{5 * time.Minute, "5m"},
		{90 * time.Second, "90s"},
		{24 * time.Hour, "24h"},
		{720 * time.Hour, "720h"},
		{90 * time.Minute, "90m"},
		{1500 * time.Millisecond, "1.5s"},
		{-time.Hour, "-1h"},
	}

	for _, tc := range tests {
		if got := configDuration(tc.in); got != tc.want {
			t.Errorf("configDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestConfigDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   any
		want string
	}{
		{"string", "abc", "abc"},
		{"empty string", "", ""},
		{"int", 8080, "8080"},
		{"int64", int64(17179869184), "17179869184"},
		{"negative", -1, "-1"},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"float", 1.0, "1"},
		{"float fraction", 0.25, "0.25"},
		{"duration", 30 * time.Minute, "30m"},
		{"list", []string{"a", "b"}, "a,b"},
		{"nil list", []string(nil), ""},
		{"map sorted", map[string]string{"b": "2", "a": "1"}, "a=1,b=2"},
		{"nil map", map[string]string(nil), ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := configDefault(reflect.ValueOf(tc.in)); got != tc.want {
				t.Errorf("configDefault(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestConfigKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      any
		want    string
		wantErr bool
	}{
		{"", "string", false},
		{0, "int", false},
		{int64(0), "int", false},
		{false, "bool", false},
		{0.5, "float", false},
		{time.Second, "duration", false},
		{[]string{}, "list", false},
		{map[string]string{}, "map", false},
		{[]int{}, "", true},
		{map[string]int{}, "", true},
		{uint8(0), "", true},
	}

	for _, tc := range tests {
		got, err := configKind(reflect.TypeOf(tc.in))
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("configKind(%T) = %q, %v; want %q, error=%t", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestConfigIsSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path []string
		want bool
	}{
		{[]string{"database", "password"}, true},
		{[]string{"gpg", "passphrase"}, true},
		{[]string{"storage", "secretKey"}, true},
		{[]string{"storage", "gcsHmacSecret"}, true},
		{[]string{"storage", "replica", "ociPassword"}, true},
		{[]string{"storage", "replica", "azureAccountKey"}, true},
		{[]string{"server", "apiKey"}, true},
		{[]string{"ai", "apiKey"}, true},
		{[]string{"auth", "hmacKey"}, true},
		{[]string{"auth", "samlSpKey"}, true},
		{[]string{"telemetry", "otlp", "headers"}, true},
		{[]string{"vault", "authToken"}, true},
		{[]string{"ai", "maxTokens"}, false},
		{[]string{"storage", "accessKey"}, false},
		{[]string{"storage", "gcsHmacKey"}, false},
		{[]string{"server", "tlsKey"}, false},
		{[]string{"gpg", "keyPath"}, false},
		{[]string{"server", "headers"}, false},
	}

	for _, tc := range tests {
		if got := configIsSecret(tc.path); got != tc.want {
			t.Errorf("configIsSecret(%v) = %t, want %t", tc.path, got, tc.want)
		}
	}
}

func TestConfigSubject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, text, goName, yamlName, want string
	}{
		{"replaced", "Interval is the pause.", "Interval", "interval", "`interval` is the pause."},
		{"dotted", "StorageReplica is the replica.", "StorageReplica", "storage.replica", "`storage.replica` is the replica."},
		{"other subject", "Pause between passes.", "Interval", "interval", "Pause between passes."},
		{"prefix of a longer word", "Intervals differ.", "Interval", "interval", "Intervals differ."},
		{"only the name", "Interval", "Interval", "interval", "Interval"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := configSubject(tc.text, tc.goName, tc.yamlName); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConfigMarkdown(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs", "operations"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "docs", "operations", "x.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{name: "plain", in: "just text", want: "just text"},
		{name: "pipe outside code", in: "a | b", want: `a \| b`},
		{name: "pipe inside code", in: "`a|b` and c", want: "`a\\|b` and c"},
		{name: "html and vue", in: "use <tag> and {{x}}", want: `use \<tag> and \{\{x\}\}`},
		{name: "html inside code stays", in: "use `<tag>` and `{{x}}`", want: "use `<tag>` and `{{x}}`"},
		{name: "env assignment", in: "set OTEL_SDK_DISABLED=true now", want: "set `OTEL_SDK_DISABLED=true` now"},
		{name: "env wildcard", in: "the OTEL_* variables", want: "the `OTEL_*` variables"},
		{name: "env prefix wildcard", in: "OTEL_EXPORTER_OTLP_TRACES_* apply", want: "`OTEL_EXPORTER_OTLP_TRACES_*` apply"},
		{name: "env single word is not a variable", in: "uses HTTP and RSA-4096", want: "uses HTTP and RSA-4096"},
		{name: "env inside code is untouched", in: "`BASE_URL` and BASE_URL", want: "`BASE_URL` and `BASE_URL`"},
		{name: "doc link", in: "see docs/operations/x.md.", want: "see [operations/x.md](../operations/x.md)."},
		{name: "doc link with anchor", in: "see docs/operations/x.md#part", want: "see [operations/x.md#part](../operations/x.md#part)"},
		{name: "missing doc", in: "see docs/operations/gone.md", wantErr: "docs/operations/gone.md"},
		{name: "unbalanced backtick", in: "a `b", wantErr: "unbalanced"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := configMarkdown(root, tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Schema.

type (
	// CfgTestInner is embedded inline by cfgTestOuter.
	CfgTestInner struct {
		A string `yaml:"a"`
	}
	cfgTestSub struct {
		B int `yaml:"b"`
	}
	cfgTestOuter struct {
		CfgTestInner `yaml:",inline"`

		Sub  cfgTestSub    `yaml:"sub"`
		Skip string        `yaml:"-"`
		D    time.Duration `yaml:"d"`
		L    []string      `yaml:"l"`

		unexported int //nolint:unused // proves that unexported fields are skipped
	}
	cfgTestNoTag   struct{ X int }
	cfgTestBadType struct {
		C chan int `yaml:"c"`
	}
)

func TestConfigWalk(t *testing.T) {
	t.Parallel()

	var (
		keys   []configKey
		groups []configGroup
	)

	v := reflect.ValueOf(&cfgTestOuter{D: time.Minute, L: []string{"x"}}).Elem()
	if err := configWalk(v, nil, nil, &keys, &groups); err != nil {
		t.Fatal(err)
	}

	var gotKeys []string
	for _, k := range keys {
		gotKeys = append(gotKeys, k.dotted()+"|"+k.goPath+"|"+k.kind+"|"+k.def+"|"+k.owner.Name()+"."+k.field)
	}

	wantKeys := []string{
		"a|A|string||CfgTestInner.A",
		"sub.b|Sub.B|int|0|cfgTestSub.B",
		"d|D|duration|1m|cfgTestOuter.D",
		"l|L|list|x|cfgTestOuter.L",
	}

	if !slices.Equal(gotKeys, wantKeys) {
		t.Errorf("keys:\n got %q\nwant %q", gotKeys, wantKeys)
	}

	if len(groups) != 1 || groups[0].dotted() != "sub" || groups[0].field != "Sub" || groups[0].owner != reflect.TypeFor[cfgTestOuter]() {
		t.Errorf("groups = %+v, want one group \"sub\" declared by cfgTestOuter.Sub", groups)
	}
}

func TestConfigWalkErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		v    any
		want string
	}{
		{"missing yaml tag", &cfgTestNoTag{}, "has no yaml tag"},
		{"unsupported type", &cfgTestBadType{}, "unsupported configuration struct"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var (
				keys   []configKey
				groups []configGroup
			)

			err := configWalk(reflect.ValueOf(tc.v).Elem(), nil, nil, &keys, &groups)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestConfigSchema(t *testing.T) {
	t.Parallel()

	keys, groups, err := configSchema(config.Defaults())
	if err != nil {
		t.Fatal(err)
	}

	have := map[string]configKey{}
	for _, k := range keys {
		have[k.dotted()] = k
	}

	for _, path := range []string{
		"baseUrl", "server.port", "database.password", "storage.type", "storage.presignTtl",
		"storage.replica.type", "storage.replica.readFallback", "recovery.journal.enabled",
		"telemetry.otlp.headers", "scanners.trivy.path",
	} {
		if _, ok := have[path]; !ok {
			t.Errorf("key %s missing from the schema", path)
		}
	}

	for _, ignored := range configReplicaIgnored {
		if _, ok := have[ignored]; ok {
			t.Errorf("key %s should have been dropped", ignored)
		}
	}

	if k := have["storage.replica.type"]; k.goPath != "StorageReplica.Type" || k.kind != "string" {
		t.Errorf("storage.replica.type = %+v, want the promoted Go path StorageReplica.Type", k)
	}

	var gotGroups []string
	for _, g := range groups {
		gotGroups = append(gotGroups, g.dotted())
	}

	for _, want := range []string{"server", "storage", "storage.replica", "telemetry.otlp"} {
		if !slices.Contains(gotGroups, want) {
			t.Errorf("group %s missing, have %v", want, gotGroups)
		}
	}
}

// ---------------------------------------------------------------------------
// Environment scan.

func parseConfigSource(t *testing.T, src string) []*ast.File {
	t.Helper()

	f, err := parser.ParseFile(token.NewFileSet(), "src.go", "package config\n"+src, 0)
	if err != nil {
		t.Fatal(err)
	}

	return []*ast.File{f}
}

const configScanSource = `
const prefix = "APP_"

func applyEnvOverrides(cfg *Config) {
	overrideString(&cfg.Name, "APP_NAME")
	overrideInt(&cfg.Server.Port, prefix+"PORT")
	r := &cfg.Replica
	overrideBool(&r.Enabled, "APP_REPLICA_ENABLED")
	if v, ok := os.LookupEnv("APP_BACKEND"); ok {
		cfg.Store.Type = v
	} else if v, ok := os.LookupEnv("APP_TYPE"); ok {
		cfg.Store.Type = v
	}
	applyTelemetry(&cfg.Telemetry, 3)
	var local string
	overrideString(&local, "APP_LOOSE")
	helperWithoutConfig(1)
}

func applyTelemetry(o *OTLP, n int) {
	const own = "APP_OTLP_"
	overrideString(&o.Endpoint, own+"ENDPOINT")
	if v, ok := os.LookupEnv("APP_METRICS"); ok {
		o.Metrics = v == "otlp"
	}
}

func helperWithoutConfig(n int) {
	overrideString(&ignored, "APP_NEVER_REACHED")
}
`

func TestConfigScanEnv(t *testing.T) {
	t.Parallel()

	sc, err := configScanEnv(parseConfigSource(t, configScanSource))
	if err != nil {
		t.Fatal(err)
	}

	want := map[string][]string{
		"Name":               {"APP_NAME"},
		"Server.Port":        {"APP_PORT"},
		"Replica.Enabled":    {"APP_REPLICA_ENABLED"},
		"Store.Type":         {"APP_BACKEND", "APP_TYPE"},
		"Telemetry.Endpoint": {"APP_OTLP_ENDPOINT"},
		"Telemetry.Metrics":  {"APP_METRICS"},
	}

	if !reflect.DeepEqual(sc.byPath, want) {
		t.Errorf("byPath:\n got %v\nwant %v", sc.byPath, want)
	}

	if !slices.Equal(sc.loose, []string{"APP_LOOSE"}) {
		t.Errorf("loose = %v, want [APP_LOOSE]", sc.loose)
	}

	if !reflect.DeepEqual(sc.chained, map[string]bool{"APP_TYPE": true}) {
		t.Errorf("chained = %v, want only APP_TYPE", sc.chained)
	}
}

func TestConfigScanEnvErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want string
	}{
		{"no entry point", "func other() {}", "applyEnvOverrides(cfg *Config) not found"},
		{"nothing set", "func applyEnvOverrides(cfg *Config) {}", "sets no config field"},
		{"dynamic name", "func applyEnvOverrides(cfg *Config) { overrideString(&cfg.A, name) }", "not a constant string"},
		{"dynamic lookup", "func applyEnvOverrides(cfg *Config) { if v, ok := os.LookupEnv(name); ok { cfg.A = v } }", "non-constant name"},
		{"runaway recursion", "func applyEnvOverrides(cfg *Config) { overrideString(&cfg.A, \"X\"); applyEnvOverrides(cfg) }", "deeper than"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := configScanEnv(parseConfigSource(t, tc.src))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestConfigBindEnv(t *testing.T) {
	t.Parallel()

	headers := configKey{path: []string{"telemetry", "otlp", "headers"}, goPath: "Telemetry.OTLP.Headers"}
	loose := []string{"OTEL_EXPORTER_OTLP_HEADERS", "OTEL_SDK_DISABLED"}

	tests := []struct {
		name     string
		keys     []configKey
		scan     configEnvScan
		wantEnvs map[string][]string
		want     []string
	}{
		{
			name: "consistent, alias joins its key",
			keys: []configKey{headers, {path: []string{"a"}, goPath: "A"}},
			scan: configEnvScan{byPath: map[string][]string{"A": {"A_ENV"}}, loose: loose},
			wantEnvs: map[string][]string{
				"telemetry.otlp.headers": {"OTEL_EXPORTER_OTLP_HEADERS"},
				"a":                      {"A_ENV"},
			},
		},
		{
			name: "key without override",
			keys: []configKey{headers, {path: []string{"a"}, goPath: "A"}},
			scan: configEnvScan{byPath: map[string][]string{}, loose: loose},
			want: []string{"yaml key a has no environment override"},
		},
		{
			name: "override without key",
			keys: []configKey{headers},
			scan: configEnvScan{byPath: map[string][]string{"Gone": {"GONE_ENV"}}, loose: loose},
			want: []string{"environment override GONE_ENV sets config field Gone"},
		},
		{
			name: "unlisted loose variable",
			keys: []configKey{headers},
			scan: configEnvScan{byPath: map[string][]string{}, loose: append(slices.Clone(loose), "NEW_LOOSE")},
			want: []string{"NEW_LOOSE does not set a config field"},
		},
		{
			name: "stale loose entry",
			keys: []configKey{headers},
			scan: configEnvScan{byPath: map[string][]string{}, loose: []string{"OTEL_SDK_DISABLED"}},
			want: []string{"configLooseEnv lists OTEL_EXPORTER_OTLP_HEADERS"},
		},
		{
			name: "loose alias of a vanished key",
			keys: nil,
			scan: configEnvScan{byPath: map[string][]string{}, loose: loose},
			want: []string{"maps OTEL_EXPORTER_OTLP_HEADERS to unknown yaml key telemetry.otlp.headers"},
		},
		{
			name: "two variables outside an else-if chain",
			keys: []configKey{headers, {path: []string{"a"}, goPath: "A"}},
			scan: configEnvScan{byPath: map[string][]string{"A": {"ONE", "TWO"}}, chained: map[string]bool{}, loose: loose},
			want: []string{"yaml key a is set by several variables (ONE, TWO)"},
		},
		{
			name: "two variables in an else-if chain",
			keys: []configKey{headers, {path: []string{"a"}, goPath: "A"}},
			scan: configEnvScan{byPath: map[string][]string{"A": {"ONE", "TWO"}}, chained: map[string]bool{"TWO": true}, loose: loose},
			wantEnvs: map[string][]string{
				"telemetry.otlp.headers": {"OTEL_EXPORTER_OTLP_HEADERS"},
				"a":                      {"ONE", "TWO"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			keys := slices.Clone(tc.keys)

			problems := strings.Join(configBindEnv(keys, &tc.scan), "\n")

			for _, w := range tc.want {
				if !strings.Contains(problems, w) {
					t.Errorf("problems %q lack %q", problems, w)
				}
			}

			if len(tc.want) == 0 && problems != "" {
				t.Errorf("unexpected problems: %s", problems)
			}

			for _, k := range keys {
				if want, ok := tc.wantEnvs[k.dotted()]; ok && !slices.Equal(k.envs, want) {
					t.Errorf("envs of %s = %v, want %v", k.dotted(), k.envs, want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Example files.

func TestConfigEnvExampleNames(t *testing.T) {
	t.Parallel()

	got := configEnvExampleNames(`# A plain comment mentioning NOT_A_VAR=1 in the middle
FIRST=1
#SECOND=2
# THIRD=3   # trailing comment
  # INDENTED_NOT_HEADER=4
lower=5
# Heading: TEXT
FOURTH=a=b
`)

	for _, name := range []string{"FIRST", "SECOND", "THIRD", "FOURTH"} {
		if !got[name] {
			t.Errorf("%s not found in %v", name, got)
		}
	}

	for _, name := range []string{"NOT_A_VAR", "INDENTED_NOT_HEADER", "lower", "TEXT"} {
		if got[name] {
			t.Errorf("%s should not be found", name)
		}
	}
}

func TestConfigYAMLExampleKeys(t *testing.T) {
	t.Parallel()

	const doc = `# Top comment.
# Note: prose that must not become a key.
server:
  port: 8080
  # host: 0.0.0.0
  # Free-form: not a key either.

storage:
  type: "filesystem"
  # Blob replication, see docs.
  # replica:
  #   type: s3                 # filesystem | s3
  #   # path: /mnt/replica
  #   readFallback: notfound
  # stagingPath: /tmp          # back at the storage level

#federation:
#  enabled: false

log:
  level: info
`

	segments := map[string]bool{}
	for s := range strings.FieldsSeq("server port host storage type replica path readFallback stagingPath federation enabled log level") {
		segments[s] = true
	}

	got := configYAMLExampleKeys(doc, segments)

	want := []string{
		"server", "server.port", "server.host",
		"storage", "storage.type", "storage.replica", "storage.replica.type",
		"storage.replica.path", "storage.replica.readFallback", "storage.stagingPath",
		"federation", "federation.enabled",
		"log", "log.level",
	}

	for _, key := range want {
		if !got[key] {
			t.Errorf("%s not found in %v", key, slices.Sorted(keysOf(got)))
		}
	}

	if len(got) != len(want) {
		t.Errorf("found %d keys %v, want %d", len(got), slices.Sorted(keysOf(got)), len(want))
	}
}

func keysOf(m map[string]bool) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func TestConfigCheckExamples(t *testing.T) {
	t.Parallel()

	keys := []configKey{
		{path: []string{"server", "port"}, envs: []string{"SERVER_PORT"}},
		{path: []string{"a"}, envs: []string{"A_ENV", "A_ALIAS"}},
	}
	groups := []configGroup{{path: []string{"server"}}}
	sc := &configEnvScan{loose: []string{"LOOSE_ONE"}}

	const (
		goodEnv  = "SERVER_PORT=1\n#A_ENV=2\n# A_ALIAS=3\n# LOOSE_ONE=4\n"
		goodYAML = "server:\n  port: 1\n# a: 2\n"
	)

	tests := []struct {
		name string
		env  string
		yaml string
		want []string
	}{
		{name: "complete", env: goodEnv, yaml: goodYAML},
		{
			name: "env variable removed",
			env:  strings.Replace(goodEnv, "SERVER_PORT=1\n", "", 1),
			yaml: goodYAML,
			want: []string{configEnvExample + " does not mention SERVER_PORT"},
		},
		{
			name: "alias and loose variable removed",
			env:  "SERVER_PORT=1\n#A_ENV=2\n",
			yaml: goodYAML,
			want: []string{"does not mention A_ALIAS", "does not mention LOOSE_ONE"},
		},
		{
			name: "yaml key removed",
			env:  goodEnv,
			yaml: "server:\n  host: x\n# a: 2\n",
			want: []string{configYAMLExample + " does not mention server.port"},
		},
		{
			name: "yaml key under the wrong parent",
			env:  goodEnv,
			yaml: "server:\n  host: x\nother:\n  port: 1\n# a: 2\n",
			want: []string{"does not mention server.port"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			writeConfigFile(t, filepath.Join(root, configEnvExample), tc.env)
			writeConfigFile(t, filepath.Join(root, configYAMLExample), tc.yaml)

			problems, err := configCheckExamples(root, keys, groups, sc)
			if err != nil {
				t.Fatal(err)
			}

			joined := strings.Join(problems, "\n")

			for _, w := range tc.want {
				if !strings.Contains(joined, w) {
					t.Errorf("problems %q lack %q", joined, w)
				}
			}

			if len(problems) != len(tc.want) {
				t.Errorf("got %d problems, want %d: %s", len(problems), len(tc.want), joined)
			}
		})
	}
}

func TestConfigCheckExamplesMissingFiles(t *testing.T) {
	t.Parallel()

	if _, err := configCheckExamples(t.TempDir(), nil, nil, &configEnvScan{}); err == nil {
		t.Fatal("expected an error when the example files are absent")
	}
}

func writeConfigFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// The real repository.

// TestRunConfigRepository generates the page from this repository: it fails
// when a key has no environment override or description, when an override
// feeds no key, or when an example file misses something.
func TestRunConfigRepository(t *testing.T) {
	t.Parallel()

	out, err := runConfig("../..")
	if err != nil {
		t.Fatal(err)
	}

	page := string(out[configPage])

	for _, want := range []string{
		"# Configuration reference",
		"## server",
		"| `server.port` | `SERVER_PORT` | int | `8080` |",
		"### `storage.replica`",
		"`OTEL_SDK_DISABLED`",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}

	if strings.Contains(page, "storage.replica.presignTtl") {
		t.Error("ignored replica key listed")
	}

	if strings.Contains(page, "{{") || strings.Contains(page, "}}") {
		t.Error("page contains Vue interpolation delimiters")
	}
}
