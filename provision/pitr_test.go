package provision

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type pitrAPI struct {
	*statefulAPI
	description *types.PointInTimeRecoveryDescription
	updates     []*dynamodb.UpdateContinuousBackupsInput
	nilDescribe bool
	describeErr error
	updateErr   error
	unavailable int
}

func (f *pitrAPI) DescribeContinuousBackups(context.Context, *dynamodb.DescribeContinuousBackupsInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeContinuousBackupsOutput, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	if f.nilDescribe {
		return &dynamodb.DescribeContinuousBackupsOutput{}, nil
	}
	return &dynamodb.DescribeContinuousBackupsOutput{ContinuousBackupsDescription: &types.ContinuousBackupsDescription{PointInTimeRecoveryDescription: f.description}}, nil
}
func (f *pitrAPI) UpdateContinuousBackups(_ context.Context, in *dynamodb.UpdateContinuousBackupsInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateContinuousBackupsOutput, error) {
	f.updates = append(f.updates, in)
	if f.unavailable > 0 {
		f.unavailable--
		return nil, &types.ContinuousBackupsUnavailableException{Message: aws.String("backups initializing")}
	}
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	status := types.PointInTimeRecoveryStatusDisabled
	if aws.ToBool(in.PointInTimeRecoverySpecification.PointInTimeRecoveryEnabled) {
		status = types.PointInTimeRecoveryStatusEnabled
	}
	days := aws.ToInt32(in.PointInTimeRecoverySpecification.RecoveryPeriodInDays)
	f.description = &types.PointInTimeRecoveryDescription{PointInTimeRecoveryStatus: status, RecoveryPeriodInDays: aws.Int32(days)}
	return &dynamodb.UpdateContinuousBackupsOutput{ContinuousBackupsDescription: &types.ContinuousBackupsDescription{PointInTimeRecoveryDescription: f.description}}, nil
}

func TestSyncReconcilesDeclaredPITR(t *testing.T) {
	base := &statefulAPI{table: tableDescriptionFromCreate(&dynamodb.CreateTableInput{
		TableName: aws.String("items"), BillingMode: types.BillingModePayPerRequest,
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}},
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeS}},
	})}
	db := &pitrAPI{statefulAPI: base, description: &types.PointInTimeRecoveryDescription{PointInTimeRecoveryStatus: types.PointInTimeRecoveryStatusDisabled}}
	want := Table{Name: "items", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}, PITR: &PITRConfig{Enabled: true, RecoveryDays: 14}}
	if _, err := Sync(context.Background(), db, []Table{want}); err != nil {
		t.Fatal(err)
	}
	if len(db.updates) != 1 || aws.ToBool(db.updates[0].PointInTimeRecoverySpecification.PointInTimeRecoveryEnabled) != true || aws.ToInt32(db.updates[0].PointInTimeRecoverySpecification.RecoveryPeriodInDays) != 14 {
		t.Fatalf("unexpected PITR update: %#v", db.updates)
	}
	if _, err := Sync(context.Background(), db, []Table{want}); err != nil || len(db.updates) != 1 {
		t.Fatalf("matching policy should be idempotent: updates=%d err=%v", len(db.updates), err)
	}
	longer := Table{Name: "items", Partition: want.Partition, PITR: &PITRConfig{Enabled: true, RecoveryDays: 14, PreserveLonger: true}}
	db.description.RecoveryPeriodInDays = aws.Int32(35)
	if _, err := Sync(context.Background(), db, []Table{longer}); err != nil || len(db.updates) != 1 {
		t.Fatalf("shared sequence retention must not be shortened: updates=%d err=%v", len(db.updates), err)
	}
}

func TestPITRManagementErrorsAndUndeclaredPolicy(t *testing.T) {
	want := Table{Name: "items", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}, PITR: &PITRConfig{Enabled: true, RecoveryDays: 35}}
	if _, err := Sync(context.Background(), &statefulAPI{table: tableDescriptionFromCreate(&dynamodb.CreateTableInput{TableName: aws.String("items"), BillingMode: types.BillingModePayPerRequest, KeySchema: []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}}, AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeS}}})}, []Table{want}); err == nil {
		t.Fatal("declared PITR requires continuous backup API")
	}
	apiErr := errors.New("access denied")
	base := &statefulAPI{table: tableDescriptionFromCreate(&dynamodb.CreateTableInput{TableName: aws.String("items"), BillingMode: types.BillingModePayPerRequest, KeySchema: []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}}, AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeS}}})}
	db := &pitrAPI{statefulAPI: base, describeErr: apiErr}
	if _, err := Sync(context.Background(), db, []Table{want}); !errors.Is(err, apiErr) {
		t.Fatalf("expected describe error, got %v", err)
	}
	db.describeErr = nil
	db.updateErr = apiErr
	db.description = &types.PointInTimeRecoveryDescription{PointInTimeRecoveryStatus: types.PointInTimeRecoveryStatusDisabled}
	if _, err := Sync(context.Background(), db, []Table{want}); !errors.Is(err, apiErr) {
		t.Fatalf("expected update error, got %v", err)
	}
	db.updateErr = nil
	db.description = &types.PointInTimeRecoveryDescription{PointInTimeRecoveryStatus: types.PointInTimeRecoveryStatusDisabled}
	disabled := Table{Name: "items", Partition: want.Partition, PITR: &PITRConfig{Enabled: false}}
	if _, err := Sync(context.Background(), db, []Table{disabled}); err != nil || len(db.updates) != 1 {
		t.Fatalf("disabled PITR should be idempotent: updates=%d err=%v", len(db.updates), err)
	}
	db.nilDescribe = true
	if _, err := Sync(context.Background(), db, []Table{want}); err == nil {
		t.Fatal("empty continuous backup description should fail")
	}
	createdWithError := &pitrAPI{statefulAPI: &statefulAPI{missing: true}, description: &types.PointInTimeRecoveryDescription{PointInTimeRecoveryStatus: types.PointInTimeRecoveryStatusDisabled}, updateErr: apiErr}
	if _, err := Sync(context.Background(), createdWithError, []Table{want}); !errors.Is(err, apiErr) {
		t.Fatalf("PITR update after create should report its error: %v", err)
	}
	if _, err := Sync(context.Background(), &statefulAPI{table: base.table}, []Table{{Name: "items", Partition: want.Partition}}); err != nil {
		t.Fatalf("undeclared PITR should be left unmanaged: %v", err)
	}
}

func TestSyncRetriesPITRAfterContinuousBackupsInitialize(t *testing.T) {
	base := &statefulAPI{missing: true}
	db := &pitrAPI{statefulAPI: base, description: &types.PointInTimeRecoveryDescription{PointInTimeRecoveryStatus: types.PointInTimeRecoveryStatusDisabled}, unavailable: 1}
	want := Table{Name: "items", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}, PITR: &PITRConfig{Enabled: true, RecoveryDays: 7}}
	created, err := Sync(context.Background(), db, []Table{want})
	if err != nil || len(created) != 1 || len(db.updates) != 2 {
		t.Fatalf("PITR should retry after initial table setup: created=%v updates=%d err=%v", created, len(db.updates), err)
	}
}

func TestSyncReportsPITRStillInitializingAfterRetry(t *testing.T) {
	db := &pitrAPI{statefulAPI: &statefulAPI{missing: true}, description: &types.PointInTimeRecoveryDescription{PointInTimeRecoveryStatus: types.PointInTimeRecoveryStatusDisabled}, unavailable: 2}
	want := Table{Name: "items", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}, PITR: &PITRConfig{Enabled: true, RecoveryDays: 7}}
	if _, err := Sync(context.Background(), db, []Table{want}); err == nil || !strings.Contains(err.Error(), "PITR change is in progress") {
		t.Fatalf("remaining service initialization should be retryable: %v", err)
	}
}

func TestSyncRetriesPITRForExistingTable(t *testing.T) {
	base := &statefulAPI{table: tableDescriptionFromCreate(&dynamodb.CreateTableInput{
		TableName: aws.String("items"), BillingMode: types.BillingModePayPerRequest,
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}},
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeS}},
	})}
	db := &pitrAPI{statefulAPI: base, description: &types.PointInTimeRecoveryDescription{PointInTimeRecoveryStatus: types.PointInTimeRecoveryStatusDisabled}, unavailable: 1}
	want := Table{Name: "items", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}, PITR: &PITRConfig{Enabled: true, RecoveryDays: 7}}
	if _, err := Sync(context.Background(), db, []Table{want}); err != nil || len(db.updates) != 2 {
		t.Fatalf("existing table PITR should retry after initialization: updates=%d err=%v", len(db.updates), err)
	}
	apiErr := errors.New("permission denied")
	db = &pitrAPI{statefulAPI: base, description: &types.PointInTimeRecoveryDescription{PointInTimeRecoveryStatus: types.PointInTimeRecoveryStatusDisabled}, unavailable: 1, updateErr: apiErr}
	if _, err := Sync(context.Background(), db, []Table{want}); !errors.Is(err, apiErr) {
		t.Fatalf("non-transient PITR retry error should be returned: %v", err)
	}
}
