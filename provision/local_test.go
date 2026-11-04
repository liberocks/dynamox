package provision

import (
	"context"
	"fmt"
	"os"
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
