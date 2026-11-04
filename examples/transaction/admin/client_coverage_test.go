package admin

import (
	"context"
	"errors"
	"testing"
	"time"

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

func TestGeneratedAdminClientCoverage(t *testing.T) {
	ctx := context.Background()
	item := map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: "EXAM#one"}, "SK": &types.AttributeValueMemberS{Value: "DRAFT#one"},
		"status": &types.AttributeValueMemberS{Value: "unpublished"}, "updatedAt": &types.AttributeValueMemberS{Value: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)},
	}
	db := &coverageDynamo{item: item}
	client := NewClient(db)
	_ = NewClientWithIDGenerator(db, nil)
	client = NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"admin-drafts": "admin-override"}})
	draft := Draft{PK: "EXAM#one", SK: "DRAFT#one", Status: "unpublished", UpdatedAt: time.Now().UTC()}
	condition := runtime.WriteOptions{Condition: &runtime.Expression{Text: "#status = :status", Names: map[string]string{"#status": "status"}, Values: map[string]any{":status": "unpublished"}}}
	if _, err := client.CreateDraft(ctx, draft); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateDraftWithOptions(ctx, draft, condition); err != nil {
		t.Fatal(err)
	}
	if err := client.PutDraft(ctx, draft); err != nil {
		t.Fatal(err)
	}
	if err := client.PutDraft(ctx, draft, condition); err != nil {
		t.Fatal(err)
	}
	key := DraftKey{PK: draft.PK, SK: draft.SK}
	if _, found, err := client.GetDraft(ctx, key); err != nil || !found {
		t.Fatalf("GetDraft found=%v err=%v", found, err)
	}
	if _, found, err := client.GetDraftWithOptions(ctx, key, runtime.ReadOptions{ConsistentRead: true}); err != nil || !found {
		t.Fatalf("GetDraftWithOptions found=%v err=%v", found, err)
	}
	if db.lastTable != "admin-override" {
		t.Fatalf("table override was not used: %q", db.lastTable)
	}
	if err := client.DeleteDraft(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteDraftWithOptions(ctx, key, condition); err != nil {
		t.Fatal(err)
	}
	if page, err := client.ScanDraft(ctx, runtime.PageRequest{Limit: 3}); err != nil || len(page.Items) != 1 {
		t.Fatalf("ScanDraft page=%#v err=%v", page, err)
	}
	if page, err := client.ScanDraftWithOptions(ctx, runtime.ScanOptions{Page: runtime.PageRequest{Limit: 3}, Filter: condition.Condition}); err != nil || len(page.Items) != 1 {
		t.Fatalf("ScanDraftWithOptions page=%#v err=%v", page, err)
	}
	status := "published"
	updatedAt := time.Now().UTC()
	patch := DraftUpdate{SetStatus: &status, SetUpdatedAt: &updatedAt}
	if err := client.UpdateDraft(ctx, key, patch); err != nil {
		t.Fatal(err)
	}
	if err := client.UpdateDraftWithOptions(ctx, key, patch, condition); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateDraftReturning(ctx, key, patch); err != nil {
		t.Fatal(err)
	}
	var destination Draft
	read, err := client.ReadDraft(key, &destination)
	if err != nil {
		t.Fatal(err)
	}
	write, err := client.WriteDraft(draft)
	if err != nil {
		t.Fatal(err)
	}
	writeWithOptions, err := client.WriteDraftWithOptions(draft, condition)
	if err != nil {
		t.Fatal(err)
	}
	create, err := client.CreateDraftOperation(draft)
	if err != nil {
		t.Fatal(err)
	}
	createWithOptions, err := client.CreateDraftOperation(draft, condition)
	if err != nil {
		t.Fatal(err)
	}
	delete, err := client.DeleteDraftOperation(key)
	if err != nil {
		t.Fatal(err)
	}
	deleteWithOptions, err := client.DeleteDraftOperationWithOptions(key, condition)
	if err != nil {
		t.Fatal(err)
	}
	update, err := client.UpdateDraftOperation(key, patch)
	if err != nil {
		t.Fatal(err)
	}
	updateWithOptions, err := client.UpdateDraftOperationWithOptions(key, patch, condition)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.QueryDraftByPK(ctx, key.PK, nil, runtime.PageRequest{Limit: 3}); err != nil {
		t.Fatal(err)
	}
	if err := client.TransactGet(ctx, read); err != nil {
		t.Fatal(err)
	}
	if err := client.TransactWrite(ctx, write, writeWithOptions, create, createWithOptions, delete, deleteWithOptions, update, updateWithOptions); err != nil {
		t.Fatal(err)
	}
	db.err = errors.New("offline")
	if _, err := client.CreateDraft(ctx, draft); err == nil {
		t.Fatal("CreateDraft should propagate errors")
	}
	if err := client.PutDraft(ctx, draft); err == nil {
		t.Fatal("PutDraft should propagate errors")
	}
	if _, _, err := client.GetDraft(ctx, key); err == nil {
		t.Fatal("GetDraft should propagate errors")
	}
	if err := client.DeleteDraft(ctx, key); err == nil {
		t.Fatal("DeleteDraft should propagate errors")
	}
	if _, err := client.ScanDraft(ctx, runtime.PageRequest{}); err == nil {
		t.Fatal("ScanDraft should propagate errors")
	}
	if err := client.UpdateDraft(ctx, key, patch); err == nil {
		t.Fatal("UpdateDraft should propagate errors")
	}
	if _, err := client.UpdateDraftReturning(ctx, key, patch); err == nil {
		t.Fatal("UpdateDraftReturning should propagate errors")
	}
	if err := client.TransactGet(ctx, read); err == nil {
		t.Fatal("TransactGet should propagate errors")
	}
	if err := client.TransactWrite(ctx, write); err == nil {
		t.Fatal("TransactWrite should propagate errors")
	}
	db.err = nil
	db.item = map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: "x"}, "SK": &types.AttributeValueMemberS{Value: "y"}, "updatedAt": &types.AttributeValueMemberS{Value: "bad"}}
	if _, _, err := client.GetDraft(ctx, DraftKey{PK: "x", SK: "y"}); err == nil {
		t.Fatal("malformed date should be rejected")
	}
}
