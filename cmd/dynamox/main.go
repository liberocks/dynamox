package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/liberocks/dynamox/codegen"
	"github.com/liberocks/dynamox/provision"
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
		return fmt.Errorf("usage: dynamox <fmt|validate|generate|sync> [options] [schema]")
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
	case "help", "-h", "--help":
		fmt.Println("usage: dynamox <fmt|validate|generate|sync> [options] [schema]")
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
func read(path string) (string, *schema.Schema, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	s, err := schema.Parse(string(src))
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", path, err)
	}
	return string(src), s, nil
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
	if err = os.WriteFile(dest, data, 0644); err != nil {
		return err
	}
	fmt.Printf("generated %s\n", dest)
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
func provisionTables(s *schema.Schema) []provision.Table {
	r := make([]provision.Table, 0, len(s.Models))
	for _, m := range s.Models {
		t := provision.Table{Name: m.Table, Partition: provision.Key{Name: m.Partition, Type: scalar(m.Field(m.Partition)), Kind: "HASH"}}
		if m.Sort != "" {
			t.Sort = provision.Key{Name: m.Sort, Type: scalar(m.Field(m.Sort)), Kind: "RANGE"}
		}
		for _, i := range m.Indexes {
			partition := i.Partition
			if strings.EqualFold(i.Kind, "LSI") && partition == "" {
				partition = m.Partition
			}
			ix := provision.Index{Name: i.Name, Local: strings.EqualFold(i.Kind, "LSI"), Partition: provision.Key{Name: partition, Type: scalar(m.Field(partition)), Kind: "HASH"}}
			if i.Sort != "" {
				ix.Sort = provision.Key{Name: i.Sort, Type: scalar(m.Field(i.Sort)), Kind: "RANGE"}
			}
			t.Indexes = append(t.Indexes, ix)
		}
		r = append(r, t)
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
