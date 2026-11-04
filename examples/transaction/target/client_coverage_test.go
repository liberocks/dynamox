package target

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

func TestGeneratedTargetClientCoverage(t *testing.T) {
	ctx := context.Background()
	targetRecord := TargetRecord{PK: "ENTITY#one", SK: "TARGET#one", Status: "processed", Details: []Detail{{Name: "detail one", Values: []string{"A", "B"}}}, Payload: runtime.JSON(map[string]any{"origin": "test"})}
	item, err := encodeTargetRecord(targetRecord)
	if err != nil {
		t.Fatal(err)
	}
	db := &coverageDynamo{item: item}
	client := NewClient(db)
	_ = NewClientWithIDGenerator(db, nil)
	client = NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"target-records": "target-override"}})
	key := TargetRecordKey{PK: targetRecord.PK, SK: targetRecord.SK}
	condition := runtime.WriteOptions{Condition: &runtime.Expression{Text: "#status = :status", Names: map[string]string{"#status": "status"}, Values: map[string]any{":status": "processed"}}}
	if _, err := client.CreateTargetRecord(ctx, targetRecord); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateTargetRecordWithOptions(ctx, targetRecord, condition); err != nil {
		t.Fatal(err)
	}
	if err := client.PutTargetRecord(ctx, targetRecord); err != nil {
		t.Fatal(err)
	}
	if err := client.PutTargetRecord(ctx, targetRecord, condition); err != nil {
		t.Fatal(err)
	}
	if _, found, err := client.GetTargetRecord(ctx, key); err != nil || !found {
		t.Fatalf("GetTargetRecord found=%v err=%v", found, err)
	}
	if _, found, err := client.GetTargetRecordWithOptions(ctx, key, runtime.ReadOptions{ConsistentRead: true}); err != nil || !found {
		t.Fatalf("GetTargetRecordWithOptions found=%v err=%v", found, err)
	}
	if db.lastTable != "target-override" {
		t.Fatalf("table override was not used: %q", db.lastTable)
	}
	if err := client.DeleteTargetRecord(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteTargetRecordWithOptions(ctx, key, condition); err != nil {
		t.Fatal(err)
	}
	if page, err := client.ScanTargetRecord(ctx, runtime.PageRequest{Limit: 3}); err != nil || len(page.Items) != 1 {
		t.Fatalf("ScanTargetRecord page=%#v err=%v", page, err)
	}
	if page, err := client.ScanTargetRecordWithOptions(ctx, runtime.ScanOptions{Page: runtime.PageRequest{Limit: 3}, Filter: condition.Condition}); err != nil || len(page.Items) != 1 {
		t.Fatalf("ScanTargetRecordWithOptions page=%#v err=%v", page, err)
	}
	status := "archived"
	details := []Detail{{Name: "detail two", Values: []string{"C"}}}
	payload := runtime.JSON(map[string]any{"origin": "update"})
	patch := TargetRecordUpdate{SetStatus: &status, SetDetails: &details, SetPayload: &payload}
	if err := client.UpdateTargetRecord(ctx, key, patch); err != nil {
		t.Fatal(err)
	}
	if err := client.UpdateTargetRecordWithOptions(ctx, key, patch, condition); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateTargetRecordReturning(ctx, key, patch); err != nil {
		t.Fatal(err)
	}
	var destination TargetRecord
	read, err := client.ReadTargetRecord(key, &destination)
	if err != nil {
		t.Fatal(err)
	}
	write, err := client.WriteTargetRecord(targetRecord)
	if err != nil {
		t.Fatal(err)
	}
	writeWithOptions, err := client.WriteTargetRecordWithOptions(targetRecord, condition)
	if err != nil {
		t.Fatal(err)
	}
	create, err := client.CreateTargetRecordOperation(targetRecord)
	if err != nil {
		t.Fatal(err)
	}
	createWithOptions, err := client.CreateTargetRecordOperation(targetRecord, condition)
	if err != nil {
		t.Fatal(err)
	}
	delete, err := client.DeleteTargetRecordOperation(key)
	if err != nil {
		t.Fatal(err)
	}
	deleteWithOptions, err := client.DeleteTargetRecordOperationWithOptions(key, condition)
	if err != nil {
		t.Fatal(err)
	}
	update, err := client.UpdateTargetRecordOperation(key, patch)
	if err != nil {
		t.Fatal(err)
	}
	updateWithOptions, err := client.UpdateTargetRecordOperationWithOptions(key, patch, condition)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.QueryTargetRecordByPK(ctx, key.PK, nil, runtime.PageRequest{Limit: 3}); err != nil {
		t.Fatal(err)
	}
	if err := client.TransactGet(ctx, read); err != nil {
		t.Fatal(err)
	}
	if err := client.TransactWrite(ctx, write, writeWithOptions, create, createWithOptions, delete, deleteWithOptions, update, updateWithOptions); err != nil {
		t.Fatal(err)
	}
	bad := runtime.JSON(make(chan int))
	badTargetRecord := targetRecord
	badTargetRecord.Payload = bad
	if _, err := client.CreateTargetRecord(ctx, badTargetRecord); err == nil {
		t.Fatal("CreateTargetRecord should return JSON encoding error")
	}
	if err := client.PutTargetRecord(ctx, badTargetRecord); err == nil {
		t.Fatal("PutTargetRecord should return JSON encoding error")
	}
	badPatch := TargetRecordUpdate{SetPayload: &bad}
	if err := client.UpdateTargetRecord(ctx, key, badPatch); err == nil {
		t.Fatal("UpdateTargetRecord should return JSON encoding error")
	}
	if _, err := client.UpdateTargetRecordReturning(ctx, key, badPatch); err == nil {
		t.Fatal("UpdateTargetRecordReturning should return JSON encoding error")
	}
	if _, err := client.UpdateTargetRecordOperation(key, badPatch); err == nil {
		t.Fatal("UpdateTargetRecordOperation should return JSON encoding error")
	}
	db.err = errors.New("offline")
	if _, err := client.CreateTargetRecord(ctx, targetRecord); err == nil {
		t.Fatal("CreateTargetRecord should propagate errors")
	}
	if err := client.PutTargetRecord(ctx, targetRecord); err == nil {
		t.Fatal("PutTargetRecord should propagate errors")
	}
	if _, _, err := client.GetTargetRecord(ctx, key); err == nil {
		t.Fatal("GetTargetRecord should propagate errors")
	}
	if err := client.DeleteTargetRecord(ctx, key); err == nil {
		t.Fatal("DeleteTargetRecord should propagate errors")
	}
	if _, err := client.ScanTargetRecord(ctx, runtime.PageRequest{}); err == nil {
		t.Fatal("ScanTargetRecord should propagate errors")
	}
	if err := client.UpdateTargetRecord(ctx, key, patch); err == nil {
		t.Fatal("UpdateTargetRecord should propagate errors")
	}
	if _, err := client.UpdateTargetRecordReturning(ctx, key, patch); err == nil {
		t.Fatal("UpdateTargetRecordReturning should propagate errors")
	}
	if err := client.TransactGet(ctx, read); err == nil {
		t.Fatal("TransactGet should propagate errors")
	}
	if err := client.TransactWrite(ctx, write); err == nil {
		t.Fatal("TransactWrite should propagate errors")
	}
	db.err = nil
	db.item = map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: "x"}, "SK": &types.AttributeValueMemberS{Value: "y"}, "payload": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"nested": nil}}}
	if _, _, err := client.GetTargetRecord(ctx, TargetRecordKey{PK: "x", SK: "y"}); err == nil {
		t.Fatal("malformed Json should be rejected")
	}
}
