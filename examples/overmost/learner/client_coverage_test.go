package learner

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/liberocks/dynamox/runtime"
)

type coverageDynamo struct {
	runtime.DynamoAPI
	err       error
	item      map[string]types.AttributeValue
	lastTable string
}

func (d *coverageDynamo) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	d.lastTable = aws.ToString(in.TableName)
	if d.err != nil {
		return nil, d.err
	}
	return &dynamodb.GetItemOutput{Item: d.item}, nil
}
func (d *coverageDynamo) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	d.lastTable = aws.ToString(in.TableName)
	return &dynamodb.PutItemOutput{}, d.err
}
func (d *coverageDynamo) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	d.lastTable = aws.ToString(in.TableName)
	if d.err != nil {
		return nil, d.err
	}
	return &dynamodb.UpdateItemOutput{Attributes: d.item}, nil
}
func (d *coverageDynamo) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	d.lastTable = aws.ToString(in.TableName)
	return &dynamodb.DeleteItemOutput{}, d.err
}
func (d *coverageDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	d.lastTable = aws.ToString(in.TableName)
	if d.err != nil {
		return nil, d.err
	}
	return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{d.item}}, nil
}
func (d *coverageDynamo) Scan(_ context.Context, in *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	d.lastTable = aws.ToString(in.TableName)
	if d.err != nil {
		return nil, d.err
	}
	return &dynamodb.ScanOutput{Items: []map[string]types.AttributeValue{d.item}}, nil
}
func (d *coverageDynamo) TransactGetItems(_ context.Context, in *dynamodb.TransactGetItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactGetItemsOutput, error) {
	if d.err != nil {
		return nil, d.err
	}
	out := &dynamodb.TransactGetItemsOutput{}
	for range in.TransactItems {
		out.Responses = append(out.Responses, types.ItemResponse{Item: d.item})
	}
	return out, nil
}
func (d *coverageDynamo) TransactWriteItems(_ context.Context, _ *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	return &dynamodb.TransactWriteItemsOutput{}, d.err
}

func TestGeneratedLearnerClientCoverage(t *testing.T) {
	ctx := context.Background()
	content := Content{PK: "EXAM#one", SK: "QUIZ#one", Status: "published", Sections: []Section{{ID: "one", Prompt: "Prompt", Choices: []string{"A", "B"}}}, ProviderPayload: runtime.JSON(map[string]any{"source": "test"})}
	item, err := encodeContent(content)
	if err != nil {
		t.Fatal(err)
	}
	db := &coverageDynamo{item: item}
	client := NewClient(db)
	_ = NewClientWithIDGenerator(db, nil)
	client = NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"learner-content": "learner-override"}})
	key := ContentKey{PK: content.PK, SK: content.SK}
	condition := runtime.WriteOptions{Condition: &runtime.Expression{Text: "#status = :status", Names: map[string]string{"#status": "status"}, Values: map[string]any{":status": "published"}}}
	if _, err := client.CreateContent(ctx, content); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateContentWithOptions(ctx, content, condition); err != nil {
		t.Fatal(err)
	}
	if err := client.PutContent(ctx, content); err != nil {
		t.Fatal(err)
	}
	if err := client.PutContent(ctx, content, condition); err != nil {
		t.Fatal(err)
	}
	if _, found, err := client.GetContent(ctx, key); err != nil || !found {
		t.Fatalf("GetContent found=%v err=%v", found, err)
	}
	if _, found, err := client.GetContentWithOptions(ctx, key, runtime.ReadOptions{ConsistentRead: true}); err != nil || !found {
		t.Fatalf("GetContentWithOptions found=%v err=%v", found, err)
	}
	if db.lastTable != "learner-override" {
		t.Fatalf("table override was not used: %q", db.lastTable)
	}
	if err := client.DeleteContent(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteContentWithOptions(ctx, key, condition); err != nil {
		t.Fatal(err)
	}
	if page, err := client.ScanContent(ctx, runtime.PageRequest{Limit: 3}); err != nil || len(page.Items) != 1 {
		t.Fatalf("ScanContent page=%#v err=%v", page, err)
	}
	if page, err := client.ScanContentWithOptions(ctx, runtime.ScanOptions{Page: runtime.PageRequest{Limit: 3}, Filter: condition.Condition}); err != nil || len(page.Items) != 1 {
		t.Fatalf("ScanContentWithOptions page=%#v err=%v", page, err)
	}
	status := "archived"
	sections := []Section{{ID: "two", Prompt: "Next", Choices: []string{"C"}}}
	payload := runtime.JSON(map[string]any{"source": "update"})
	patch := ContentUpdate{SetStatus: &status, SetSections: &sections, SetProviderPayload: &payload}
	if err := client.UpdateContent(ctx, key, patch); err != nil {
		t.Fatal(err)
	}
	if err := client.UpdateContentWithOptions(ctx, key, patch, condition); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateContentReturning(ctx, key, patch); err != nil {
		t.Fatal(err)
	}
	var destination Content
	read, err := client.ReadContent(key, &destination)
	if err != nil {
		t.Fatal(err)
	}
	write, err := client.WriteContent(content)
	if err != nil {
		t.Fatal(err)
	}
	writeWithOptions, err := client.WriteContentWithOptions(content, condition)
	if err != nil {
		t.Fatal(err)
	}
	create, err := client.CreateContentOperation(content)
	if err != nil {
		t.Fatal(err)
	}
	createWithOptions, err := client.CreateContentOperation(content, condition)
	if err != nil {
		t.Fatal(err)
	}
	delete, err := client.DeleteContentOperation(key)
	if err != nil {
		t.Fatal(err)
	}
	deleteWithOptions, err := client.DeleteContentOperationWithOptions(key, condition)
	if err != nil {
		t.Fatal(err)
	}
	update, err := client.UpdateContentOperation(key, patch)
	if err != nil {
		t.Fatal(err)
	}
	updateWithOptions, err := client.UpdateContentOperationWithOptions(key, patch, condition)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.QueryContentByPK(ctx, key.PK, nil, runtime.PageRequest{Limit: 3}); err != nil {
		t.Fatal(err)
	}
	if err := client.TransactGet(ctx, read); err != nil {
		t.Fatal(err)
	}
	if err := client.TransactWrite(ctx, write, writeWithOptions, create, createWithOptions, delete, deleteWithOptions, update, updateWithOptions); err != nil {
		t.Fatal(err)
	}
	bad := runtime.JSON(make(chan int))
	badContent := content
	badContent.ProviderPayload = bad
	if _, err := client.CreateContent(ctx, badContent); err == nil {
		t.Fatal("CreateContent should return JSON encoding error")
	}
	if err := client.PutContent(ctx, badContent); err == nil {
		t.Fatal("PutContent should return JSON encoding error")
	}
	badPatch := ContentUpdate{SetProviderPayload: &bad}
	if err := client.UpdateContent(ctx, key, badPatch); err == nil {
		t.Fatal("UpdateContent should return JSON encoding error")
	}
	if _, err := client.UpdateContentReturning(ctx, key, badPatch); err == nil {
		t.Fatal("UpdateContentReturning should return JSON encoding error")
	}
	if _, err := client.UpdateContentOperation(key, badPatch); err == nil {
		t.Fatal("UpdateContentOperation should return JSON encoding error")
	}
	db.err = errors.New("offline")
	if _, err := client.CreateContent(ctx, content); err == nil {
		t.Fatal("CreateContent should propagate errors")
	}
	if err := client.PutContent(ctx, content); err == nil {
		t.Fatal("PutContent should propagate errors")
	}
	if _, _, err := client.GetContent(ctx, key); err == nil {
		t.Fatal("GetContent should propagate errors")
	}
	if err := client.DeleteContent(ctx, key); err == nil {
		t.Fatal("DeleteContent should propagate errors")
	}
	if _, err := client.ScanContent(ctx, runtime.PageRequest{}); err == nil {
		t.Fatal("ScanContent should propagate errors")
	}
	if err := client.UpdateContent(ctx, key, patch); err == nil {
		t.Fatal("UpdateContent should propagate errors")
	}
	if _, err := client.UpdateContentReturning(ctx, key, patch); err == nil {
		t.Fatal("UpdateContentReturning should propagate errors")
	}
	if err := client.TransactGet(ctx, read); err == nil {
		t.Fatal("TransactGet should propagate errors")
	}
	if err := client.TransactWrite(ctx, write); err == nil {
		t.Fatal("TransactWrite should propagate errors")
	}
	db.err = nil
	db.item = map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: "x"}, "SK": &types.AttributeValueMemberS{Value: "y"}, "providerPayload": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"nested": nil}}}
	if _, _, err := client.GetContent(ctx, ContentKey{PK: "x", SK: "y"}); err == nil {
		t.Fatal("malformed Json should be rejected")
	}
}
