// Package provision creates absent DynamoDB tables and checks existing schemas for drift.
package provision

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go"
)

type API interface {
	DescribeTable(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
	CreateTable(context.Context, *dynamodb.CreateTableInput, ...func(*dynamodb.Options)) (*dynamodb.CreateTableOutput, error)
	UpdateTable(context.Context, *dynamodb.UpdateTableInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateTableOutput, error)
	DescribeTimeToLive(context.Context, *dynamodb.DescribeTimeToLiveInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTimeToLiveOutput, error)
	UpdateTimeToLive(context.Context, *dynamodb.UpdateTimeToLiveInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateTimeToLiveOutput, error)
}
type Key struct {
	Name string
	Type types.ScalarAttributeType
	Kind types.KeyType
}
type Index struct {
	Name                                  string
	Partition, Sort                       Key
	Local                                 bool
	ReadCapacityUnits, WriteCapacityUnits int64
}
type Table struct {
	Name                                  string
	Partition, Sort                       Key
	Indexes                               []Index
	ReadCapacityUnits, WriteCapacityUnits int64
	TTL                                   string
}

// Sync creates missing tables and reconciles capacity and TTL on structurally matching tables.
func Sync(ctx context.Context, db API, tables []Table) ([]string, error) {
	created := []string{}
	pending := []string{}
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
			ttlPending, ttlErr := reconcileTTL(ctx, db, want)
			if ttlErr != nil {
				return created, ttlErr
			}
			if ttlPending {
				pending = append(pending, ttlPendingMessage(want.Name))
			}
			continue
		}
		if out.Table == nil {
			return created, fmt.Errorf("describe table %s returned no description", want.Name)
		}
		if err = waitActive(ctx, db, want.Name); err != nil {
			return created, err
		}
		active, err := describeTable(ctx, db, want.Name)
		if err != nil {
			return created, err
		}
		if ds := drift(want, active); len(ds) > 0 {
			return created, fmt.Errorf("table %s drift: %v", want.Name, ds)
		}
		if err = reconcileCapacity(ctx, db, want, active); err != nil {
			return created, err
		}
		ttlPending, ttlErr := reconcileTTL(ctx, db, want)
		if ttlErr != nil {
			return created, ttlErr
		}
		if ttlPending {
			pending = append(pending, ttlPendingMessage(want.Name))
		}
	}
	if len(pending) > 0 {
		return created, fmt.Errorf("%s", strings.Join(pending, "; "))
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
			var throughput *types.ProvisionedThroughput
			if t.ReadCapacityUnits > 0 && t.WriteCapacityUnits > 0 {
				throughput = provisionedThroughput(ix.ReadCapacityUnits, ix.WriteCapacityUnits)
			}
			gsis = append(gsis, types.GlobalSecondaryIndex{IndexName: aws.String(ix.Name), KeySchema: ks, Projection: projection, ProvisionedThroughput: throughput})
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
	billingMode := types.BillingModePayPerRequest
	if t.ReadCapacityUnits > 0 && t.WriteCapacityUnits > 0 {
		billingMode = types.BillingModeProvisioned
	}
	in := &dynamodb.CreateTableInput{TableName: aws.String(t.Name), AttributeDefinitions: defs, KeySchema: keySchema, BillingMode: billingMode, GlobalSecondaryIndexes: gsis, LocalSecondaryIndexes: lsis}
	if billingMode == types.BillingModeProvisioned {
		in.ProvisionedThroughput = provisionedThroughput(t.ReadCapacityUnits, t.WriteCapacityUnits)
	}
	if _, err := db.CreateTable(ctx, in); err != nil {
		return fmt.Errorf("create table %s: %w", t.Name, err)
	}
	return waitActive(ctx, db, t.Name)
}

func provisionedThroughput(read, write int64) *types.ProvisionedThroughput {
	return &types.ProvisionedThroughput{ReadCapacityUnits: aws.Int64(read), WriteCapacityUnits: aws.Int64(write)}
}

func describeTable(ctx context.Context, db API, name string) (*types.TableDescription, error) {
	out, err := db.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)})
	if err != nil {
		return nil, fmt.Errorf("describe table %s: %w", name, err)
	}
	if out.Table == nil {
		return nil, fmt.Errorf("describe table %s returned no description", name)
	}
	return out.Table, nil
}

func reconcileCapacity(ctx context.Context, db API, want Table, got *types.TableDescription) error {
	wantMode := types.BillingModePayPerRequest
	if want.ReadCapacityUnits > 0 && want.WriteCapacityUnits > 0 {
		wantMode = types.BillingModeProvisioned
	}
	currentMode := tableBillingMode(got)
	tableThroughputChanged := wantMode == types.BillingModeProvisioned && !sameThroughput(got.ProvisionedThroughput, want.ReadCapacityUnits, want.WriteCapacityUnits)
	if currentMode != wantMode || tableThroughputChanged {
		in := &dynamodb.UpdateTableInput{TableName: aws.String(want.Name)}
		if currentMode != wantMode {
			in.BillingMode = wantMode
		}
		if wantMode == types.BillingModeProvisioned {
			in.ProvisionedThroughput = provisionedThroughput(want.ReadCapacityUnits, want.WriteCapacityUnits)
			// DynamoDB requires initial throughput for every GSI in the same
			// request that switches an on-demand table to provisioned mode.
			if currentMode != wantMode {
				for _, ix := range want.Indexes {
					if ix.Local {
						continue
					}
					in.GlobalSecondaryIndexUpdates = append(in.GlobalSecondaryIndexUpdates, types.GlobalSecondaryIndexUpdate{
						Update: &types.UpdateGlobalSecondaryIndexAction{
							IndexName: aws.String(ix.Name), ProvisionedThroughput: provisionedThroughput(ix.ReadCapacityUnits, ix.WriteCapacityUnits),
						},
					})
				}
			}
		}
		if _, err := db.UpdateTable(ctx, in); err != nil {
			return fmt.Errorf("update table %s capacity: %w", want.Name, err)
		}
		if err := waitActive(ctx, db, want.Name); err != nil {
			return err
		}
		var err error
		got, err = describeTable(ctx, db, want.Name)
		if err != nil {
			return err
		}
	}
	if wantMode != types.BillingModeProvisioned {
		return nil
	}
	for _, ix := range want.Indexes {
		if ix.Local {
			continue
		}
		actual := globalIndex(got, ix.Name)
		if actual == nil {
			return fmt.Errorf("table %s is missing GSI %s while reconciling capacity", want.Name, ix.Name)
		}
		if sameThroughput(actual.ProvisionedThroughput, ix.ReadCapacityUnits, ix.WriteCapacityUnits) {
			continue
		}
		update := types.GlobalSecondaryIndexUpdate{Update: &types.UpdateGlobalSecondaryIndexAction{
			IndexName: aws.String(ix.Name), ProvisionedThroughput: provisionedThroughput(ix.ReadCapacityUnits, ix.WriteCapacityUnits),
		}}
		if _, err := db.UpdateTable(ctx, &dynamodb.UpdateTableInput{TableName: aws.String(want.Name), GlobalSecondaryIndexUpdates: []types.GlobalSecondaryIndexUpdate{update}}); err != nil {
			return fmt.Errorf("update table %s GSI %s capacity: %w", want.Name, ix.Name, err)
		}
		if err := waitActive(ctx, db, want.Name); err != nil {
			return err
		}
		var err error
		got, err = describeTable(ctx, db, want.Name)
		if err != nil {
			return err
		}
	}
	return nil
}

func tableBillingMode(got *types.TableDescription) types.BillingMode {
	if got.BillingModeSummary != nil && got.BillingModeSummary.BillingMode != "" {
		return got.BillingModeSummary.BillingMode
	}
	if got.ProvisionedThroughput != nil {
		return types.BillingModeProvisioned
	}
	return types.BillingModePayPerRequest
}

func sameThroughput(got *types.ProvisionedThroughputDescription, read, write int64) bool {
	return got != nil && aws.ToInt64(got.ReadCapacityUnits) == read && aws.ToInt64(got.WriteCapacityUnits) == write
}

func globalIndex(got *types.TableDescription, name string) *types.GlobalSecondaryIndexDescription {
	for i := range got.GlobalSecondaryIndexes {
		if aws.ToString(got.GlobalSecondaryIndexes[i].IndexName) == name {
			return &got.GlobalSecondaryIndexes[i]
		}
	}
	return nil
}

func reconcileTTL(ctx context.Context, db API, want Table) (bool, error) {
	described, err := db.DescribeTimeToLive(ctx, &dynamodb.DescribeTimeToLiveInput{TableName: aws.String(want.Name)})
	if err != nil {
		return false, fmt.Errorf("describe table %s TTL: %w", want.Name, err)
	}
	if described == nil {
		return false, fmt.Errorf("describe table %s TTL returned no description", want.Name)
	}
	description := described.TimeToLiveDescription
	status := types.TimeToLiveStatusDisabled
	attribute := ""
	if description != nil {
		status = description.TimeToLiveStatus
		attribute = aws.ToString(description.AttributeName)
		if status == "" {
			status = types.TimeToLiveStatusDisabled
		}
	}
	switch status {
	case types.TimeToLiveStatusEnabled:
		if want.TTL != "" && want.TTL == attribute {
			return false, nil
		}
		if attribute == "" {
			return false, fmt.Errorf("table %s TTL is enabled without an attribute name", want.Name)
		}
		if err := updateTTL(ctx, db, want.Name, attribute, false); err != nil {
			return false, err
		}
		return true, nil
	case types.TimeToLiveStatusDisabled:
		if want.TTL == "" {
			return false, nil
		}
		if err := updateTTL(ctx, db, want.Name, want.TTL, true); err != nil {
			return false, err
		}
		return true, nil
	case types.TimeToLiveStatusEnabling, types.TimeToLiveStatusDisabling:
		return true, nil
	default:
		return false, fmt.Errorf("table %s has unsupported TTL status %q", want.Name, status)
	}
}

func updateTTL(ctx context.Context, db API, table, attribute string, enabled bool) error {
	_, err := db.UpdateTimeToLive(ctx, &dynamodb.UpdateTimeToLiveInput{
		TableName:               aws.String(table),
		TimeToLiveSpecification: &types.TimeToLiveSpecification{AttributeName: aws.String(attribute), Enabled: aws.Bool(enabled)},
	})
	if err != nil {
		var apiError smithy.APIError
		if errors.As(err, &apiError) && apiError.ErrorCode() == "ValidationException" && strings.Contains(strings.ToLower(apiError.ErrorMessage()), "fixed interval") {
			return fmt.Errorf("update table %s TTL: DynamoDB allows one TTL configuration update per hour; wait for the update window to pass, then rerun dynamox sync", table)
		}
		return fmt.Errorf("update table %s TTL: %w", table, err)
	}
	return nil
}

func ttlPendingMessage(table string) string {
	return fmt.Sprintf("table %s TTL change is in progress; rerun dynamox sync after it completes", table)
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
