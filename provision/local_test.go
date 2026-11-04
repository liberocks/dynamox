package provision

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestSyncAgainstDynamoDBLocal(t *testing.T) {
	endpoint := os.Getenv("DYNAMODB_ENDPOINT")
	if endpoint == "" {
		t.Skip("set DYNAMODB_ENDPOINT to run DynamoDB Local integration test")
	}
	db := dynamodb.New(dynamodb.Options{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("local", "local", ""), BaseEndpoint: aws.String(endpoint)})
	table := Table{Name: fmt.Sprintf("dynamox_it_%d", time.Now().UnixNano()), Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}}
	created, err := Sync(context.Background(), db, []Table{table})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 {
		t.Fatalf("expected new local table, got %v", created)
	}
	defer db.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table.Name)})
	created, err = Sync(context.Background(), db, []Table{table})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 0 {
		t.Fatalf("second sync created tables: %v", created)
	}
}

func TestSchemaChangesPreserveExistingLocalItemsWithoutMigration(t *testing.T) {
	endpoint := os.Getenv("DYNAMODB_ENDPOINT")
	if endpoint == "" {
		t.Skip("set DYNAMODB_ENDPOINT to run DynamoDB Local integration test")
	}
	db := dynamodb.New(dynamodb.Options{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("local", "local", ""), BaseEndpoint: aws.String(endpoint)})
	table := Table{Name: fmt.Sprintf("dynamox_migration_it_%d", time.Now().UnixNano()), Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}}
	defer db.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table.Name)})
	created, err := Sync(context.Background(), db, []Table{table})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 {
		t.Fatalf("expected new local table, got %v", created)
	}
	_, err = db.PutItem(context.Background(), &dynamodb.PutItemInput{
		TableName: aws.String(table.Name),
		Item: map[string]types.AttributeValue{
			"id":    &types.AttributeValueMemberS{Value: "existing"},
			"value": &types.AttributeValueMemberS{Value: "keep-me"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	indexChange := table
	indexChange.Indexes = []Index{{Name: "ByValue", Partition: Key{Name: "value", Type: types.ScalarAttributeTypeS}}}
	if _, err = Sync(context.Background(), db, []Table{indexChange}); err == nil || !strings.Contains(err.Error(), "missing GSI ByValue") {
		t.Fatalf("expected index drift error, got %v", err)
	}
	keyChange := table
	keyChange.Partition = Key{Name: "newID", Type: types.ScalarAttributeTypeS}
	if _, err = Sync(context.Background(), db, []Table{keyChange}); err == nil || !strings.Contains(err.Error(), "primary key schema differs") {
		t.Fatalf("expected primary-key drift error, got %v", err)
	}

	stored, err := db.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: aws.String(table.Name),
		Key:       map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: "existing"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	value, ok := stored.Item["value"].(*types.AttributeValueMemberS)
	if !ok || value.Value != "keep-me" {
		t.Fatalf("existing item changed after schema drift checks: %#v", stored.Item)
	}
	description, err := db.DescribeTable(context.Background(), &dynamodb.DescribeTableInput{TableName: aws.String(table.Name)})
	if err != nil {
		t.Fatal(err)
	}
	if len(description.Table.GlobalSecondaryIndexes) != 0 || aws.ToString(description.Table.KeySchema[0].AttributeName) != "id" {
		t.Fatalf("existing table layout changed after schema drift checks: %#v", description.Table)
	}

	renamedTable := table
	renamedTable.Name += "_v2"
	defer db.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(renamedTable.Name)})
	created, err = Sync(context.Background(), db, []Table{renamedTable})
	if err != nil || len(created) != 1 || created[0] != renamedTable.Name {
		t.Fatalf("renamed table should be created separately: created=%v err=%v", created, err)
	}
	newTableItem, err := db.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: aws.String(renamedTable.Name),
		Key:       map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: "existing"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(newTableItem.Item) != 0 {
		t.Fatalf("renamed table unexpectedly copied data: %#v", newTableItem.Item)
	}
}
