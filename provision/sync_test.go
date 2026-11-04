package provision

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type provisionFake struct {
	description *types.TableDescription
	create      *dynamodb.CreateTableInput
	missing     bool
}

func (f *provisionFake) DescribeTable(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
	if f.missing {
		return nil, &types.ResourceNotFoundException{}
	}
	return &dynamodb.DescribeTableOutput{Table: f.description}, nil
}
func (f *provisionFake) CreateTable(_ context.Context, in *dynamodb.CreateTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.CreateTableOutput, error) {
	f.create = in
	f.missing = false
	f.description = &types.TableDescription{TableStatus: types.TableStatusActive, KeySchema: in.KeySchema, GlobalSecondaryIndexes: []types.GlobalSecondaryIndexDescription{}}
	return &dynamodb.CreateTableOutput{}, nil
}
func (f *provisionFake) UpdateTable(context.Context, *dynamodb.UpdateTableInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateTableOutput, error) {
	return &dynamodb.UpdateTableOutput{}, nil
}
func (f *provisionFake) DescribeTimeToLive(context.Context, *dynamodb.DescribeTimeToLiveInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTimeToLiveOutput, error) {
	return &dynamodb.DescribeTimeToLiveOutput{}, nil
}
func (f *provisionFake) UpdateTimeToLive(context.Context, *dynamodb.UpdateTimeToLiveInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateTimeToLiveOutput, error) {
	return &dynamodb.UpdateTimeToLiveOutput{}, nil
}

func TestSyncCreatesMissingTableWithAllProjection(t *testing.T) {
	db := &provisionFake{missing: true}
	want := Table{Name: "users", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}, Indexes: []Index{{Name: "ByEmail", Partition: Key{Name: "email", Type: types.ScalarAttributeTypeS}, Local: false}}}
	created, err := Sync(context.Background(), db, []Table{want})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || created[0] != "users" {
		t.Fatalf("created tables: %v", created)
	}
	if db.create == nil || db.create.BillingMode != types.BillingModePayPerRequest || len(db.create.GlobalSecondaryIndexes) != 1 {
		t.Fatalf("unexpected create input: %#v", db.create)
	}
	if len(db.create.AttributeDefinitions) != 2 {
		t.Fatalf("expected only the table and index partition keys: %#v", db.create.AttributeDefinitions)
	}
	for _, definition := range db.create.AttributeDefinitions {
		if aws.ToString(definition.AttributeName) == "" || definition.AttributeType == "" {
			t.Fatalf("empty attribute definition: %#v", definition)
		}
	}
	if db.create.GlobalSecondaryIndexes[0].Projection.ProjectionType != types.ProjectionTypeAll {
		t.Fatalf("expected ALL projection: %#v", db.create.GlobalSecondaryIndexes[0].Projection)
	}
}

func TestSyncReportsExistingTableDriftWithoutWriting(t *testing.T) {
	db := &provisionFake{description: &types.TableDescription{TableStatus: types.TableStatusActive, KeySchema: []types.KeySchemaElement{{AttributeName: aws.String("other"), KeyType: types.KeyTypeHash}}}}
	_, err := Sync(context.Background(), db, []Table{{Name: "users", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}}})
	if err == nil || db.create != nil {
		t.Fatalf("expected drift and no write, err=%v create=%#v", err, db.create)
	}
}

func TestSyncPropagatesDescribeErrors(t *testing.T) {
	db := &errorDescribe{err: errors.New("offline")}
	_, err := Sync(context.Background(), db, []Table{{Name: "x", Partition: Key{Name: "id"}}})
	if err == nil {
		t.Fatal("expected describe error")
	}
}

func TestSyncMergesSharedTableDeclarationsOnce(t *testing.T) {
	db := &provisionFake{missing: true}
	common := Key{Name: "id", Type: types.ScalarAttributeTypeS, Kind: types.KeyTypeHash}
	a := Table{Name: "shared", Partition: common, Indexes: []Index{{Name: "ByEmail", Partition: Key{Name: "email", Type: types.ScalarAttributeTypeS}}}}
	b := Table{Name: "shared", Partition: common, Indexes: []Index{{Name: "ByCreated", Partition: Key{Name: "created", Type: types.ScalarAttributeTypeS}}}}
	created, err := Sync(context.Background(), db, []Table{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || db.create == nil || len(db.create.GlobalSecondaryIndexes) != 2 {
		t.Fatalf("shared table was not reconciled once with both indexes: created=%v input=%#v", created, db.create)
	}
	b.Partition = Key{Name: "other", Type: types.ScalarAttributeTypeS}
	if _, err := mergeTables([]Table{a, b}); err == nil {
		t.Fatal("conflicting shared key declarations should fail")
	}
	if _, err := Sync(context.Background(), &provisionFake{}, []Table{a, b}); err == nil {
		t.Fatal("Sync should return conflicting shared key declarations")
	}
	b = a
	b.Indexes = []Index{{Name: "ByEmail", Partition: Key{Name: "different", Type: types.ScalarAttributeTypeS}}}
	if _, err := mergeTables([]Table{a, b}); err == nil {
		t.Fatal("conflicting shared index declarations should fail")
	}
	b = a
	b.TTL = "expires"
	a.TTL = "other"
	if _, err := mergeTables([]Table{a, b}); err == nil {
		t.Fatal("conflicting shared TTL declarations should fail")
	}
	b = a
	b.ReadCapacityUnits = 1
	if _, err := mergeTables([]Table{a, b}); err == nil {
		t.Fatal("conflicting shared capacity declarations should fail")
	}
	ordered, err := mergeTables([]Table{{Name: "a", Partition: common}, {Name: "b", Partition: common}})
	if err != nil || len(ordered) != 2 || ordered[0].Name != "a" || ordered[1].Name != "b" {
		t.Fatalf("distinct physical tables were not sorted deterministically: %#v err=%v", ordered, err)
	}
	ordered, err = mergeTables([]Table{{Name: "b", Partition: common}, {Name: "a", Partition: common}})
	if err != nil || len(ordered) != 2 || ordered[0].Name != "a" || ordered[1].Name != "b" {
		t.Fatalf("reversed physical tables were not sorted deterministically: %#v err=%v", ordered, err)
	}
}

type errorDescribe struct{ err error }

func (f *errorDescribe) DescribeTable(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
	return nil, f.err
}
func (f *errorDescribe) CreateTable(context.Context, *dynamodb.CreateTableInput, ...func(*dynamodb.Options)) (*dynamodb.CreateTableOutput, error) {
	return nil, nil
}
func (f *errorDescribe) UpdateTable(context.Context, *dynamodb.UpdateTableInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateTableOutput, error) {
	return nil, nil
}
func (f *errorDescribe) DescribeTimeToLive(context.Context, *dynamodb.DescribeTimeToLiveInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTimeToLiveOutput, error) {
	return nil, nil
}
func (f *errorDescribe) UpdateTimeToLive(context.Context, *dynamodb.UpdateTimeToLiveInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateTimeToLiveOutput, error) {
	return nil, nil
}
