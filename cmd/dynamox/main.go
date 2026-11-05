package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/liberocks/dynamox/backup"
	"github.com/liberocks/dynamox/codegen"
	"github.com/liberocks/dynamox/provision"
	"github.com/liberocks/dynamox/runtime"
	"github.com/liberocks/dynamox/schema"
)

func main() {
	mainWithExit(os.Args[1:], os.Exit)
}
func mainWithExit(args []string, exit func(int)) {
	if err := run(args); err != nil {
		fmt.Fprintln(os.Stderr, "dynamox:", err)
		exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dynamox <fmt|validate|generate|sync|backup|restore> [options] [schema]")
	}
	switch args[0] {
	case "fmt":
		return runFmt(args[1:])
	case "validate":
		return runValidate(args[1:])
	case "generate":
		return runGenerate(args[1:])
	case "sync":
		return runSync(args[1:])
	case "backup":
		return runBackup(args[1:])
	case "restore":
		return runRestore(args[1:])
	case "help", "-h", "--help":
		fmt.Println("usage: dynamox <fmt|validate|generate|sync|backup|restore> [options] [schema]")
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
func read(path string) (string, *schema.Schema, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer file.Close()
	src, err := readSource(file)
	if err != nil {
		return "", nil, err
	}
	s, err := schema.Parse(string(src))
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", path, err)
	}
	return string(src), s, nil
}

func readSource(source io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(source, schema.MaxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > schema.MaxSourceBytes {
		return nil, fmt.Errorf("schema exceeds the %d-byte limit", schema.MaxSourceBytes)
	}
	return data, nil
}
func validate(path string, s *schema.Schema) error {
	ds := schema.Validate(s)
	if len(ds) == 0 {
		return nil
	}
	var b strings.Builder
	for _, d := range ds {
		fmt.Fprintf(&b, "%s:%d:%d: %s\n", path, d.Position.Line, d.Position.Column, d.Message)
	}
	return fmt.Errorf("%s", strings.TrimSuffix(b.String(), "\n"))
}
func runFmt(args []string) error {
	fs := flag.NewFlagSet("fmt", flag.ContinueOnError)
	check := fs.Bool("check", false, "check formatting without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := "schema.dynamox"
	if fs.NArg() > 1 {
		return fmt.Errorf("fmt accepts one schema file")
	}
	if fs.NArg() == 1 {
		path = fs.Arg(0)
	}
	src, _, err := read(path)
	if err != nil {
		return err
	}
	// read already parsed this immutable source, so formatting cannot hit a
	// lexical error here.
	formatted, _ := schema.Format(src)
	if *check {
		if src != formatted {
			return fmt.Errorf("%s is not formatted", path)
		}
		return nil
	}
	if err := ensureRegularWriteTarget(path, false); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(formatted), 0644)
}
func runValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := "schema.dynamox"
	if fs.NArg() > 1 {
		return fmt.Errorf("validate accepts one schema file")
	}
	if fs.NArg() == 1 {
		path = fs.Arg(0)
	}
	_, s, err := read(path)
	if err != nil {
		return err
	}
	if err = validate(path, s); err != nil {
		return err
	}
	fmt.Printf("%s is valid\n", path)
	return nil
}
func runGenerate(args []string) error {
	return runGenerateWith(args, os.WriteFile)
}

func runGenerateWith(args []string, writeFile func(string, []byte, os.FileMode) error) error {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	out := fs.String("out", "dynamox.gen.go", "generated Go file or output directory")
	pkg := fs.String("package", "dynamoxgen", "generated Go package name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := "schema.dynamox"
	if fs.NArg() > 1 {
		return fmt.Errorf("generate accepts one schema file")
	}
	if fs.NArg() == 1 {
		path = fs.Arg(0)
	}
	_, s, err := read(path)
	if err != nil {
		return err
	}
	if err = validate(path, s); err != nil {
		return err
	}
	data, err := codegen.Generate(s, *pkg)
	if err != nil {
		return err
	}
	dest := *out
	if filepath.Ext(dest) != ".go" {
		dest = filepath.Join(dest, "client_gen.go")
	}
	if err = os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	if err = ensureRegularWriteTarget(dest, true); err != nil {
		return err
	}
	if err = writeFile(dest, data, 0644); err != nil {
		return err
	}
	fmt.Printf("generated %s\n", dest)
	return nil
}

func ensureRegularWriteTarget(path string, allowMissing bool) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && allowMissing {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to write non-regular file %q", path)
	}
	return nil
}

func runSync(args []string) error {
	return runSyncWith(args, loadAWSConfig, syncUsingSDK)
}
func loadAWSConfig(ctx context.Context, profile, region string) (aws.Config, error) {
	opts := []func(*config.LoadOptions) error{}
	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	return config.LoadDefaultConfig(ctx, opts...)
}
func syncUsingSDK(ctx context.Context, cfg aws.Config, endpoint string, tables []provision.Table) ([]string, error) {
	client := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	})
	return provision.Sync(ctx, client, tables)
}
func runSyncWith(args []string, loadConfig func(context.Context, string, string) (aws.Config, error), syncTables func(context.Context, aws.Config, string, []provision.Table) ([]string, error)) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	profile := fs.String("profile", "", "AWS shared config profile")
	region := fs.String("region", "", "AWS region (defaults to the SDK configuration)")
	endpoint := fs.String("endpoint", "", "DynamoDB endpoint, for example DynamoDB Local")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := "schema.dynamox"
	if fs.NArg() > 1 {
		return fmt.Errorf("sync accepts one schema file")
	}
	if fs.NArg() == 1 {
		path = fs.Arg(0)
	}
	_, s, err := read(path)
	if err != nil {
		return err
	}
	if err = validate(path, s); err != nil {
		return err
	}
	ctx := context.Background()
	cfg, err := loadConfig(ctx, *profile, *region)
	if err != nil {
		return err
	}
	created, err := syncTables(ctx, cfg, *endpoint, provisionTables(s))
	if err != nil {
		return err
	}
	if len(created) == 0 {
		fmt.Println("tables are in sync")
	} else {
		for _, name := range created {
			fmt.Printf("created table %s\n", name)
		}
	}
	return nil
}

func runBackup(args []string) error {
	return runBackupWith(args, loadAWSConfig, newDynamoOperationsClient)
}

type dynamoOperationsAPI interface {
	backup.API
	provision.API
}

func runBackupWith(args []string, loadConfig func(context.Context, string, string) (aws.Config, error), newClient func(aws.Config, string) (dynamoOperationsAPI, error)) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dynamox backup <create|list|status> [options] [schema]")
	}
	fs := flag.NewFlagSet("backup "+args[0], flag.ContinueOnError)
	profile := fs.String("profile", "", "AWS shared config profile")
	region := fs.String("region", "", "AWS region (defaults to SDK configuration)")
	endpoint := fs.String("endpoint", "", "DynamoDB endpoint")
	name := fs.String("name", "", "backup set name")
	manifestPath := fs.String("manifest", "", "backup set manifest path")
	writesPaused := fs.Bool("writes-paused", false, "confirm application writes are paused")
	settle := fs.Duration("settle", time.Minute, "time to wait after writes stop")
	timeout := fs.Duration("timeout", 2*time.Hour, "maximum operation duration")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("backup %s accepts at most one schema file", args[0])
	}
	if args[0] == "create" && (*manifestPath == "" || *name == "") {
		return errors.New("backup create requires --manifest and --name")
	}
	if args[0] == "status" && *manifestPath == "" {
		return errors.New("backup status requires --manifest")
	}
	if args[0] == "create" && !*writesPaused {
		return errors.New("backup set requires --writes-paused acknowledgment")
	}
	path := "schema.dynamox"
	if fs.NArg() == 1 {
		path = fs.Arg(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	var s *schema.Schema
	if args[0] != "status" {
		_, parsed, err := read(path)
		if err != nil {
			return err
		}
		if err = validate(path, parsed); err != nil {
			return err
		}
		s = parsed
	}
	cfg, err := loadConfig(ctx, *profile, *region)
	if err != nil {
		return err
	}
	db, err := newClient(cfg, *endpoint)
	if err != nil {
		return err
	}
	switch args[0] {
	case "create":
		manifest, err := backup.CreateSet(ctx, db, *manifestPath, *name, cfg.Region, tableNames(provisionTables(s)), *writesPaused, *settle)
		if err != nil {
			return err
		}
		manifest, err = backup.WaitBackupSet(ctx, db, *manifestPath, 10*time.Second)
		if err != nil {
			return err
		}
		return printJSON(manifest)
	case "list":
		result, err := backup.List(ctx, db, tableNames(provisionTables(s)))
		if err != nil {
			return err
		}
		return printJSON(result)
	case "status":
		manifest, err := backup.RefreshBackupStatus(ctx, db, *manifestPath)
		if err != nil {
			return err
		}
		return printJSON(manifest)
	default:
		return fmt.Errorf("unknown backup command %q", args[0])
	}
}

func runRestore(args []string) error {
	return runRestoreWith(args, loadAWSConfig, newDynamoOperationsClient)
}

func newDynamoOperationsClient(cfg aws.Config, endpoint string) (dynamoOperationsAPI, error) {
	return dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	}), nil
}

func runRestoreWith(args []string, loadConfig func(context.Context, string, string) (aws.Config, error), newClient func(aws.Config, string) (dynamoOperationsAPI, error)) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dynamox restore <backup|pitr|status|resume> [options] [schema]")
	}
	fs := flag.NewFlagSet("restore "+args[0], flag.ContinueOnError)
	profile := fs.String("profile", "", "AWS shared config profile")
	region := fs.String("region", "", "AWS region (defaults to SDK configuration)")
	endpoint := fs.String("endpoint", "", "DynamoDB endpoint")
	manifestPath := fs.String("manifest", "", "on-demand backup manifest path")
	statePath := fs.String("state", "", "restore state path")
	suffix := fs.String("suffix", "", "suffix appended to restored table names")
	atText := fs.String("at", "", "PITR timestamp in RFC3339 format")
	writesPaused := fs.Bool("writes-paused", false, "confirm application writes are paused")
	timeout := fs.Duration("timeout", 2*time.Hour, "maximum operation duration")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("restore %s accepts at most one schema file", args[0])
	}
	switch args[0] {
	case "backup":
		if *manifestPath == "" || *statePath == "" || *suffix == "" {
			return errors.New("restore backup requires --manifest, --state, and --suffix")
		}
	case "pitr":
		if *statePath == "" || *suffix == "" || *atText == "" {
			return errors.New("restore pitr requires --state, --suffix, and --at")
		}
	case "status", "resume":
		if *statePath == "" {
			return fmt.Errorf("restore %s requires --state", args[0])
		}
	}
	path := "schema.dynamox"
	if fs.NArg() == 1 {
		path = fs.Arg(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	cfg, err := loadConfig(ctx, *profile, *region)
	if err != nil {
		return err
	}
	db, err := newClient(cfg, *endpoint)
	if err != nil {
		return err
	}
	switch args[0] {
	case "backup":
		_, s, err := read(path)
		if err != nil {
			return err
		}
		if err = validate(path, s); err != nil {
			return err
		}
		if err = validateRecoveryTables(*manifestPath, s); err != nil {
			return err
		}
		manifest, err := backup.StartBackupRestore(ctx, db, *manifestPath, *statePath, cfg.Region, *suffix, *writesPaused)
		if err != nil {
			return err
		}
		manifest, err = backup.WaitRestoreSet(ctx, db, *statePath, 10*time.Second)
		if err != nil {
			return err
		}
		if err := reconcileRestoredTables(ctx, db, s, manifest); err != nil {
			return err
		}
		return printJSON(struct {
			State      backup.Manifest   `json:"state"`
			TableNames map[string]string `json:"tableNames"`
		}{manifest, backup.TableMappings(manifest)})
	case "pitr":
		_, s, err := read(path)
		if err != nil {
			return err
		}
		if err = validate(path, s); err != nil {
			return err
		}
		at, err := time.Parse(time.RFC3339Nano, *atText)
		if err != nil {
			return fmt.Errorf("parse --at timestamp: %w", err)
		}
		manifest, err := backup.StartPITRRestore(ctx, db, *statePath, cfg.Region, *suffix, at, tableNames(provisionTables(s)), *writesPaused)
		if err != nil {
			return err
		}
		manifest, err = backup.WaitRestoreSet(ctx, db, *statePath, 10*time.Second)
		if err != nil {
			return err
		}
		if err := reconcileRestoredTables(ctx, db, s, manifest); err != nil {
			return err
		}
		return printJSON(struct {
			State      backup.Manifest   `json:"state"`
			TableNames map[string]string `json:"tableNames"`
		}{manifest, backup.TableMappings(manifest)})
	case "status":
		manifest, err := backup.RefreshRestoreStatus(ctx, db, *statePath)
		if err != nil {
			return err
		}
		return printJSON(struct {
			State      backup.Manifest   `json:"state"`
			TableNames map[string]string `json:"tableNames"`
		}{manifest, backup.TableMappings(manifest)})
	case "resume":
		_, s, err := read(path)
		if err != nil {
			return err
		}
		if err = validate(path, s); err != nil {
			return err
		}
		if err = validateRecoveryTables(*statePath, s); err != nil {
			return err
		}
		manifest, err := backup.ResumeRestore(ctx, db, *statePath, *writesPaused)
		if err != nil {
			return err
		}
		manifest, err = backup.WaitRestoreSet(ctx, db, *statePath, 10*time.Second)
		if err != nil {
			return err
		}
		if err := reconcileRestoredTables(ctx, db, s, manifest); err != nil {
			return err
		}
		return printJSON(struct {
			State      backup.Manifest   `json:"state"`
			TableNames map[string]string `json:"tableNames"`
		}{manifest, backup.TableMappings(manifest)})
	default:
		return fmt.Errorf("unknown restore command %q", args[0])
	}
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func tableNames(tables []provision.Table) []string {
	names := make([]string, 0, len(tables))
	for _, table := range tables {
		names = append(names, table.Name)
	}
	return backup.UniqueTables(names)
}

func validateRecoveryTables(path string, s *schema.Schema) error {
	manifest, err := backup.Load(path)
	if err != nil {
		return err
	}
	expected := tableNames(provisionTables(s))
	actual := make([]string, 0, len(manifest.Tables))
	for _, table := range manifest.Tables {
		actual = append(actual, table.SourceTable)
	}
	actual = backup.UniqueTables(actual)
	if len(actual) != len(expected) {
		return fmt.Errorf("recovery table set in %s does not match the schema", path)
	}
	for i := range expected {
		if actual[i] != expected[i] {
			return fmt.Errorf("recovery table set in %s does not match the schema", path)
		}
	}
	return nil
}

func reconcileRestoredTables(ctx context.Context, db provision.API, s *schema.Schema, manifest backup.Manifest) error {
	mappings := backup.TableMappings(manifest)
	tables := provisionTables(s)
	for i := range tables {
		if target := mappings[tables[i].Name]; target != "" {
			tables[i].Name = target
		}
	}
	_, err := provision.Sync(ctx, db, tables)
	return err
}

func provisionTables(s *schema.Schema) []provision.Table {
	r := make([]provision.Table, 0, len(s.Models))
	needsSequences := false
	pitrByTable := map[string]*provision.PITRConfig{}
	for _, m := range s.Models {
		t := provision.Table{
			Name: m.Table, Partition: provision.Key{Name: m.Partition, Type: scalar(m.Field(m.Partition)), Kind: "HASH"},
			ReadCapacityUnits: capacityValue(m.ReadCapacityUnits), WriteCapacityUnits: capacityValue(m.WriteCapacityUnits), TTL: m.TTL,
		}
		if m.PITR != nil {
			t.PITR = &provision.PITRConfig{Enabled: m.PITR.Enabled, RecoveryDays: m.PITR.RecoveryDays}
			pitrByTable[m.Table] = t.PITR
		}
		if key := m.Field(m.Partition); key != nil && key.Default == "serial" {
			needsSequences = true
		}
		if m.Sort != "" {
			t.Sort = provision.Key{Name: m.Sort, Type: scalar(m.Field(m.Sort)), Kind: "RANGE"}
		}
		for _, i := range m.Indexes {
			partition := i.Partition
			if strings.EqualFold(i.Kind, "LSI") && partition == "" {
				partition = m.Partition
			}
			ix := provision.Index{
				Name: i.Name, Local: strings.EqualFold(i.Kind, "LSI"), Partition: provision.Key{Name: partition, Type: scalar(m.Field(partition)), Kind: "HASH"},
				ReadCapacityUnits: capacityValue(i.ReadCapacityUnits), WriteCapacityUnits: capacityValue(i.WriteCapacityUnits),
			}
			if i.Sort != "" {
				ix.Sort = provision.Key{Name: i.Sort, Type: scalar(m.Field(i.Sort)), Kind: "RANGE"}
			}
			t.Indexes = append(t.Indexes, ix)
		}
		r = append(r, t)
	}
	if needsSequences {
		sequence := provision.Table{Name: runtime.SequenceTableName, Partition: provision.Key{Name: "name", Type: types.ScalarAttributeTypeS, Kind: "HASH"}}
		sequencePITR := int32(0)
		for _, m := range s.Models {
			key := m.Field(m.Partition)
			if key == nil || key.Default != "serial" {
				continue
			}
			if policy := pitrByTable[m.Table]; policy != nil && policy.Enabled && policy.RecoveryDays > sequencePITR {
				sequencePITR = policy.RecoveryDays
			}
		}
		if sequencePITR > 0 {
			sequence.PITR = &provision.PITRConfig{Enabled: true, RecoveryDays: sequencePITR, PreserveLonger: true}
		}
		r = append(r, sequence)
	}
	return r
}
func scalar(f *schema.Field) types.ScalarAttributeType {
	if f == nil {
		return types.ScalarAttributeTypeS
	}
	switch f.Type {
	case "Int", "Float":
		return types.ScalarAttributeTypeN
	case "Bytes":
		return types.ScalarAttributeTypeB
	default:
		return types.ScalarAttributeTypeS
	}
}

func capacityValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
