package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/liberocks/dynamox/backup"
	"github.com/liberocks/dynamox/provision"
	"github.com/liberocks/dynamox/runtime"
	"github.com/liberocks/dynamox/schema"
)

type commandDynamoFake struct {
	tables                 map[string]*types.TableDescription
	backups                map[string]*types.BackupDetails
	pitr                   map[string]*types.PointInTimeRecoveryDescription
	describeErr            error
	listErr                error
	backupErr              error
	backupErrOnCall        int
	describeBackupCalls    int
	createBackupErr        error
	createBackupStatus     types.BackupStatus
	restoreErr             error
	continuousErr          error
	continuousErrOnRestore bool
	updateContinuousErr    error
	driftRestore           bool
	restoreStatus          types.TableStatus
}

func newCommandDynamoFake() *commandDynamoFake {
	now := time.Now().UTC()
	input := &dynamodb.CreateTableInput{TableName: aws.String("items"), KeySchema: []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}}, AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeS}}, BillingMode: types.BillingModePayPerRequest}
	table := commandTableDescription(input)
	table.TableArn = aws.String("arn:aws:dynamodb:us-east-1:000000000000:table/items")
	return &commandDynamoFake{tables: map[string]*types.TableDescription{"items": table}, backups: map[string]*types.BackupDetails{}, pitr: map[string]*types.PointInTimeRecoveryDescription{"items": {PointInTimeRecoveryStatus: types.PointInTimeRecoveryStatusEnabled, EarliestRestorableDateTime: aws.Time(now.Add(-24 * time.Hour)), LatestRestorableDateTime: aws.Time(now)}}}
}

func commandTableDescription(in *dynamodb.CreateTableInput) *types.TableDescription {
	return &types.TableDescription{
		TableName: in.TableName, TableStatus: types.TableStatusActive, KeySchema: in.KeySchema,
		AttributeDefinitions: in.AttributeDefinitions, BillingModeSummary: &types.BillingModeSummary{BillingMode: in.BillingMode},
		GlobalSecondaryIndexes: []types.GlobalSecondaryIndexDescription{}, LocalSecondaryIndexes: []types.LocalSecondaryIndexDescription{},
	}
}

func (f *commandDynamoFake) DescribeTable(_ context.Context, in *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	if table := f.tables[aws.ToString(in.TableName)]; table != nil {
		return &dynamodb.DescribeTableOutput{Table: table}, nil
	}
	return nil, &types.ResourceNotFoundException{}
}
func (f *commandDynamoFake) CreateTable(_ context.Context, in *dynamodb.CreateTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.CreateTableOutput, error) {
	f.tables[aws.ToString(in.TableName)] = commandTableDescription(in)
	return &dynamodb.CreateTableOutput{}, nil
}
func (f *commandDynamoFake) UpdateTable(context.Context, *dynamodb.UpdateTableInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateTableOutput, error) {
	return &dynamodb.UpdateTableOutput{}, nil
}
func (f *commandDynamoFake) DescribeTimeToLive(context.Context, *dynamodb.DescribeTimeToLiveInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTimeToLiveOutput, error) {
	return &dynamodb.DescribeTimeToLiveOutput{TimeToLiveDescription: &types.TimeToLiveDescription{TimeToLiveStatus: types.TimeToLiveStatusDisabled}}, nil
}
func (f *commandDynamoFake) UpdateTimeToLive(context.Context, *dynamodb.UpdateTimeToLiveInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateTimeToLiveOutput, error) {
	return &dynamodb.UpdateTimeToLiveOutput{}, nil
}
func (f *commandDynamoFake) DescribeContinuousBackups(_ context.Context, in *dynamodb.DescribeContinuousBackupsInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeContinuousBackupsOutput, error) {
	if f.continuousErr != nil {
		return nil, f.continuousErr
	}
	if f.continuousErrOnRestore && strings.Contains(aws.ToString(in.TableName), "-") {
		return nil, errors.New("injected restored PITR inspection failure")
	}
	return &dynamodb.DescribeContinuousBackupsOutput{ContinuousBackupsDescription: &types.ContinuousBackupsDescription{PointInTimeRecoveryDescription: f.pitr[aws.ToString(in.TableName)]}}, nil
}
func (f *commandDynamoFake) UpdateContinuousBackups(_ context.Context, in *dynamodb.UpdateContinuousBackupsInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateContinuousBackupsOutput, error) {
	if f.updateContinuousErr != nil {
		return nil, f.updateContinuousErr
	}
	status := types.PointInTimeRecoveryStatusDisabled
	if aws.ToBool(in.PointInTimeRecoverySpecification.PointInTimeRecoveryEnabled) {
		status = types.PointInTimeRecoveryStatusEnabled
	}
	days := aws.ToInt32(in.PointInTimeRecoverySpecification.RecoveryPeriodInDays)
	f.pitr[aws.ToString(in.TableName)] = &types.PointInTimeRecoveryDescription{PointInTimeRecoveryStatus: status, RecoveryPeriodInDays: aws.Int32(days)}
	return &dynamodb.UpdateContinuousBackupsOutput{ContinuousBackupsDescription: &types.ContinuousBackupsDescription{PointInTimeRecoveryDescription: f.pitr[aws.ToString(in.TableName)]}}, nil
}
func (f *commandDynamoFake) CreateBackup(_ context.Context, in *dynamodb.CreateBackupInput, _ ...func(*dynamodb.Options)) (*dynamodb.CreateBackupOutput, error) {
	if f.createBackupErr != nil {
		return nil, f.createBackupErr
	}
	arn := "arn:aws:dynamodb:us-east-1:000000000000:table/items/backup/" + aws.ToString(in.BackupName)
	status := f.createBackupStatus
	if status == "" {
		status = types.BackupStatusAvailable
	}
	details := &types.BackupDetails{BackupArn: aws.String(arn), BackupName: in.BackupName, BackupStatus: status}
	f.backups[arn] = details
	return &dynamodb.CreateBackupOutput{BackupDetails: details}, nil
}
func (f *commandDynamoFake) ListBackups(_ context.Context, in *dynamodb.ListBackupsInput, _ ...func(*dynamodb.Options)) (*dynamodb.ListBackupsOutput, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := &dynamodb.ListBackupsOutput{}
	for _, item := range f.backups {
		out.BackupSummaries = append(out.BackupSummaries, types.BackupSummary{BackupArn: item.BackupArn, BackupName: item.BackupName, BackupStatus: item.BackupStatus})
	}
	return out, nil
}
func (f *commandDynamoFake) DescribeBackup(_ context.Context, in *dynamodb.DescribeBackupInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeBackupOutput, error) {
	f.describeBackupCalls++
	if f.backupErr != nil {
		return nil, f.backupErr
	}
	if f.backupErrOnCall > 0 && f.describeBackupCalls == f.backupErrOnCall {
		return nil, errors.New("injected backup status failure")
	}
	if item := f.backups[aws.ToString(in.BackupArn)]; item != nil {
		return &dynamodb.DescribeBackupOutput{BackupDescription: &types.BackupDescription{BackupDetails: item}}, nil
	}
	return nil, errors.New("backup missing")
}
func (f *commandDynamoFake) RestoreTableFromBackup(_ context.Context, in *dynamodb.RestoreTableFromBackupInput, _ ...func(*dynamodb.Options)) (*dynamodb.RestoreTableFromBackupOutput, error) {
	if f.restoreErr != nil {
		return nil, f.restoreErr
	}
	source := f.tables["items"]
	target := *source
	if f.driftRestore {
		target.KeySchema = []types.KeySchemaElement{{AttributeName: aws.String("wrong"), KeyType: types.KeyTypeHash}}
	}
	target.TableName, target.TableArn, target.TableStatus = in.TargetTableName, aws.String("arn:aws:dynamodb:us-east-1:000000000000:table/"+aws.ToString(in.TargetTableName)), types.TableStatusActive
	if f.restoreStatus != "" {
		target.TableStatus = f.restoreStatus
	}
	target.RestoreSummary = &types.RestoreSummary{SourceBackupArn: in.BackupArn}
	f.tables[aws.ToString(in.TargetTableName)] = &target
	f.pitr[aws.ToString(in.TargetTableName)] = &types.PointInTimeRecoveryDescription{PointInTimeRecoveryStatus: types.PointInTimeRecoveryStatusDisabled}
	return &dynamodb.RestoreTableFromBackupOutput{}, nil
}
func (f *commandDynamoFake) RestoreTableToPointInTime(_ context.Context, in *dynamodb.RestoreTableToPointInTimeInput, _ ...func(*dynamodb.Options)) (*dynamodb.RestoreTableToPointInTimeOutput, error) {
	if f.restoreErr != nil {
		return nil, f.restoreErr
	}
	source := f.tables[aws.ToString(in.SourceTableName)]
	target := *source
	if f.driftRestore {
		target.KeySchema = []types.KeySchemaElement{{AttributeName: aws.String("wrong"), KeyType: types.KeyTypeHash}}
	}
	target.TableName, target.TableArn, target.TableStatus = in.TargetTableName, aws.String("arn:aws:dynamodb:us-east-1:000000000000:table/"+aws.ToString(in.TargetTableName)), types.TableStatusActive
	if f.restoreStatus != "" {
		target.TableStatus = f.restoreStatus
	}
	target.RestoreSummary = &types.RestoreSummary{SourceTableArn: source.TableArn, RestoreDateTime: in.RestoreDateTime}
	f.tables[aws.ToString(in.TargetTableName)] = &target
	f.pitr[aws.ToString(in.TargetTableName)] = &types.PointInTimeRecoveryDescription{PointInTimeRecoveryStatus: types.PointInTimeRecoveryStatusDisabled}
	return &dynamodb.RestoreTableToPointInTimeOutput{}, nil
}

func TestBackupAndRestoreCommandsWithInjectedDynamo(t *testing.T) {
	db := newCommandDynamoFake()
	load := func(context.Context, string, string) (aws.Config, error) { return aws.Config{Region: "us-east-1"}, nil }
	client := func(aws.Config, string) (dynamoOperationsAPI, error) { return db, nil }
	work := t.TempDir()
	schemaPath := filepath.Join(work, "schema.dynamox")
	if err := os.WriteFile(schemaPath, []byte(`model Item { id String @id @@table(name: "items") @@pitr(enabled: true, recoveryDays: 7) }`), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(work, "backup.json")
	state := filepath.Join(work, "restore.json")
	if err := hiddenOutput(t, func() error {
		return runBackupWith([]string{"create", "--name", "set", "--manifest", manifest, "--writes-paused", "--settle", "0s", schemaPath}, load, client)
	}); err != nil {
		t.Fatal(err)
	}
	if err := hiddenOutput(t, func() error { return runBackupWith([]string{"list", schemaPath}, load, client) }); err != nil {
		t.Fatal(err)
	}
	if err := hiddenOutput(t, func() error { return runBackupWith([]string{"status", "--manifest", manifest}, load, client) }); err != nil {
		t.Fatal(err)
	}
	if err := hiddenOutput(t, func() error {
		return runRestoreWith([]string{"backup", "--manifest", manifest, "--state", state, "--suffix", "copy", "--writes-paused", schemaPath}, load, client)
	}); err != nil {
		t.Fatal(err)
	}
	if err := hiddenOutput(t, func() error { return runRestoreWith([]string{"status", "--state", state}, load, client) }); err != nil {
		t.Fatal(err)
	}
	if err := hiddenOutput(t, func() error {
		return runRestoreWith([]string{"resume", "--state", state, "--writes-paused", schemaPath}, load, client)
	}); err != nil {
		t.Fatal(err)
	}
	pitrState := filepath.Join(work, "pitr.json")
	if err := hiddenOutput(t, func() error {
		return runRestoreWith([]string{"pitr", "--at", time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), "--state", pitrState, "--suffix", "pitr", "--writes-paused", schemaPath}, load, client)
	}); err != nil {
		t.Fatal(err)
	}
	if err := hiddenOutput(t, func() error { return runRestoreWith([]string{"status", "--state", pitrState}, load, client) }); err != nil {
		t.Fatal(err)
	}
	if err := hiddenOutput(t, func() error {
		return runRestoreWith([]string{"resume", "--state", pitrState, "--writes-paused", schemaPath}, load, client)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBackupCommandFailurePaths(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.dynamox")
	if err := os.WriteFile(schemaPath, []byte(`model Item { id String @id @@table(name: "items") @@pitr(enabled: true) }`), 0600); err != nil {
		t.Fatal(err)
	}
	invalidPath := filepath.Join(dir, "invalid.dynamox")
	if err := os.WriteFile(invalidPath, []byte(`model Item { id MissingType @id @@table(name: "items") }`), 0600); err != nil {
		t.Fatal(err)
	}
	loadOK := func(context.Context, string, string) (aws.Config, error) { return aws.Config{Region: "us-east-1"}, nil }
	loadFail := func(context.Context, string, string) (aws.Config, error) {
		return aws.Config{}, errors.New("injected config failure")
	}
	clientFail := func(aws.Config, string) (dynamoOperationsAPI, error) {
		return nil, errors.New("injected client failure")
	}
	base := func(db *commandDynamoFake) func(aws.Config, string) (dynamoOperationsAPI, error) {
		return func(aws.Config, string) (dynamoOperationsAPI, error) { return db, nil }
	}
	tests := []struct {
		name string
		args []string
		load func(context.Context, string, string) (aws.Config, error)
		new  func(aws.Config, string) (dynamoOperationsAPI, error)
	}{
		{"schema read", []string{"create", "--name", "set", "--manifest", filepath.Join(dir, "read.json"), "--writes-paused", filepath.Join(dir, "missing.dynamox")}, loadOK, base(newCommandDynamoFake())},
		{"schema validation", []string{"create", "--name", "set", "--manifest", filepath.Join(dir, "invalid.json"), "--writes-paused", invalidPath}, loadOK, base(newCommandDynamoFake())},
		{"configuration", []string{"list", schemaPath}, loadFail, base(newCommandDynamoFake())},
		{"client", []string{"list", schemaPath}, loadOK, clientFail},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := runBackupWith(test.args, test.load, test.new); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	createPath := filepath.Join(dir, "create.json")
	db := newCommandDynamoFake()
	db.createBackupErr = errors.New("injected create failure")
	if err := runBackupWith([]string{"create", "--name", "set", "--manifest", createPath, "--writes-paused", "--settle", "0s", schemaPath}, loadOK, base(db)); err == nil {
		t.Fatal("expected create backup error")
	}
	db = newCommandDynamoFake()
	db.describeErr = errors.New("injected source inspection failure")
	if err := runBackupWith([]string{"create", "--name", "set", "--manifest", filepath.Join(dir, "describe.json"), "--writes-paused", "--settle", "0s", schemaPath}, loadOK, base(db)); err == nil {
		t.Fatal("expected source table inspection error")
	}
	db = newCommandDynamoFake()
	db.createBackupStatus = types.BackupStatusCreating
	if err := runBackupWith([]string{"create", "--name", "set", "--manifest", filepath.Join(dir, "wait.json"), "--writes-paused", "--settle", "0s", "--timeout", "1ms", schemaPath}, loadOK, base(db)); err == nil {
		t.Fatal("expected backup wait timeout")
	}
	db = newCommandDynamoFake()
	db.listErr = errors.New("injected list failure")
	if err := runBackupWith([]string{"list", schemaPath}, loadOK, base(db)); err == nil {
		t.Fatal("expected backup list error")
	}
	manifestPath := filepath.Join(dir, "status.json")
	if err := backup.Save(manifestPath, backup.Manifest{Version: 1, Name: "set", Tables: []backup.TableRecord{{SourceTable: "items", BackupARN: "backup-arn"}}}); err != nil {
		t.Fatal(err)
	}
	db = newCommandDynamoFake()
	db.backupErr = errors.New("injected backup describe failure")
	if err := runBackupWith([]string{"status", "--manifest", manifestPath}, loadOK, base(db)); err == nil {
		t.Fatal("expected backup status error")
	}
	db = newCommandDynamoFake()
	db.backupErrOnCall = 2
	if err := runBackupWith([]string{"create", "--name", "set", "--manifest", filepath.Join(dir, "refresh-wait.json"), "--writes-paused", "--settle", "0s", schemaPath}, loadOK, base(db)); err == nil {
		t.Fatal("expected wait status refresh error")
	}
	if err := runBackupWith([]string{"status", "--manifest", filepath.Join(dir, "missing.json")}, loadOK, base(newCommandDynamoFake())); err == nil {
		t.Fatal("expected missing manifest error")
	}
}

func TestRestoreCommandFailurePaths(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.dynamox")
	if err := os.WriteFile(schemaPath, []byte(`model Item { id String @id @@table(name: "items") @@pitr(enabled: true) }`), 0600); err != nil {
		t.Fatal(err)
	}
	invalidPath := filepath.Join(dir, "invalid.dynamox")
	if err := os.WriteFile(invalidPath, []byte(`model Item { id MissingType @id @@table(name: "items") }`), 0600); err != nil {
		t.Fatal(err)
	}
	loadOK := func(context.Context, string, string) (aws.Config, error) { return aws.Config{Region: "us-east-1"}, nil }
	loadFail := func(context.Context, string, string) (aws.Config, error) {
		return aws.Config{}, errors.New("injected config failure")
	}
	base := func(db *commandDynamoFake) func(aws.Config, string) (dynamoOperationsAPI, error) {
		return func(aws.Config, string) (dynamoOperationsAPI, error) { return db, nil }
	}
	clientFail := func(aws.Config, string) (dynamoOperationsAPI, error) {
		return nil, errors.New("injected client failure")
	}
	if err := runRestoreWith([]string{"status", "--state", filepath.Join(dir, "missing.json")}, loadFail, base(newCommandDynamoFake())); err == nil {
		t.Fatal("expected config error")
	}
	if err := runRestoreWith([]string{"status", "--state", filepath.Join(dir, "missing.json")}, loadOK, clientFail); err == nil {
		t.Fatal("expected client error")
	}
	for _, path := range []string{filepath.Join(dir, "missing.dynamox"), invalidPath} {
		args := []string{"backup", "--manifest", filepath.Join(dir, "missing-backup.json"), "--state", filepath.Join(dir, "state.json"), "--suffix", "copy", "--writes-paused", path}
		if err := runRestoreWith(args, loadOK, base(newCommandDynamoFake())); err == nil {
			t.Fatalf("expected schema failure for %s", path)
		}
	}
	backupPath := filepath.Join(dir, "backup.json")
	backupARN := "arn:aws:dynamodb:us-east-1:000000000000:table/items/backup/snapshot"
	manifest := backup.Manifest{Version: 1, Name: "set", Region: "us-east-1", Mode: "backup", Tables: []backup.TableRecord{{SourceTable: "items", BackupName: "snapshot", BackupARN: backupARN, BackupState: types.BackupStatusAvailable}}}
	if err := backup.Save(backupPath, manifest); err != nil {
		t.Fatal(err)
	}
	mismatchBackupPath := filepath.Join(dir, "mismatch-backup.json")
	mismatchManifest := manifest
	mismatchManifest.Tables = []backup.TableRecord{{SourceTable: "other", BackupARN: backupARN}}
	if err := backup.Save(mismatchBackupPath, mismatchManifest); err != nil {
		t.Fatal(err)
	}
	if err := runRestoreWith([]string{"backup", "--manifest", mismatchBackupPath, "--state", filepath.Join(dir, "mismatch-restore.json"), "--suffix", "copy", "--writes-paused", schemaPath}, loadOK, base(newCommandDynamoFake())); err == nil {
		t.Fatal("backup table set must match the current schema")
	}
	lengthMismatchPath := filepath.Join(dir, "length-mismatch.json")
	lengthMismatch := manifest
	lengthMismatch.Tables = append(lengthMismatch.Tables, backup.TableRecord{SourceTable: "extra"})
	if err := backup.Save(lengthMismatchPath, lengthMismatch); err != nil {
		t.Fatal(err)
	}
	if err := runRestoreWith([]string{"backup", "--manifest", lengthMismatchPath, "--state", filepath.Join(dir, "length-mismatch-state.json"), "--suffix", "copy", "--writes-paused", schemaPath}, loadOK, base(newCommandDynamoFake())); err == nil {
		t.Fatal("backup table count must match the schema")
	}
	backupArgs := func(state, path string, timeout bool) []string {
		args := []string{"backup", "--manifest", backupPath, "--state", state, "--suffix", "copy", "--writes-paused", path}
		if timeout {
			args = append(args, "--timeout", "1ms")
		}
		return args
	}
	readyDB := func() *commandDynamoFake {
		db := newCommandDynamoFake()
		db.backups[backupARN] = &types.BackupDetails{BackupArn: aws.String(backupARN), BackupStatus: types.BackupStatusAvailable}
		return db
	}
	for _, tc := range []struct {
		name string
		init func() *commandDynamoFake
	}{
		{"start restore", func() *commandDynamoFake {
			db := readyDB()
			db.restoreErr = errors.New("injected restore failure")
			return db
		}},
		{"restore wait", func() *commandDynamoFake { db := readyDB(); db.restoreStatus = types.TableStatusCreating; return db }},
		{"restore reconciliation", func() *commandDynamoFake { db := readyDB(); db.continuousErrOnRestore = true; return db }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := tc.init()
			args := []string{"backup", "--manifest", backupPath, "--state", filepath.Join(t.TempDir(), "state.json"), "--suffix", "copy", "--writes-paused"}
			if tc.name == "restore wait" {
				args = append(args, "--timeout", "1ms")
			}
			args = append(args, schemaPath)
			if err := hiddenOutput(t, func() error { return runRestoreWith(args, loadOK, base(db)) }); err == nil {
				t.Fatal("expected restore failure")
			}
		})
	}
	noAck := backupArgs(filepath.Join(dir, "no-ack.json"), schemaPath, false)
	for i, arg := range noAck {
		if arg == "--writes-paused" {
			noAck = append(noAck[:i], noAck[i+1:]...)
			break
		}
	}
	if err := runRestoreWith(noAck, loadOK, base(readyDB())); err == nil {
		t.Fatal("restore without write pause acknowledgment should fail")
	}
	if err := runRestoreWith([]string{"pitr", "--state", filepath.Join(dir, "parse.json"), "--suffix", "copy", "--at", "bad", schemaPath}, loadOK, base(readyDB())); err == nil {
		t.Fatal("invalid timestamp should fail")
	}
	if err := runRestoreWith([]string{"pitr", "--state", filepath.Join(dir, "missing-schema.json"), "--suffix", "copy", "--at", time.Now().UTC().Format(time.RFC3339), filepath.Join(dir, "missing.dynamox")}, loadOK, base(readyDB())); err == nil {
		t.Fatal("missing PITR schema should fail")
	}
	if err := runRestoreWith([]string{"pitr", "--state", filepath.Join(dir, "invalid-schema.json"), "--suffix", "copy", "--at", time.Now().UTC().Format(time.RFC3339), invalidPath}, loadOK, base(readyDB())); err == nil {
		t.Fatal("invalid PITR schema should fail")
	}
	for _, tc := range []struct {
		name string
		args []string
		db   *commandDynamoFake
	}{
		{"PITR acknowledgment", []string{"pitr", "--state", filepath.Join(dir, "pitr-no-ack.json"), "--suffix", "copy", "--at", time.Now().UTC().Format(time.RFC3339), schemaPath}, readyDB()},
		{"PITR window", []string{"pitr", "--state", filepath.Join(dir, "pitr-old.json"), "--suffix", "copy", "--at", time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339), "--writes-paused", schemaPath}, readyDB()},
		{"PITR wait", []string{"pitr", "--state", filepath.Join(dir, "pitr-wait.json"), "--suffix", "copy", "--at", time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), "--writes-paused", "--timeout", "1ms", schemaPath}, readyDB()},
		{"PITR reconciliation", []string{"pitr", "--state", filepath.Join(dir, "pitr-sync.json"), "--suffix", "copy", "--at", time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), "--writes-paused", schemaPath}, readyDB()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "PITR wait" {
				tc.db.restoreStatus = types.TableStatusCreating
			}
			if tc.name == "PITR reconciliation" {
				tc.db.continuousErrOnRestore = true
			}
			if err := hiddenOutput(t, func() error { return runRestoreWith(tc.args, loadOK, base(tc.db)) }); err == nil {
				t.Fatal("expected PITR restore failure")
			}
		})
	}
	if err := runRestoreWith([]string{"status", "--state", filepath.Join(dir, "missing-state.json")}, loadOK, base(readyDB())); err == nil {
		t.Fatal("missing restore state should fail")
	}
	if err := runRestoreWith([]string{"resume", "--state", filepath.Join(dir, "missing-state.json"), "--writes-paused", schemaPath}, loadOK, base(readyDB())); err == nil {
		t.Fatal("missing resume state should fail")
	}
	state := filepath.Join(dir, "resume-invalid-mode.json")
	if err := backup.Save(state, backup.Manifest{Version: 1, Name: "x", Tables: []backup.TableRecord{{SourceTable: "items"}}}); err != nil {
		t.Fatal(err)
	}
	if err := runRestoreWith([]string{"resume", "--state", state, "--writes-paused", schemaPath}, loadOK, base(readyDB())); err == nil {
		t.Fatal("resume with invalid state should fail")
	}
	mismatchState := filepath.Join(dir, "resume-mismatch.json")
	mismatchManifest.Mode = "backup"
	if err := backup.Save(mismatchState, mismatchManifest); err != nil {
		t.Fatal(err)
	}
	if err := runRestoreWith([]string{"resume", "--state", mismatchState, "--writes-paused", schemaPath}, loadOK, base(readyDB())); err == nil {
		t.Fatal("resume table set must match the current schema")
	}
	if err := runBackupWith([]string{"unknown", schemaPath}, loadOK, base(readyDB())); err == nil {
		t.Fatal("unknown backup command should fail")
	}
	if err := runRestoreWith([]string{"unknown"}, loadOK, base(readyDB())); err == nil {
		t.Fatal("unknown restore command should fail")
	}
	for _, tc := range []struct {
		name string
		path string
		db   *commandDynamoFake
	}{
		{"resume wait", schemaPath, func() *commandDynamoFake { db := readyDB(); db.restoreStatus = types.TableStatusCreating; return db }()},
		{"resume schema read", filepath.Join(dir, "missing.dynamox"), readyDB()},
		{"resume schema validation", invalidPath, readyDB()},
		{"resume reconciliation", schemaPath, func() *commandDynamoFake { db := readyDB(); db.continuousErrOnRestore = true; return db }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			statePath := filepath.Join(t.TempDir(), "resume.json")
			stateManifest := manifest
			stateManifest.Tables[0].TargetTable = "items-copy"
			if tc.name != "resume wait" {
				table := *tc.db.tables["items"]
				table.TableName = aws.String("items-copy")
				table.TableArn = aws.String("arn:aws:dynamodb:us-east-1:000000000000:table/items-copy")
				table.RestoreSummary = &types.RestoreSummary{SourceBackupArn: aws.String(backupARN)}
				tc.db.tables["items-copy"] = &table
			}
			if err := backup.Save(statePath, stateManifest); err != nil {
				t.Fatal(err)
			}
			args := []string{"resume", "--state", statePath, "--writes-paused"}
			if tc.name == "resume wait" {
				args = append(args, "--timeout", "1ms")
			}
			args = append(args, tc.path)
			if err := hiddenOutput(t, func() error { return runRestoreWith(args, loadOK, base(tc.db)) }); err == nil {
				t.Fatal("expected resume error")
			}
		})
	}
}

func TestPrintJSONWriteFailure(t *testing.T) {
	old := os.Stdout
	file, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = file
	defer func() { os.Stdout = old }()
	if err := printJSON(map[string]string{"ok": "value"}); err == nil {
		t.Fatal("expected JSON output error")
	}
}

func TestBackupAndRestoreCommandArgumentErrors(t *testing.T) {
	for _, call := range []struct {
		name string
		fn   func() error
	}{
		{"backup usage", func() error { return runBackup(nil) }},
		{"restore usage", func() error { return runRestore(nil) }},
		{"backup missing arguments", func() error { return runBackup([]string{"create"}) }},
		{"backup status manifest", func() error { return runBackup([]string{"status"}) }},
		{"unknown backup subcommand", func() error { return runBackup([]string{"remove"}) }},
		{"restore backup arguments", func() error { return runRestore([]string{"backup"}) }},
		{"restore PITR arguments", func() error { return runRestore([]string{"pitr"}) }},
		{"restore status arguments", func() error { return runRestore([]string{"status"}) }},
		{"unknown restore subcommand", func() error { return runRestore([]string{"delete"}) }},
	} {
		t.Run(call.name, func(t *testing.T) {
			if err := call.fn(); err == nil {
				t.Fatal("expected argument error")
			}
		})
	}
	if err := runBackupWith([]string{"create", "--name", "x", "--manifest", "x", "--writes-paused", "a", "b"}, nil, nil); err == nil {
		t.Fatal("too many schema paths should fail")
	}
	if err := runRestoreWith([]string{"status", "--state", "x", "a", "b"}, nil, nil); err == nil {
		t.Fatal("too many schema paths should fail")
	}
	if err := runRestoreWith([]string{"status", "--state", "x", "--bad"}, nil, nil); err == nil {
		t.Fatal("invalid restore flag should fail")
	}
	if err := runBackupWith([]string{"create", "--name", "x", "--manifest", "x", "schema.dynamox"}, nil, nil); err == nil {
		t.Fatal("writes must be paused")
	}
	if err := runBackupWith([]string{"create", "--name", "x", "--manifest", "x", "--writes-paused", "--bad"}, nil, nil); err == nil {
		t.Fatal("invalid flags should fail")
	}
}

func TestDefaultDynamoClientFactoryAndSchemaEdge(t *testing.T) {
	client, err := newDynamoOperationsClient(aws.Config{Region: "us-east-1"}, "http://localhost:8000")
	if err != nil || client == nil {
		t.Fatalf("create configured DynamoDB client: %v", err)
	}
	if err := run([]string{"backup", "unknown"}); err == nil {
		t.Fatal("unknown backup command should fail")
	}
	if err := run([]string{"restore", "unknown"}); err == nil {
		t.Fatal("unknown restore command should fail")
	}
	if got := provisionTables(&schema.Schema{Models: []schema.Model{{Table: "items", Partition: "id", Fields: []schema.Field{{Name: "id", Type: "Int"}}}}}); len(got) != 1 {
		t.Fatalf("missing partition should not imply a shared sequence table: %#v", got)
	}
	serialAndRegular := &schema.Schema{Models: []schema.Model{
		{Table: "serial_items", Partition: "id", Fields: []schema.Field{{Name: "id", Type: "Int", Default: "serial"}}},
		{Table: "regular_items", Partition: "id", Fields: []schema.Field{{Name: "id", Type: "Int"}}},
	}}
	if tables := provisionTables(serialAndRegular); len(tables) != 3 || tables[2].Name != runtime.SequenceTableName {
		t.Fatalf("serial and regular models should provision shared sequence table: %#v", tables)
	}
}

func hiddenOutput(t *testing.T, fn func() error) error {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	os.Stdout = w
	callErr := fn()
	_ = w.Close()
	os.Stdout = old
	_, readErr := io.Copy(io.Discard, r)
	_ = r.Close()
	if readErr != nil {
		return readErr
	}
	return callErr
}

var _ dynamoOperationsAPI = (*commandDynamoFake)(nil)
var _ provision.API = (*commandDynamoFake)(nil)
