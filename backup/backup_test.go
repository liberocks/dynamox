package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type fakeAPI struct {
	tables       map[string]*types.TableDescription
	backups      map[string]*types.BackupDetails
	pitr         map[string]*types.PointInTimeRecoveryDescription
	createErr    error
	describeErr  error
	listErr      error
	restoreErr   error
	backupCalls  int
	restoreCalls int
}

func newFakeAPI() *fakeAPI {
	now := time.Now().UTC()
	f := &fakeAPI{tables: map[string]*types.TableDescription{}, backups: map[string]*types.BackupDetails{}, pitr: map[string]*types.PointInTimeRecoveryDescription{}}
	for _, name := range []string{"items", "serials"} {
		f.tables[name] = &types.TableDescription{TableName: aws.String(name), TableArn: aws.String("arn:aws:dynamodb:us-east-1:123456789012:table/" + name), TableStatus: types.TableStatusActive}
		f.pitr[name] = &types.PointInTimeRecoveryDescription{PointInTimeRecoveryStatus: types.PointInTimeRecoveryStatusEnabled, EarliestRestorableDateTime: aws.Time(now.Add(-24 * time.Hour)), LatestRestorableDateTime: aws.Time(now)}
	}
	return f
}

func (f *fakeAPI) CreateBackup(_ context.Context, in *dynamodb.CreateBackupInput, _ ...func(*dynamodb.Options)) (*dynamodb.CreateBackupOutput, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.backupCalls++
	arn := "arn:aws:dynamodb:us-east-1:123456789012:table/" + aws.ToString(in.TableName) + "/backup/" + aws.ToString(in.BackupName)
	details := &types.BackupDetails{BackupArn: aws.String(arn), BackupName: in.BackupName, BackupStatus: types.BackupStatusAvailable, BackupCreationDateTime: aws.Time(time.Now().UTC())}
	f.backups[arn] = details
	return &dynamodb.CreateBackupOutput{BackupDetails: details}, nil
}
func (f *fakeAPI) ListBackups(_ context.Context, in *dynamodb.ListBackupsInput, _ ...func(*dynamodb.Options)) (*dynamodb.ListBackupsOutput, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := &dynamodb.ListBackupsOutput{}
	for _, backup := range f.backups {
		if strings.Contains(aws.ToString(backup.BackupArn), "table/"+aws.ToString(in.TableName)+"/") {
			out.BackupSummaries = append(out.BackupSummaries, types.BackupSummary{BackupArn: backup.BackupArn, BackupName: backup.BackupName, BackupStatus: backup.BackupStatus, BackupCreationDateTime: backup.BackupCreationDateTime})
		}
	}
	return out, nil
}
func (f *fakeAPI) DescribeBackup(_ context.Context, in *dynamodb.DescribeBackupInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeBackupOutput, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	backup := f.backups[aws.ToString(in.BackupArn)]
	if backup == nil {
		return nil, errors.New("backup missing")
	}
	return &dynamodb.DescribeBackupOutput{BackupDescription: &types.BackupDescription{BackupDetails: backup}}, nil
}
func (f *fakeAPI) DescribeTable(_ context.Context, in *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	table := f.tables[aws.ToString(in.TableName)]
	if table == nil {
		return nil, &types.ResourceNotFoundException{}
	}
	return &dynamodb.DescribeTableOutput{Table: table}, nil
}
func (f *fakeAPI) DescribeContinuousBackups(_ context.Context, in *dynamodb.DescribeContinuousBackupsInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeContinuousBackupsOutput, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	return &dynamodb.DescribeContinuousBackupsOutput{ContinuousBackupsDescription: &types.ContinuousBackupsDescription{PointInTimeRecoveryDescription: f.pitr[aws.ToString(in.TableName)]}}, nil
}
func (f *fakeAPI) RestoreTableFromBackup(_ context.Context, in *dynamodb.RestoreTableFromBackupInput, _ ...func(*dynamodb.Options)) (*dynamodb.RestoreTableFromBackupOutput, error) {
	f.restoreCalls++
	if f.restoreErr != nil {
		return nil, f.restoreErr
	}
	backup := f.backups[aws.ToString(in.BackupArn)]
	if backup == nil {
		return nil, errors.New("backup missing")
	}
	f.tables[aws.ToString(in.TargetTableName)] = &types.TableDescription{TableName: in.TargetTableName, TableArn: aws.String("arn:restore:" + aws.ToString(in.TargetTableName)), TableStatus: types.TableStatusActive, RestoreSummary: &types.RestoreSummary{SourceBackupArn: in.BackupArn}}
	return &dynamodb.RestoreTableFromBackupOutput{}, nil
}

func TestRestoreWaitsForLineageWhileDestinationIsRestoring(t *testing.T) {
	db := newFakeAPI()
	backupARN := "arn:aws:dynamodb:us-east-1:123456789012:table/items/backup/snapshot"
	db.backups[backupARN] = &types.BackupDetails{BackupArn: aws.String(backupARN), BackupStatus: types.BackupStatusAvailable}
	db.tables["items-copy"] = &types.TableDescription{TableName: aws.String("items-copy"), TableStatus: types.TableStatusCreating}
	backupPath := filepath.Join(t.TempDir(), "source.json")
	statePath := filepath.Join(t.TempDir(), "restore.json")
	manifest := Manifest{Version: 1, Name: "set", Tables: []TableRecord{{SourceTable: "items", BackupName: "snapshot", BackupARN: backupARN}}}
	if err := Save(backupPath, manifest); err != nil {
		t.Fatal(err)
	}
	state, err := StartBackupRestore(context.Background(), db, backupPath, statePath, "us-east-1", "copy", true)
	if err != nil {
		t.Fatal("in-progress destination should be observed without duplicate restore:", err)
	}
	if state.Tables[0].Restored || db.restoreCalls != 0 {
		t.Fatalf("restore should stay pending without lineage: state=%#v restore calls=%d", state.Tables[0], db.restoreCalls)
	}
	target := db.tables["items-copy"]
	target.TableStatus = types.TableStatusActive
	target.RestoreSummary = &types.RestoreSummary{SourceBackupArn: aws.String(backupARN)}
	state, err = RefreshRestoreStatus(context.Background(), db, statePath)
	if err != nil || !state.Tables[0].Restored {
		t.Fatalf("active destination lineage should complete restore: state=%#v err=%v", state.Tables[0], err)
	}
	other := newFakeAPI()
	other.tables["items-copy"] = &types.TableDescription{TableName: aws.String("items-copy"), TableStatus: types.TableStatusActive}
	if _, err := StartBackupRestore(context.Background(), other, backupPath, filepath.Join(t.TempDir(), "collision.json"), "us-east-1", "copy", true); err == nil {
		t.Fatal("active destination without matching restore lineage must be rejected")
	}
}

func TestRestoreAcceptsCompletedTableAfterAWSDropsRestoreSummary(t *testing.T) {
	db := newFakeAPI()
	backupARN := "arn:aws:dynamodb:us-east-1:123456789012:table/items/backup/snapshot"
	statePath := filepath.Join(t.TempDir(), "restore.json")
	manifest := Manifest{Version: 1, Name: "set", Mode: "backup", Tables: []TableRecord{{SourceTable: "items", BackupARN: backupARN, TargetTable: "items-copy", RestoreRequested: true}}}
	if err := Save(statePath, manifest); err != nil {
		t.Fatal(err)
	}
	db.tables["items-copy"] = &types.TableDescription{TableName: aws.String("items-copy"), TableStatus: types.TableStatusActive}
	got, err := RefreshRestoreStatus(context.Background(), db, statePath)
	if err != nil || !got.Tables[0].Restored {
		t.Fatalf("completed restore should remain verifiable from persisted lineage: state=%#v err=%v", got.Tables[0], err)
	}
	manifest.Tables[0].RestoreRequested = false
	manifest.Tables[0].LineageVerified = false
	if err := Save(statePath, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := RefreshRestoreStatus(context.Background(), db, statePath); err == nil {
		t.Fatal("active table without observed or persisted restore lineage must fail")
	}
}
func (f *fakeAPI) RestoreTableToPointInTime(_ context.Context, in *dynamodb.RestoreTableToPointInTimeInput, _ ...func(*dynamodb.Options)) (*dynamodb.RestoreTableToPointInTimeOutput, error) {
	if f.restoreErr != nil {
		return nil, f.restoreErr
	}
	source := f.tables[aws.ToString(in.SourceTableName)]
	if source == nil {
		return nil, &types.ResourceNotFoundException{}
	}
	f.tables[aws.ToString(in.TargetTableName)] = &types.TableDescription{TableName: in.TargetTableName, TableArn: aws.String("arn:restore:" + aws.ToString(in.TargetTableName)), TableStatus: types.TableStatusActive, RestoreSummary: &types.RestoreSummary{SourceTableArn: source.TableArn, RestoreDateTime: in.RestoreDateTime}}
	return &dynamodb.RestoreTableToPointInTimeOutput{}, nil
}

func TestBackupSetRestoreAndResume(t *testing.T) {
	db := newFakeAPI()
	path := filepath.Join(t.TempDir(), "backup.json")
	if _, err := CreateSet(context.Background(), db, path, "release", "us-east-1", []string{"serials", "items", "items"}, false, 0); err == nil {
		t.Fatal("missing paused-writes acknowledgement should fail")
	}
	manifest, err := CreateSet(context.Background(), db, path, "release", "us-east-1", []string{"serials", "items", "items"}, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Tables) != 2 || db.backupCalls != 2 {
		t.Fatalf("backup set did not deduplicate tables: %#v calls=%d", manifest.Tables, db.backupCalls)
	}
	manifest, err = WaitBackupSet(context.Background(), db, path, time.Millisecond)
	if err != nil || manifest.Tables[0].BackupState != types.BackupStatusAvailable {
		t.Fatalf("backup status: %#v err=%v", manifest, err)
	}
	list, err := List(context.Background(), db, []string{"items", "items"})
	if err != nil || len(list) != 1 || len(list[0].Backups) != 1 {
		t.Fatalf("backup list: %#v err=%v", list, err)
	}
	state := filepath.Join(t.TempDir(), "restore.json")
	manifest, err = StartBackupRestore(context.Background(), db, path, state, "us-east-1", "recovery", true)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err = WaitRestoreSet(context.Background(), db, state, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(TableMappings(manifest)) != 2 || !manifest.Tables[0].Restored {
		t.Fatalf("restore mapping/state: %#v", manifest)
	}
	if _, err := ResumeRestore(context.Background(), db, state, true); err != nil {
		t.Fatalf("resume completed restore: %v", err)
	}
	if _, err := StartBackupRestore(context.Background(), db, path, state, "us-east-1", "another", true); err == nil {
		t.Fatal("must not overwrite existing restore state")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("manifest permissions = %v, err=%v", info, err)
	}
}

func TestPITRRestoreWindowAndManifest(t *testing.T) {
	db := newFakeAPI()
	at := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	state := filepath.Join(t.TempDir(), "pitr.json")
	if _, err := StartPITRRestore(context.Background(), db, state, "us-east-1", "recovery", at, []string{"items", "serials", "items"}, true); err != nil {
		t.Fatal(err)
	}
	manifest, err := WaitRestoreSet(context.Background(), db, state, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Tables) != 2 || manifest.Mode != "pitr" || manifest.Tables[0].SourceTableARN == "" || len(TableMappings(manifest)) != 2 {
		t.Fatalf("PITR restore manifest: %#v", manifest)
	}
	if _, err := ResumeRestore(context.Background(), db, state, true); err != nil {
		t.Fatalf("resume PITR restore: %v", err)
	}
	if err := CheckPITRWindow(context.Background(), db, []string{"items", "serials"}, time.Now().Add(-72*time.Hour)); err == nil || !strings.Contains(err.Error(), "common PITR window") {
		t.Fatalf("out-of-window restore should fail clearly: %v", err)
	}
	if _, err := StartPITRRestore(context.Background(), db, state, "us-east-1", "again", at, []string{"items"}, true); err == nil {
		t.Fatal("must not overwrite restore state")
	}
}

func TestManifestAndOperationFailures(t *testing.T) {
	db := newFakeAPI()
	path := filepath.Join(t.TempDir(), "missing", "backup.json")
	apiErr := errors.New("denied")
	db.describeErr = apiErr
	if _, err := CreateSet(context.Background(), db, path, "set", "us-east-1", []string{"items"}, true, 0); !errors.Is(err, apiErr) {
		t.Fatalf("source preflight error: %v", err)
	}
	if _, err := CreateSet(context.Background(), db, path, "set", "us-east-1", []string{"items"}, true, 0); err == nil {
		t.Fatal("existing manifest should prevent changed/incomplete reinitialization")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil {
		t.Fatal("invalid JSON should fail")
	}
	if err := Save("", Manifest{}); err == nil {
		t.Fatal("empty manifest path should fail")
	}
	if err := os.WriteFile(bad, []byte(`{"version":8,"tables":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil {
		t.Fatal("unsupported manifest should fail")
	}
	if _, err := CreateSet(context.Background(), newFakeAPI(), filepath.Join(t.TempDir(), "empty.json"), "x", "us-east-1", nil, true, 0); err == nil {
		t.Fatal("empty set should fail")
	}
	if _, err := StartPITRRestore(context.Background(), newFakeAPI(), filepath.Join(t.TempDir(), "no-paused.json"), "us-east-1", "x", time.Now(), []string{"items"}, false); err == nil {
		t.Fatal("restore requires paused writers")
	}
	if _, err := StartPITRRestore(context.Background(), newFakeAPI(), filepath.Join(t.TempDir(), "no-time.json"), "us-east-1", "x", time.Time{}, []string{"items"}, true); err == nil {
		t.Fatal("restore requires timestamp")
	}
	if _, err := StartPITRRestore(context.Background(), newFakeAPI(), filepath.Join(t.TempDir(), "bad-window.json"), "us-east-1", "x", time.Now().Add(-72*time.Hour), []string{"items"}, true); err == nil {
		t.Fatal("restore outside recovery window should fail")
	}
	if _, err := StartBackupRestore(context.Background(), newFakeAPI(), bad, filepath.Join(t.TempDir(), "no-writes.json"), "us-east-1", "x", false); err == nil {
		t.Fatal("restore requires paused writers")
	}
}

func TestBackupCreationCanResumeByListingExistingBackup(t *testing.T) {
	db := newFakeAPI()
	path := filepath.Join(t.TempDir(), "backup.json")
	manifest := Manifest{Version: 1, Name: "set", Region: "us-east-1", Tables: []TableRecord{{SourceTable: "items", BackupName: "set-01-items"}}}
	if err := Save(path, manifest); err != nil {
		t.Fatal(err)
	}
	db.CreateBackup(context.Background(), &dynamodb.CreateBackupInput{TableName: aws.String("items"), BackupName: aws.String("set-01-items")})
	calls := db.backupCalls
	manifest, err := CreateSet(context.Background(), db, path, "set", "us-east-1", []string{"items"}, true, 0)
	if err != nil || manifest.Tables[0].BackupARN == "" || db.backupCalls != calls {
		t.Fatalf("resume by finding existing backup: %#v calls=%d err=%v", manifest, db.backupCalls, err)
	}
	db.listErr = errors.New("list failed")
	missing := filepath.Join(t.TempDir(), "new.json")
	if _, err := CreateSet(context.Background(), db, missing, "new", "us-east-1", []string{"items"}, true, 0); err == nil {
		t.Fatal("list failures should be returned")
	}
}
