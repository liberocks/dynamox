package source

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

func TestGeneratedSourceClientCoverage(t *testing.T) {
	ctx := context.Background()
	item := map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: "ENTITY#one"}, "SK": &types.AttributeValueMemberS{Value: "SOURCE#one"},
		"status": &types.AttributeValueMemberS{Value: "pending"}, "updatedAt": &types.AttributeValueMemberS{Value: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)},
	}
	db := &coverageDynamo{item: item}
	client := NewClient(db)
	_ = NewClientWithIDGenerator(db, nil)
	client = NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"source-records": "source-override"}})
	pending := SourceRecord{PK: "ENTITY#one", SK: "SOURCE#one", Status: "pending", UpdatedAt: time.Now().UTC()}
	condition := runtime.WriteOptions{Condition: &runtime.Expression{Text: "#status = :status", Names: map[string]string{"#status": "status"}, Values: map[string]any{":status": "pending"}}}
	if _, err := client.CreateSourceRecord(ctx, pending); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateSourceRecordWithOptions(ctx, pending, condition); err != nil {
		t.Fatal(err)
	}
	if err := client.PutSourceRecord(ctx, pending); err != nil {
		t.Fatal(err)
	}
	if err := client.PutSourceRecord(ctx, pending, condition); err != nil {
		t.Fatal(err)
	}
	key := SourceRecordKey{PK: pending.PK, SK: pending.SK}
	if _, found, err := client.GetSourceRecord(ctx, key); err != nil || !found {
		t.Fatalf("GetSourceRecord found=%v err=%v", found, err)
	}
	if _, found, err := client.GetSourceRecordWithOptions(ctx, key, runtime.ReadOptions{ConsistentRead: true}); err != nil || !found {
		t.Fatalf("GetSourceRecordWithOptions found=%v err=%v", found, err)
	}
	if db.lastTable != "source-override" {
		t.Fatalf("table override was not used: %q", db.lastTable)
	}
	if err := client.DeleteSourceRecord(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteSourceRecordWithOptions(ctx, key, condition); err != nil {
		t.Fatal(err)
	}
	if page, err := client.ScanSourceRecord(ctx, runtime.PageRequest{Limit: 3}); err != nil || len(page.Items) != 1 {
		t.Fatalf("ScanSourceRecord page=%#v err=%v", page, err)
	}
	if page, err := client.ScanSourceRecordWithOptions(ctx, runtime.ScanOptions{Page: runtime.PageRequest{Limit: 3}, Filter: condition.Condition}); err != nil || len(page.Items) != 1 {
		t.Fatalf("ScanSourceRecordWithOptions page=%#v err=%v", page, err)
	}
	status := "processed"
	updatedAt := time.Now().UTC()
	patch := SourceRecordUpdate{SetStatus: &status, SetUpdatedAt: &updatedAt}
	if err := client.UpdateSourceRecord(ctx, key, patch); err != nil {
		t.Fatal(err)
	}
	if err := client.UpdateSourceRecordWithOptions(ctx, key, patch, condition); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateSourceRecordReturning(ctx, key, patch); err != nil {
		t.Fatal(err)
	}
	var destination SourceRecord
	read, err := client.ReadSourceRecord(key, &destination)
	if err != nil {
		t.Fatal(err)
	}
	write, err := client.WriteSourceRecord(pending)
	if err != nil {
		t.Fatal(err)
	}
	writeWithOptions, err := client.WriteSourceRecordWithOptions(pending, condition)
	if err != nil {
		t.Fatal(err)
	}
	create, err := client.CreateSourceRecordOperation(pending)
	if err != nil {
		t.Fatal(err)
	}
	createWithOptions, err := client.CreateSourceRecordOperation(pending, condition)
	if err != nil {
		t.Fatal(err)
	}
	delete, err := client.DeleteSourceRecordOperation(key)
	if err != nil {
		t.Fatal(err)
	}
	deleteWithOptions, err := client.DeleteSourceRecordOperationWithOptions(key, condition)
	if err != nil {
		t.Fatal(err)
	}
	update, err := client.UpdateSourceRecordOperation(key, patch)
	if err != nil {
		t.Fatal(err)
	}
	updateWithOptions, err := client.UpdateSourceRecordOperationWithOptions(key, patch, condition)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.QuerySourceRecordByPK(ctx, key.PK, nil, runtime.PageRequest{Limit: 3}); err != nil {
		t.Fatal(err)
	}
	if err := client.TransactGet(ctx, read); err != nil {
		t.Fatal(err)
	}
	if err := client.TransactWrite(ctx, write, writeWithOptions, create, createWithOptions, delete, deleteWithOptions, update, updateWithOptions); err != nil {
		t.Fatal(err)
	}
	db.err = errors.New("offline")
	if _, err := client.CreateSourceRecord(ctx, pending); err == nil {
		t.Fatal("CreateSourceRecord should propagate errors")
	}
	if err := client.PutSourceRecord(ctx, pending); err == nil {
		t.Fatal("PutSourceRecord should propagate errors")
	}
	if _, _, err := client.GetSourceRecord(ctx, key); err == nil {
		t.Fatal("GetSourceRecord should propagate errors")
	}
	if err := client.DeleteSourceRecord(ctx, key); err == nil {
		t.Fatal("DeleteSourceRecord should propagate errors")
	}
	if _, err := client.ScanSourceRecord(ctx, runtime.PageRequest{}); err == nil {
		t.Fatal("ScanSourceRecord should propagate errors")
	}
	if err := client.UpdateSourceRecord(ctx, key, patch); err == nil {
		t.Fatal("UpdateSourceRecord should propagate errors")
	}
	if _, err := client.UpdateSourceRecordReturning(ctx, key, patch); err == nil {
		t.Fatal("UpdateSourceRecordReturning should propagate errors")
	}
	if err := client.TransactGet(ctx, read); err == nil {
		t.Fatal("TransactGet should propagate errors")
	}
	if err := client.TransactWrite(ctx, write); err == nil {
		t.Fatal("TransactWrite should propagate errors")
	}
	db.err = nil
	db.item = map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: "x"}, "SK": &types.AttributeValueMemberS{Value: "y"}, "updatedAt": &types.AttributeValueMemberS{Value: "bad"}}
	if _, _, err := client.GetSourceRecord(ctx, SourceRecordKey{PK: "x", SK: "y"}); err == nil {
		t.Fatal("malformed date should be rejected")
	}
}
