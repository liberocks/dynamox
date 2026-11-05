// Package backup manages schema-wide DynamoDB backup sets and restores.
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type API interface {
	CreateBackup(context.Context, *dynamodb.CreateBackupInput, ...func(*dynamodb.Options)) (*dynamodb.CreateBackupOutput, error)
	ListBackups(context.Context, *dynamodb.ListBackupsInput, ...func(*dynamodb.Options)) (*dynamodb.ListBackupsOutput, error)
	DescribeBackup(context.Context, *dynamodb.DescribeBackupInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeBackupOutput, error)
	DescribeTable(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
	DescribeContinuousBackups(context.Context, *dynamodb.DescribeContinuousBackupsInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeContinuousBackupsOutput, error)
	RestoreTableFromBackup(context.Context, *dynamodb.RestoreTableFromBackupInput, ...func(*dynamodb.Options)) (*dynamodb.RestoreTableFromBackupOutput, error)
	RestoreTableToPointInTime(context.Context, *dynamodb.RestoreTableToPointInTimeInput, ...func(*dynamodb.Options)) (*dynamodb.RestoreTableToPointInTimeOutput, error)
}

type TableRecord struct {
	SourceTable      string             `json:"sourceTable"`
	SourceTableARN   string             `json:"sourceTableArn,omitempty"`
	BackupName       string             `json:"backupName,omitempty"`
	BackupARN        string             `json:"backupArn,omitempty"`
	BackupState      types.BackupStatus `json:"backupState,omitempty"`
	TargetTable      string             `json:"targetTable,omitempty"`
	RestoreRequested bool               `json:"restoreRequested,omitempty"`
	LineageVerified  bool               `json:"lineageVerified,omitempty"`
	Restored         bool               `json:"restored,omitempty"`
}

type Manifest struct {
	Version   int           `json:"version"`
	Name      string        `json:"name"`
	Region    string        `json:"region"`
	Mode      string        `json:"mode,omitempty"`
	RestoreAt *time.Time    `json:"restoreAt,omitempty"`
	CreatedAt time.Time     `json:"createdAt"`
	Tables    []TableRecord `json:"tables"`
}

type BackupSummary struct {
	TableName string                `json:"tableName"`
	Backups   []types.BackupSummary `json:"backups"`
}

func UniqueTables(names []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func CreateSet(ctx context.Context, db API, path, setName, region string, tables []string, writesPaused bool, settle time.Duration) (Manifest, error) {
	if !writesPaused {
		return Manifest{}, errors.New("backup set requires --writes-paused acknowledgment")
	}
	if strings.TrimSpace(setName) == "" || len(setName) > 200 {
		return Manifest{}, errors.New("backup set name must be non-empty and at most 200 characters")
	}
	tables = UniqueTables(tables)
	if len(tables) == 0 {
		return Manifest{}, errors.New("backup set has no tables")
	}
	manifest, err := Load(path)
	if err == nil {
		if manifest.Name != setName || manifest.Region != region || len(manifest.Tables) != len(tables) {
			return Manifest{}, errors.New("existing manifest does not match this backup set request")
		}
		for i, name := range tables {
			if manifest.Tables[i].SourceTable != name {
				return Manifest{}, errors.New("existing manifest table set does not match this schema")
			}
		}
	} else if !os.IsNotExist(err) {
		return Manifest{}, err
	} else {
		manifest = Manifest{Version: 1, Name: setName, Region: region, CreatedAt: time.Now().UTC()}
		for i, name := range tables {
			manifest.Tables = append(manifest.Tables, TableRecord{SourceTable: name, BackupName: backupName(setName, i, name)})
		}
		if err := Save(path, manifest); err != nil {
			return manifest, err
		}
	}
	for _, table := range manifest.Tables {
		if _, err := db.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(table.SourceTable)}); err != nil {
			return manifest, fmt.Errorf("inspect source table %s: %w", table.SourceTable, err)
		}
	}
	if settle > 0 {
		if err := wait(ctx, settle); err != nil {
			return manifest, err
		}
	}
	for i := range manifest.Tables {
		entry := &manifest.Tables[i]
		if entry.BackupARN == "" {
			arn, state, err := findBackup(ctx, db, entry.SourceTable, entry.BackupName)
			if err != nil {
				return manifest, err
			}
			if arn == "" {
				created, err := db.CreateBackup(ctx, &dynamodb.CreateBackupInput{TableName: aws.String(entry.SourceTable), BackupName: aws.String(entry.BackupName)})
				if err != nil {
					return manifest, fmt.Errorf("create backup for %s: %w", entry.SourceTable, err)
				}
				if created == nil || created.BackupDetails == nil || aws.ToString(created.BackupDetails.BackupArn) == "" {
					return manifest, fmt.Errorf("create backup for %s returned no backup ARN; rerun with the same manifest to resume", entry.SourceTable)
				}
				arn = aws.ToString(created.BackupDetails.BackupArn)
				state = created.BackupDetails.BackupStatus
			}
			entry.BackupARN, entry.BackupState = arn, state
			if err := Save(path, manifest); err != nil {
				return manifest, err
			}
		}
	}
	return RefreshBackupStatus(ctx, db, path)
}

func WaitBackupSet(ctx context.Context, db API, path string, interval time.Duration) (Manifest, error) {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	for {
		manifest, err := RefreshBackupStatus(ctx, db, path)
		if err != nil {
			return manifest, err
		}
		ready := true
		for _, entry := range manifest.Tables {
			if entry.BackupState != types.BackupStatusAvailable {
				ready = false
			}
			if entry.BackupState == types.BackupStatusDeleted {
				return manifest, fmt.Errorf("backup for %s entered terminal state %s", entry.SourceTable, entry.BackupState)
			}
		}
		if ready {
			return manifest, nil
		}
		if err := wait(ctx, interval); err != nil {
			return manifest, err
		}
	}
}

func backupName(set string, index int, table string) string {
	clean := regexp.MustCompile(`[^a-zA-Z0-9_.-]+`).ReplaceAllString(table, "-")
	name := fmt.Sprintf("%s-%02d-%s", set, index+1, clean)
	if len(name) > 255 {
		name = name[:255]
	}
	return name
}

func findBackup(ctx context.Context, db API, table, name string) (string, types.BackupStatus, error) {
	var cursor *string
	for {
		out, err := db.ListBackups(ctx, &dynamodb.ListBackupsInput{TableName: aws.String(table), BackupType: types.BackupTypeFilterUser, ExclusiveStartBackupArn: cursor})
		if err != nil {
			return "", "", fmt.Errorf("list backups for %s: %w", table, err)
		}
		if out != nil {
			for _, summary := range out.BackupSummaries {
				if aws.ToString(summary.BackupName) == name {
					return aws.ToString(summary.BackupArn), summary.BackupStatus, nil
				}
			}
			cursor = out.LastEvaluatedBackupArn
		}
		if cursor == nil || *cursor == "" {
			return "", "", nil
		}
	}
}

func RefreshBackupStatus(ctx context.Context, db API, path string) (Manifest, error) {
	manifest, err := Load(path)
	if err != nil {
		return manifest, err
	}
	for i := range manifest.Tables {
		entry := &manifest.Tables[i]
		if entry.BackupARN == "" {
			continue
		}
		out, err := db.DescribeBackup(ctx, &dynamodb.DescribeBackupInput{BackupArn: aws.String(entry.BackupARN)})
		if err != nil {
			return manifest, fmt.Errorf("describe backup for %s: %w", entry.SourceTable, err)
		}
		if out == nil || out.BackupDescription == nil || out.BackupDescription.BackupDetails == nil {
			return manifest, fmt.Errorf("describe backup for %s returned no backup details", entry.SourceTable)
		}
		entry.BackupState = out.BackupDescription.BackupDetails.BackupStatus
	}
	if err := Save(path, manifest); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func List(ctx context.Context, db API, tables []string) ([]BackupSummary, error) {
	var result []BackupSummary
	for _, table := range UniqueTables(tables) {
		item := BackupSummary{TableName: table}
		var cursor *string
		for {
			out, err := db.ListBackups(ctx, &dynamodb.ListBackupsInput{TableName: aws.String(table), BackupType: types.BackupTypeFilterUser, ExclusiveStartBackupArn: cursor})
			if err != nil {
				return result, fmt.Errorf("list backups for %s: %w", table, err)
			}
			if out != nil {
				item.Backups = append(item.Backups, out.BackupSummaries...)
				cursor = out.LastEvaluatedBackupArn
			}
			if cursor == nil || *cursor == "" {
				break
			}
		}
		result = append(result, item)
	}
	return result, nil
}

func StartBackupRestore(ctx context.Context, db API, backupPath, statePath, region, suffix string, writesPaused bool) (Manifest, error) {
	if !writesPaused {
		return Manifest{}, errors.New("restore requires --writes-paused acknowledgment")
	}
	if err := requireNewState(statePath); err != nil {
		return Manifest{}, err
	}
	manifest, err := Load(backupPath)
	if err != nil {
		return manifest, err
	}
	if manifest.Mode != "" && manifest.Mode != "backup" {
		return manifest, errors.New("manifest is not an on-demand backup set")
	}
	manifest.Mode = "backup"
	manifest.Region = region
	manifest.RestoreAt = nil
	if err := Save(statePath, manifest); err != nil {
		return manifest, err
	}
	return startRestore(ctx, db, statePath, &manifest, suffix, nil)
}

func StartPITRRestore(ctx context.Context, db API, path, region, suffix string, at time.Time, sourceTables []string, writesPaused bool) (Manifest, error) {
	if !writesPaused {
		return Manifest{}, errors.New("restore requires --writes-paused acknowledgment")
	}
	if err := requireNewState(path); err != nil {
		return Manifest{}, err
	}
	if at.IsZero() {
		return Manifest{}, errors.New("PITR restore requires --at timestamp")
	}
	at = at.UTC().Truncate(time.Second)
	manifest := Manifest{Version: 1, Name: "pitr-" + at.Format("20060102T150405Z"), Region: region, Mode: "pitr", RestoreAt: &at, CreatedAt: time.Now().UTC()}
	for _, name := range UniqueTables(sourceTables) {
		manifest.Tables = append(manifest.Tables, TableRecord{SourceTable: name})
	}
	if len(manifest.Tables) == 0 {
		return manifest, errors.New("PITR restore has no source tables")
	}
	if err := CheckPITRWindow(ctx, db, sourceTables, at); err != nil {
		return manifest, err
	}
	for i := range manifest.Tables {
		description, err := db.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(manifest.Tables[i].SourceTable)})
		if err != nil {
			return manifest, fmt.Errorf("inspect source table %s: %w", manifest.Tables[i].SourceTable, err)
		}
		if description == nil || description.Table == nil || aws.ToString(description.Table.TableArn) == "" {
			return manifest, fmt.Errorf("source table %s returned no ARN", manifest.Tables[i].SourceTable)
		}
		manifest.Tables[i].SourceTableARN = aws.ToString(description.Table.TableArn)
	}
	if err := Save(path, manifest); err != nil {
		return manifest, err
	}
	return startRestore(ctx, db, path, &manifest, suffix, &at)
}

func requireNewState(path string) error {
	if path == "" {
		return errors.New("restore state path is required")
	}
	_, err := os.Lstat(path)
	if err == nil {
		return errors.New("restore state file already exists; choose a new path")
	}
	if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func ResumeRestore(ctx context.Context, db API, path string, writesPaused bool) (Manifest, error) {
	if !writesPaused {
		return Manifest{}, errors.New("restore requires --writes-paused acknowledgment")
	}
	manifest, err := Load(path)
	if err != nil {
		return manifest, err
	}
	if manifest.Mode != "backup" && manifest.Mode != "pitr" {
		return manifest, errors.New("manifest does not contain a restore request")
	}
	var at *time.Time
	if manifest.Mode == "pitr" {
		at = manifest.RestoreAt
		if at == nil {
			return manifest, errors.New("PITR manifest has no restore timestamp")
		}
	}
	return startRestore(ctx, db, path, &manifest, "", at)
}

func startRestore(ctx context.Context, db API, path string, manifest *Manifest, suffix string, at *time.Time) (Manifest, error) {
	if len(manifest.Tables) == 0 {
		return *manifest, errors.New("restore manifest has no tables")
	}
	if suffix != "" {
		for i := range manifest.Tables {
			if manifest.Tables[i].TargetTable == "" {
				manifest.Tables[i].TargetTable = manifest.Tables[i].SourceTable + "-" + suffix
			}
		}
	}
	seen := map[string]bool{}
	for i := range manifest.Tables {
		target := manifest.Tables[i].TargetTable
		if target == "" {
			return *manifest, errors.New("restore manifest is missing a destination table name")
		}
		if len(target) < 3 || len(target) > 255 || !regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`).MatchString(target) {
			return *manifest, fmt.Errorf("invalid destination table name %q", target)
		}
		if seen[target] {
			return *manifest, fmt.Errorf("duplicate destination table %s", target)
		}
		seen[target] = true
		out, err := db.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(target)})
		if err == nil && out != nil && out.Table != nil {
			entry := manifest.Tables[i]
			verified := restoreMatches(out.Table, entry, manifest.Mode, manifest.RestoreAt) || entry.RestoreRequested
			if out.Table.TableStatus == types.TableStatusActive && !verified {
				return *manifest, fmt.Errorf("destination %s already exists and does not match this restore request", target)
			}
			continue
		}
		var missing *types.ResourceNotFoundException
		if err != nil && !errors.As(err, &missing) {
			return *manifest, fmt.Errorf("inspect destination table %s: %w", target, err)
		}
	}
	if err := Save(path, *manifest); err != nil {
		return *manifest, err
	}
	for i := range manifest.Tables {
		entry := &manifest.Tables[i]
		out, err := db.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(entry.TargetTable)})
		if err != nil {
			var missing *types.ResourceNotFoundException
			if !errors.As(err, &missing) {
				return *manifest, fmt.Errorf("inspect destination table %s: %w", entry.TargetTable, err)
			}
		} else if out != nil && out.Table != nil {
			matched := restoreMatches(out.Table, *entry, manifest.Mode, at)
			if matched {
				entry.RestoreRequested = true
				entry.LineageVerified = true
			}
			verified := matched || entry.RestoreRequested
			if !verified {
				if out.Table.TableStatus == types.TableStatusActive {
					return *manifest, fmt.Errorf("destination %s already exists and does not match this restore request", entry.TargetTable)
				}
				entry.Restored = false
				if err := Save(path, *manifest); err != nil {
					return *manifest, err
				}
				continue
			}
			entry.Restored = out.Table.TableStatus == types.TableStatusActive
			if err := Save(path, *manifest); err != nil {
				return *manifest, err
			}
			continue
		}
		if manifest.Mode == "backup" {
			if entry.BackupARN == "" {
				return *manifest, fmt.Errorf("backup ARN missing for source table %s", entry.SourceTable)
			}
			backup, describeErr := db.DescribeBackup(ctx, &dynamodb.DescribeBackupInput{BackupArn: aws.String(entry.BackupARN)})
			if describeErr != nil {
				return *manifest, fmt.Errorf("inspect backup for %s: %w", entry.SourceTable, describeErr)
			}
			if backup == nil || backup.BackupDescription == nil || backup.BackupDescription.BackupDetails == nil || backup.BackupDescription.BackupDetails.BackupStatus != types.BackupStatusAvailable {
				return *manifest, fmt.Errorf("backup for %s is not available", entry.SourceTable)
			}
			_, err = db.RestoreTableFromBackup(ctx, &dynamodb.RestoreTableFromBackupInput{BackupArn: aws.String(entry.BackupARN), TargetTableName: aws.String(entry.TargetTable)})
		} else {
			_, err = db.RestoreTableToPointInTime(ctx, &dynamodb.RestoreTableToPointInTimeInput{SourceTableName: aws.String(entry.SourceTable), TargetTableName: aws.String(entry.TargetTable), RestoreDateTime: at})
		}
		if err != nil {
			return *manifest, fmt.Errorf("start restore for %s to %s: %w", entry.SourceTable, entry.TargetTable, err)
		}
		entry.RestoreRequested = true
		if err := Save(path, *manifest); err != nil {
			return *manifest, err
		}
	}
	return RefreshRestoreStatus(ctx, db, path)
}

func restoreMatches(table *types.TableDescription, entry TableRecord, mode string, at *time.Time) bool {
	if table.RestoreSummary == nil {
		return false
	}
	if mode == "backup" {
		return aws.ToString(table.RestoreSummary.SourceBackupArn) == entry.BackupARN
	}
	return at != nil && table.RestoreSummary.RestoreDateTime != nil && table.RestoreSummary.RestoreDateTime.UTC().Truncate(time.Second).Equal(at.UTC().Truncate(time.Second)) && aws.ToString(table.RestoreSummary.SourceTableArn) == entry.SourceTableARN
}

func WaitRestoreSet(ctx context.Context, db API, path string, interval time.Duration) (Manifest, error) {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	for {
		manifest, err := RefreshRestoreStatus(ctx, db, path)
		if err != nil {
			return manifest, err
		}
		ready := true
		for _, entry := range manifest.Tables {
			if !entry.Restored {
				ready = false
			}
		}
		if ready {
			return manifest, nil
		}
		if err := wait(ctx, interval); err != nil {
			return manifest, err
		}
	}
}

func RefreshRestoreStatus(ctx context.Context, db API, path string) (Manifest, error) {
	manifest, err := Load(path)
	if err != nil {
		return manifest, err
	}
	for i := range manifest.Tables {
		entry := &manifest.Tables[i]
		out, err := db.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(entry.TargetTable)})
		if err != nil {
			var missing *types.ResourceNotFoundException
			if errors.As(err, &missing) {
				entry.Restored = false
				continue
			}
			return manifest, fmt.Errorf("check restored table %s: %w", entry.TargetTable, err)
		}
		if out == nil || out.Table == nil {
			return manifest, fmt.Errorf("restored table %s does not match manifest", entry.TargetTable)
		}
		matched := restoreMatches(out.Table, *entry, manifest.Mode, manifest.RestoreAt)
		if matched {
			entry.RestoreRequested = true
			entry.LineageVerified = true
		}
		if !matched && !entry.RestoreRequested {
			if out.Table.TableStatus != types.TableStatusActive {
				entry.Restored = false
				continue
			}
			return manifest, fmt.Errorf("restored table %s does not match manifest", entry.TargetTable)
		}
		entry.Restored = out.Table.TableStatus == types.TableStatusActive
	}
	if err := Save(path, manifest); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func CheckPITRWindow(ctx context.Context, db API, tables []string, at time.Time) error {
	var earliest, latest time.Time
	for _, name := range UniqueTables(tables) {
		out, err := db.DescribeContinuousBackups(ctx, &dynamodb.DescribeContinuousBackupsInput{TableName: aws.String(name)})
		if err != nil {
			return fmt.Errorf("inspect PITR window for %s: %w", name, err)
		}
		if out == nil || out.ContinuousBackupsDescription == nil || out.ContinuousBackupsDescription.PointInTimeRecoveryDescription == nil {
			return fmt.Errorf("table %s has no PITR recovery window", name)
		}
		desc := out.ContinuousBackupsDescription.PointInTimeRecoveryDescription
		if desc.PointInTimeRecoveryStatus != types.PointInTimeRecoveryStatusEnabled || desc.EarliestRestorableDateTime == nil || desc.LatestRestorableDateTime == nil {
			return fmt.Errorf("table %s does not have an active PITR recovery window", name)
		}
		e := *desc.EarliestRestorableDateTime
		l := *desc.LatestRestorableDateTime
		if earliest.IsZero() || e.After(earliest) {
			earliest = e
		}
		if latest.IsZero() || l.Before(latest) {
			latest = l
		}
	}
	if at.Before(earliest) || at.After(latest) {
		return fmt.Errorf("restore time is outside the common PITR window %s through %s", earliest.UTC().Format(time.RFC3339), latest.UTC().Format(time.RFC3339))
	}
	return nil
}

func Load(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	if manifest.Version != 1 || len(manifest.Tables) == 0 {
		return Manifest{}, errors.New("unsupported or incomplete backup manifest")
	}
	return manifest, nil
}

func Save(path string, manifest Manifest) error {
	if path == "" {
		return errors.New("manifest path is required")
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dynamox-manifest-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func TableMappings(manifest Manifest) map[string]string {
	mappings := make(map[string]string, len(manifest.Tables))
	for _, table := range manifest.Tables {
		if table.TargetTable != "" {
			mappings[table.SourceTable] = table.TargetTable
		}
	}
	return mappings
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
