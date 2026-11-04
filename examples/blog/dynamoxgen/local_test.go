package dynamoxgen

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
	"github.com/liberocks/dynamox/runtime"
)

func TestDynamoDBLocalGeneratedTableOverridesReachCRUDQueryScanRelationsAndSerial(t *testing.T) {
	endpoint := os.Getenv("DYNAMODB_LOCAL_ENDPOINT")
	if endpoint == "" {
		t.Skip("set DYNAMODB_LOCAL_ENDPOINT to run DynamoDB Local integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db := dynamodb.New(dynamodb.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("local", "local", ""),
		BaseEndpoint: aws.String(endpoint),
	})
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	usersTable, postsTable := "dynamox_users_it_"+suffix, "dynamox_posts_it_"+suffix
	sequenceItemsTable, sequenceTable := "dynamox_sequence_items_it_"+suffix, "dynamox_sequences_it_"+suffix
	createLocalGeneratedTable(t, ctx, db, usersTable, &dynamodb.CreateTableInput{
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}},
	})
	createLocalGeneratedTable(t, ctx, db, postsTable, &dynamodb.CreateTableInput{
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("authorId"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("createdAt"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}},
		GlobalSecondaryIndexes: []types.GlobalSecondaryIndex{{
			IndexName: aws.String("ByAuthor"),
			KeySchema: []types.KeySchemaElement{
				{AttributeName: aws.String("authorId"), KeyType: types.KeyTypeHash},
				{AttributeName: aws.String("createdAt"), KeyType: types.KeyTypeRange},
			},
			Projection: &types.Projection{ProjectionType: types.ProjectionTypeAll},
		}},
	})
	createLocalGeneratedTable(t, ctx, db, sequenceItemsTable, &dynamodb.CreateTableInput{
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeN}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}},
	})
	createLocalGeneratedTable(t, ctx, db, sequenceTable, &dynamodb.CreateTableInput{
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("name"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("name"), KeyType: types.KeyTypeHash}},
	})

	client := NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{
		"users": usersTable, "posts": postsTable, "sequence_items": sequenceItemsTable,
		runtime.SequenceTableName: sequenceTable,
	}})
	user, err := client.CreateUser(ctx, User{Name: "Ada", Preferences: Preferences{Theme: "dark"}, Settings: Preferences{Theme: "light"}})
	if err != nil || user.ID == "" {
		t.Fatalf("create on overridden table: user=%#v err=%v", user, err)
	}
	got, found, err := client.GetUserWithOptions(ctx, UserKey{ID: user.ID}, runtime.ReadOptions{ConsistentRead: true})
	if err != nil || !found || got.Name != user.Name {
		t.Fatalf("consistent get on overridden table: user=%#v found=%v err=%v", got, found, err)
	}
	scan, err := client.ScanUserWithOptions(ctx, runtime.ScanOptions{
		Page:   runtime.PageRequest{Limit: 5, ConsistentRead: true},
		Filter: &runtime.Expression{Text: "#name = :name", Names: map[string]string{"#name": "name"}, Values: map[string]any{":name": "Ada"}},
	})
	if err != nil || len(scan.Items) != 1 || scan.Items[0].ID != user.ID {
		t.Fatalf("filtered scan on overridden table: page=%#v err=%v", scan, err)
	}
	nickname := "Countess"
	updated, err := client.UpdateUserReturning(ctx, UserKey{ID: user.ID}, UserUpdate{SetNickname: &nickname})
	if err != nil || updated.Nickname == nil || *updated.Nickname != nickname {
		t.Fatalf("ALL_NEW update on overridden table: user=%#v err=%v", updated, err)
	}
	post, err := client.CreatePost(ctx, Post{AuthorID: user.ID, Title: "Hello", CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	queried, err := client.QueryPostUsingByAuthor(ctx, user.ID, nil, runtime.PageRequest{Limit: 3})
	if err != nil || len(queried.Items) != 1 || queried.Items[0].ID != post.ID {
		t.Fatalf("GSI query on overridden table: page=%#v err=%v", queried, err)
	}
	related, err := client.GetUserWithPosts(ctx, UserKey{ID: user.ID}, runtime.PageRequest{Limit: 3})
	if err != nil || len(related.Posts.Items) != 1 || related.Posts.Items[0].ID != post.ID {
		t.Fatalf("relation query on overridden table: result=%#v err=%v", related, err)
	}
	serial, err := client.CreateSequenceItem(ctx, SequenceItem{Value: "counter override"})
	if err != nil || serial.ID < 1 {
		t.Fatalf("serial and model table overrides: item=%#v err=%v", serial, err)
	}
	if err := client.DeleteUserWithOptions(ctx, UserKey{ID: user.ID}, runtime.WriteOptions{Condition: &runtime.Expression{Text: "#name = :name", Names: map[string]string{"#name": "name"}, Values: map[string]any{":name": "Ada"}}}); err != nil {
		t.Fatal(err)
	}
}

func createLocalGeneratedTable(t *testing.T, ctx context.Context, db *dynamodb.Client, tableName string, input *dynamodb.CreateTableInput) {
	t.Helper()
	input.TableName = aws.String(tableName)
	input.BillingMode = types.BillingModePayPerRequest
	if _, err := db.CreateTable(ctx, input); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(tableName)})
	})
	if err := dynamodb.NewTableExistsWaiter(db).Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(tableName)}, 10*time.Second); err != nil {
		t.Fatal(err)
	}
}
