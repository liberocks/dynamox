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

type errorDescribe struct{ err error }

func (f *errorDescribe) DescribeTable(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
	return nil, f.err
}
func (f *errorDescribe) CreateTable(context.Context, *dynamodb.CreateTableInput, ...func(*dynamodb.Options)) (*dynamodb.CreateTableOutput, error) {
	return nil, nil
}
