package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/liberocks/dynamox/provision"
	"github.com/liberocks/dynamox/runtime"
	"github.com/liberocks/dynamox/schema"
)

func TestFmtAndValidateCommands(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.dynamox")
	src := `// note
model User{
id String@id
@@table(name:"users")
}`
	if err := os.WriteFile(path, []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"fmt", path}); err != nil {
		t.Fatal(err)
	}
	formatted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(formatted), "// note") || !strings.Contains(string(formatted), "id String @id") {
		t.Fatalf("unexpected formatted schema:\n%s", formatted)
	}
	if err := run([]string{"fmt", "--check", path}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"validate", path}); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateWritesDirectoryOutput(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.dynamox")
	out := filepath.Join(dir, "generated")
	src := `model User {
 id String @id
 @@table(name: "users")
}`
	if err := os.WriteFile(schemaPath, []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"generate", "--out", out, schemaPath}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "client_gen.go")); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratePropagatesWriteFailure(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.dynamox")
	if err := os.WriteFile(schemaPath, []byte(`model User { id String @id @@table(name: "users") }`), 0600); err != nil {
		t.Fatal(err)
	}
	want := errors.New("disk full")
	err := runGenerateWith([]string{"--out", filepath.Join(dir, "out.go"), schemaPath}, func(string, []byte, os.FileMode) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("write failure = %v", err)
	}
}

func TestValidateIncludesFileLineAndColumn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid.dynamox")
	src := `model User {
 id String @id
 name MissingType
 @@table(name: "users")
}`
	if err := os.WriteFile(path, []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"validate", path})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), path+":3:") || !strings.Contains(err.Error(), "unknown field type MissingType") {
		t.Fatalf("missing location and message: %v", err)
	}
}

func TestCommandDispatchAndMainExit(t *testing.T) {
	if err := run(nil); err == nil {
		t.Fatal("expected usage error")
	}
	if err := run([]string{"not-a-command"}); err == nil {
		t.Fatal("expected unknown command error")
	}
	if err := run([]string{"help"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--help"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"sync", "/missing/dynamox.schema"}); err == nil {
		t.Fatal("sync dispatch should surface schema read error")
	}
	if err := run([]string{"backup"}); err == nil {
		t.Fatal("backup dispatch should surface usage")
	}
	if err := run([]string{"restore"}); err == nil {
		t.Fatal("restore dispatch should surface usage")
	}
	called := -1
	mainWithExit([]string{"help"}, func(code int) { called = code })
	if called != -1 {
		t.Fatalf("success called exit(%d)", called)
	}
	mainWithExit([]string{"bad"}, func(code int) { called = code })
	if called != 1 {
		t.Fatalf("failure exit code = %d", called)
	}
	old := os.Args
	os.Args = []string{"dynamox", "help"}
	defer func() { os.Args = old }()
	main()
}

func TestCLIArgumentAndFileFailures(t *testing.T) {
	readErr := errors.New("schema source read failed")
	if _, err := readSource(failingSource{err: readErr}); !errors.Is(err, readErr) {
		t.Fatalf("readSource did not return the read error: %v", err)
	}
	if err := runFmt([]string{"--bad"}); err == nil {
		t.Fatal("fmt flag error")
	}
	if err := runFmt([]string{"a", "b"}); err == nil {
		t.Fatal("fmt argument error")
	}
	if err := runFmt([]string{"/missing/dynamox.schema"}); err == nil {
		t.Fatal("fmt read error")
	}
	if err := runValidate([]string{"--bad"}); err == nil {
		t.Fatal("validate flag error")
	}
	if err := runValidate([]string{"a", "b"}); err == nil {
		t.Fatal("validate argument error")
	}
	if err := runValidate([]string{"/missing/dynamox.schema"}); err == nil {
		t.Fatal("validate read error")
	}
	if err := runGenerate([]string{"--bad"}); err == nil {
		t.Fatal("generate flag error")
	}
	if err := runGenerate([]string{"a", "b"}); err == nil {
		t.Fatal("generate argument error")
	}
	if err := runGenerate([]string{"/missing/dynamox.schema"}); err == nil {
		t.Fatal("generate read error")
	}
	if err := runSync([]string{"--bad"}); err == nil {
		t.Fatal("sync flag error")
	}
	if err := runSyncWith([]string{"a", "b"}, nil, nil); err == nil {
		t.Fatal("sync argument error")
	}
	if err := runSyncWith([]string{"/missing/dynamox.schema"}, nil, nil); err == nil {
		t.Fatal("sync read error")
	}
	if _, _, err := read("/missing/dynamox.schema"); err == nil {
		t.Fatal("read missing file")
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.dynamox")
	if err := os.WriteFile(bad, []byte("model"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := read(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("parse diagnostic: %v", err)
	}
	if err := runFmt([]string{bad}); err == nil {
		t.Fatal("fmt parse error")
	}
	if err := runValidate([]string{bad}); err == nil {
		t.Fatal("validate parse error")
	}
	if err := runGenerate([]string{bad}); err == nil {
		t.Fatal("generate parse error")
	}
	if err := runSyncWith([]string{bad}, nil, nil); err == nil {
		t.Fatal("sync parse error")
	}
	oversized := filepath.Join(dir, "oversized.dynamox")
	if err := os.WriteFile(oversized, []byte(strings.Repeat(" ", schema.MaxSourceBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := read(oversized); err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("read accepted oversized schema: %v", err)
	}
}

func TestCLIFormatValidationAndGenerationErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.dynamox")
	invalid := `model Bad { value Unknown @@table(name: "bad") }`
	if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runValidate([]string{path}); err == nil {
		t.Fatal("expected schema diagnostic")
	}
	if err := runGenerate([]string{path}); err == nil {
		t.Fatal("generate should validate first")
	}
	if err := validate(path, &schema.Schema{}); err == nil || !strings.Contains(err.Error(), path+":1:1:") {
		t.Fatalf("formatted diagnostics: %v", err)
	}
	valid := `model Item { id String @id @@table(name: "items") }`
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runFmt([]string{"--check", path}); err == nil {
		t.Fatal("unformatted file should fail check")
	}
	if err := runGenerate([]string{"--package", "bad-name", "--out", filepath.Join(dir, "bad.go"), path}); err == nil {
		t.Fatal("invalid package should fail gofmt")
	}
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runGenerate([]string{"--out", filepath.Join(blocker, "out.go"), path}); err == nil {
		t.Fatal("mkdir failure")
	}
	if err := runGenerate([]string{"--out", "/dev/full", path}); err == nil {
		// Some systems resolve /dev/full as a directory path during mkdir.
	}
	writeBlocker := filepath.Join(dir, "existing.go")
	if err := os.Mkdir(writeBlocker, 0700); err != nil {
		t.Fatal(err)
	}
	if err := runGenerate([]string{"--out", writeBlocker, path}); err == nil {
		t.Fatal("write failure")
	}
	if err := runGenerate([]string{"--out", filepath.Join(dir, "out.go"), path}); err != nil {
		t.Fatal(err)
	}
	if err := runFmt([]string{path}); err != nil {
		t.Fatal(err)
	}
	if err := runFmt([]string{"--check", path}); err != nil {
		t.Fatal(err)
	}
	missingTarget := filepath.Join(dir, "missing.go")
	if err := ensureRegularWriteTarget(missingTarget, false); err == nil {
		t.Fatal("write target validation accepted a missing required file")
	}
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("leave this intact"), 0600); err != nil {
		t.Fatal(err)
	}
	generatedLink := filepath.Join(dir, "generated.go")
	if err := os.Symlink(victim, generatedLink); err != nil {
		t.Fatal(err)
	}
	if err := runGenerate([]string{"--out", generatedLink, path}); err == nil {
		t.Fatal("generate followed a symbolic-link output")
	}
	formatLink := filepath.Join(dir, "linked-schema.dynamox")
	if err := os.Symlink(path, formatLink); err != nil {
		t.Fatal(err)
	}
	if err := runFmt([]string{formatLink}); err == nil {
		t.Fatal("fmt followed a symbolic-link input while writing")
	}
	if contents, err := os.ReadFile(victim); err != nil || string(contents) != "leave this intact" {
		t.Fatalf("symbolic-link output changed its target: %q %v", contents, err)
	}
}

func TestRunSyncWithInjectedDependencies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.dynamox")
	if err := os.WriteFile(path, []byte(`model Item { id String @id @@table(name: "items") }`), 0600); err != nil {
		t.Fatal(err)
	}
	var profile, region, endpoint string
	load := func(_ context.Context, p, r string) (aws.Config, error) {
		profile, region = p, r
		return aws.Config{Region: r}, nil
	}
	syncer := func(_ context.Context, cfg aws.Config, e string, tables []provision.Table) ([]string, error) {
		endpoint = e
		if cfg.Region != "ap-southeast-3" || len(tables) != 1 || tables[0].Name != "items" {
			t.Fatalf("sync args: %#v %#v", cfg, tables)
		}
		return []string{"items"}, nil
	}
	if err := runSyncWith([]string{"--profile", "test-profile", "--region", "ap-southeast-3", "--endpoint", "http://local", path}, load, syncer); err != nil {
		t.Fatal(err)
	}
	if profile != "test-profile" || region != "ap-southeast-3" || endpoint != "http://local" {
		t.Fatalf("options: %q %q %q", profile, region, endpoint)
	}
	zero := func(_ context.Context, _, _ string) (aws.Config, error) { return aws.Config{}, nil }
	noCreates := func(context.Context, aws.Config, string, []provision.Table) ([]string, error) { return nil, nil }
	if err := runSyncWith([]string{path}, zero, noCreates); err != nil {
		t.Fatal(err)
	}
	invalidPath := filepath.Join(dir, "invalid.dynamox")
	if err := os.WriteFile(invalidPath, []byte(`model Item { id Unknown @id @@table(name: "items") }`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runSyncWith([]string{invalidPath}, zero, noCreates); err == nil {
		t.Fatal("sync validation failure")
	}
	boom := errors.New("config")
	if err := runSyncWith([]string{path}, func(context.Context, string, string) (aws.Config, error) { return aws.Config{}, boom }, noCreates); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if err := runSyncWith([]string{path}, zero, func(context.Context, aws.Config, string, []provision.Table) ([]string, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
}

func TestLoadAWSConfigAndSyncSDKThroughLocalHTTP(t *testing.T) {
	awsDir := t.TempDir()
	configPath := filepath.Join(awsDir, "config")
	credentialsPath := filepath.Join(awsDir, "credentials")
	if err := os.WriteFile(configPath, []byte("[profile dynamox-test]\nregion = ap-southeast-3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialsPath, []byte("[dynamox-test]\naws_access_key_id = local\naws_secret_access_key = local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", configPath)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credentialsPath)
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_SESSION_TOKEN", "")

	cfg, err := loadAWSConfig(context.Background(), "dynamox-test", "ap-southeast-3")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Region != "ap-southeast-3" {
		t.Fatalf("region %q", cfg.Region)
	}
	describe := 0
	transport := httpClientFunc(func(r *http.Request) (*http.Response, error) {
		var status int
		headers := make(http.Header)
		var body string
		switch r.Header.Get("X-Amz-Target") {
		case "DynamoDB_20120810.DescribeTable":
			describe++
			if describe == 1 {
				status = http.StatusBadRequest
				headers.Set("x-amzn-errortype", "ResourceNotFoundException")
				body = `{"__type":"ResourceNotFoundException","message":"missing"}`
			} else {
				body = `{"Table":{"TableName":"sdk-test","TableStatus":"ACTIVE","KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}]}}`
			}
		case "DynamoDB_20120810.CreateTable":
			status = http.StatusOK
			body = `{"TableDescription":{"TableName":"sdk-test","TableStatus":"CREATING"}}`
		case "DynamoDB_20120810.DescribeTimeToLive":
			body = `{"TimeToLiveDescription":{"TimeToLiveStatus":"DISABLED"}}`
		default:
			return nil, errors.New("unexpected target " + r.Header.Get("X-Amz-Target"))
		}
		if status == 0 {
			status = http.StatusOK
		}
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	creds := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "local", SecretAccessKey: "local", Source: "test"}, nil
	})
	created, err := syncUsingSDK(context.Background(), aws.Config{Region: "ap-southeast-3", Credentials: creds, HTTPClient: transport}, "http://dynamodb.invalid", []provision.Table{{Name: "sdk-test", Partition: provision.Key{Name: "id", Type: "S", Kind: "HASH"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(created, []string{"sdk-test"}) {
		t.Fatalf("created %v", created)
	}
}

type httpClientFunc func(*http.Request) (*http.Response, error)

type failingSource struct{ err error }

func (r failingSource) Read([]byte) (int, error) { return 0, r.err }

func (f httpClientFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestProvisionTablesAndScalarMapping(t *testing.T) {
	s, err := schema.Parse(`model Example { id String @id sort Int @sortKey at DateTime amount Float enabled Boolean expiresAt Int data Bytes @@table(name: "example", read: 4, write: 2) @@index(name: "ByAmount", partition: amount, sort: at, type: GSI, read: 3, write: 1) @@index(name: "LocalBySort", sort: sort, type: LSI) @@ttl(field: expiresAt) }`)
	if err != nil {
		t.Fatal(err)
	}
	got := provisionTables(s)
	if len(got) != 1 || got[0].Partition.Type != "S" || got[0].Indexes[0].Partition.Type != "N" || !got[0].Indexes[1].Local || got[0].Indexes[1].Partition.Name != "id" || got[0].TTL != "expiresAt" || got[0].ReadCapacityUnits != 4 || got[0].WriteCapacityUnits != 2 || got[0].Indexes[0].ReadCapacityUnits != 3 || got[0].Indexes[0].WriteCapacityUnits != 1 {
		t.Fatalf("provision tables: %#v", got)
	}
	for _, tc := range []struct {
		field *schema.Field
		want  string
	}{{nil, "S"}, {&schema.Field{Type: "String"}, "S"}, {&schema.Field{Type: "DateTime"}, "S"}, {&schema.Field{Type: "Boolean"}, "S"}, {&schema.Field{Type: "Int"}, "N"}, {&schema.Field{Type: "Float"}, "N"}, {&schema.Field{Type: "Bytes"}, "B"}} {
		if got := scalar(tc.field); string(got) != tc.want {
			t.Fatalf("scalar %v = %v", tc.field, got)
		}
	}
}

func TestSerialSchemaProvisionsSharedSequenceTable(t *testing.T) {
	s, err := schema.Parse(`model Item { id Int @id @default(serial) value String @@table(name: "items") @@pitr(enabled: true, recoveryDays: 14) }`)
	if err != nil {
		t.Fatal(err)
	}
	if ds := schema.Validate(s); len(ds) != 0 {
		t.Fatalf("schema diagnostics: %v", ds)
	}
	tables := provisionTables(s)
	if len(tables) != 2 || tables[1].Name != runtime.SequenceTableName || tables[1].Partition.Name != "name" || tables[1].Partition.Type != "S" || tables[1].PITR == nil || !tables[1].PITR.Enabled || tables[1].PITR.RecoveryDays != 14 || !tables[1].PITR.PreserveLonger {
		t.Fatalf("sequence provisioning: %#v", tables)
	}
}
