// Package provision creates absent DynamoDB tables and checks existing schemas for drift.
package provision

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type API interface {
	DescribeTable(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
	CreateTable(context.Context, *dynamodb.CreateTableInput, ...func(*dynamodb.Options)) (*dynamodb.CreateTableOutput, error)
}
type Key struct {
	Name string
	Type types.ScalarAttributeType
	Kind types.KeyType
}
type Index struct {
	Name            string
	Partition, Sort Key
	Local           bool
}
type Table struct {
	Name            string
	Partition, Sort Key
	Indexes         []Index
}

// Sync creates missing tables. For existing tables it only checks the declared key and index layout.
func Sync(ctx context.Context, db API, tables []Table) ([]string, error) {
	created := []string{}
	for _, want := range tables {
		out, err := db.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(want.Name)})
		if err != nil {
			var missing *types.ResourceNotFoundException
			if !errors.As(err, &missing) {
				return created, fmt.Errorf("describe table %s: %w", want.Name, err)
			}
			if err = create(ctx, db, want); err != nil {
				return created, err
			}
			created = append(created, want.Name)
			continue
		}
		if out.Table == nil {
			return created, fmt.Errorf("describe table %s returned no description", want.Name)
		}
		if err = waitActive(ctx, db, want.Name); err != nil {
			return created, err
		}
		if ds := drift(want, out.Table); len(ds) > 0 {
			return created, fmt.Errorf("table %s drift: %v", want.Name, ds)
		}
	}
	return created, nil
}
func create(ctx context.Context, db API, t Table) error {
	attrs := map[string]types.ScalarAttributeType{}
	put := func(k Key) { attrs[k.Name] = k.Type }
	put(t.Partition)
	if t.Sort.Name != "" {
		put(t.Sort)
	}
	keySchema := []types.KeySchemaElement{{AttributeName: aws.String(t.Partition.Name), KeyType: types.KeyTypeHash}}
	if t.Sort.Name != "" {
		keySchema = append(keySchema, types.KeySchemaElement{AttributeName: aws.String(t.Sort.Name), KeyType: types.KeyTypeRange})
	}
	var gsis []types.GlobalSecondaryIndex
	var lsis []types.LocalSecondaryIndex
	for _, ix := range t.Indexes {
		put(ix.Partition)
		if ix.Sort.Name != "" {
			put(ix.Sort)
		}
		ks := []types.KeySchemaElement{{AttributeName: aws.String(ix.Partition.Name), KeyType: types.KeyTypeHash}}
		if ix.Sort.Name != "" {
			ks = append(ks, types.KeySchemaElement{AttributeName: aws.String(ix.Sort.Name), KeyType: types.KeyTypeRange})
		}
		projection := &types.Projection{ProjectionType: types.ProjectionTypeAll}
		if ix.Local {
			lsis = append(lsis, types.LocalSecondaryIndex{IndexName: aws.String(ix.Name), KeySchema: ks, Projection: projection})
		} else {
			gsis = append(gsis, types.GlobalSecondaryIndex{IndexName: aws.String(ix.Name), KeySchema: ks, Projection: projection})
		}
	}
	names := make([]string, 0, len(attrs))
	for n := range attrs {
		names = append(names, n)
	}
	sort.Strings(names)
	defs := make([]types.AttributeDefinition, 0, len(attrs))
	for _, n := range names {
		defs = append(defs, types.AttributeDefinition{AttributeName: aws.String(n), AttributeType: attrs[n]})
	}
	in := &dynamodb.CreateTableInput{TableName: aws.String(t.Name), AttributeDefinitions: defs, KeySchema: keySchema, BillingMode: types.BillingModePayPerRequest, GlobalSecondaryIndexes: gsis, LocalSecondaryIndexes: lsis}
	if _, err := db.CreateTable(ctx, in); err != nil {
		return fmt.Errorf("create table %s: %w", t.Name, err)
	}
	return waitActive(ctx, db, t.Name)
}
func waitActive(ctx context.Context, db API, name string) error {
	return waitActiveWith(ctx, db, name, 5*time.Minute, 2*time.Second)
}
func waitActiveWith(ctx context.Context, db API, name string, timeout, interval time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		out, err := db.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)})
		if err != nil {
			return fmt.Errorf("wait for table %s: %w", name, err)
		}
		ready := out.Table != nil && out.Table.TableStatus == types.TableStatusActive
		if ready {
			for _, ix := range out.Table.GlobalSecondaryIndexes {
				if ix.IndexStatus != types.IndexStatusActive {
					ready = false
					break
				}
			}
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("wait for table %s: timed out", name)
		case <-tick.C:
		}
	}
}
func drift(want Table, got *types.TableDescription) []string {
	var out []string
	actual := map[string]types.KeyType{}
	actualTypes := map[string]types.ScalarAttributeType{}
	for _, k := range got.KeySchema {
		actual[aws.ToString(k.AttributeName)] = k.KeyType
	}
	for _, a := range got.AttributeDefinitions {
		actualTypes[aws.ToString(a.AttributeName)] = a.AttributeType
	}
	wantKey := map[string]types.KeyType{want.Partition.Name: types.KeyTypeHash}
	wantTypes := map[string]types.ScalarAttributeType{want.Partition.Name: want.Partition.Type}
	if want.Sort.Name != "" {
		wantKey[want.Sort.Name] = types.KeyTypeRange
		wantTypes[want.Sort.Name] = want.Sort.Type
	}
	for _, ix := range want.Indexes {
		wantTypes[ix.Partition.Name] = ix.Partition.Type
		if ix.Sort.Name != "" {
			wantTypes[ix.Sort.Name] = ix.Sort.Type
		}
	}
	if !reflect.DeepEqual(actual, wantKey) {
		out = append(out, "primary key schema differs")
	}
	for name, wantType := range wantTypes {
		if actualTypes[name] != wantType {
			out = append(out, "attribute type differs for "+name)
		}
	}
	gotIndexes := map[string]types.GlobalSecondaryIndexDescription{}
	for _, x := range got.GlobalSecondaryIndexes {
		if x.IndexName != nil {
			gotIndexes[*x.IndexName] = x
		}
	}
	gotLSIs := map[string]types.LocalSecondaryIndexDescription{}
	for _, x := range got.LocalSecondaryIndexes {
		if x.IndexName != nil {
			gotLSIs[*x.IndexName] = x
		}
	}
	for _, ix := range want.Indexes {
		var keys []types.KeySchemaElement
		var projection *types.Projection
		if ix.Local {
			if x, ok := gotLSIs[ix.Name]; ok {
				keys = x.KeySchema
				projection = x.Projection
			} else {
				out = append(out, "missing LSI "+ix.Name)
				continue
			}
		} else {
			if x, ok := gotIndexes[ix.Name]; ok {
				keys = x.KeySchema
				projection = x.Projection
			} else {
				out = append(out, "missing GSI "+ix.Name)
				continue
			}
		}
		expected := map[string]types.KeyType{ix.Partition.Name: types.KeyTypeHash}
		if ix.Sort.Name != "" {
			expected[ix.Sort.Name] = types.KeyTypeRange
		}
		actual := map[string]types.KeyType{}
		for _, k := range keys {
			actual[aws.ToString(k.AttributeName)] = k.KeyType
		}
		if !reflect.DeepEqual(actual, expected) {
			out = append(out, "index "+ix.Name+" key schema differs")
		}
		if projection == nil || projection.ProjectionType != types.ProjectionTypeAll {
			out = append(out, "index "+ix.Name+" must project all attributes")
		}
	}
	if len(gotIndexes)+len(gotLSIs) != len(want.Indexes) {
		out = append(out, "undeclared indexes exist")
	}
	sort.Strings(out)
	return out
}
