// Package runtime contains the small DynamoDB runtime used by generated dynamox clients.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// DynamoAPI is implemented by *dynamodb.Client and by test doubles.
type DynamoAPI interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	Scan(context.Context, *dynamodb.ScanInput, ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
	TransactGetItems(context.Context, *dynamodb.TransactGetItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactGetItemsOutput, error)
	TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
	DescribeTable(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
	CreateTable(context.Context, *dynamodb.CreateTableInput, ...func(*dynamodb.Options)) (*dynamodb.CreateTableOutput, error)
}

type Codec[T any] struct {
	Encode func(T) (map[string]types.AttributeValue, error)
	Decode func(map[string]types.AttributeValue) (T, error)
}
type Table[T any, K any] struct {
	DB            DynamoAPI
	Name          string
	PartitionName string
	SortName      string
	Codec         Codec[T]
	Key           func(K) (map[string]types.AttributeValue, error)
}
type PageRequest struct {
	Limit   int32
	Cursor  map[string]types.AttributeValue
	Forward *bool
}
type Page[T any] struct {
	Items  []T
	Cursor map[string]types.AttributeValue
}
type Operator string

const (
	Equal        Operator = "="
	Less         Operator = "<"
	LessEqual    Operator = "<="
	Greater      Operator = ">"
	GreaterEqual Operator = ">="
	BeginsWith   Operator = "begins_with"
)

type Condition struct {
	Operator Operator
	Value    any
}
type QueryRequest struct {
	IndexName, PartitionName string
	PartitionValue           any
	SortName                 string
	Sort                     *Condition
	Page                     PageRequest
}

func (t Table[T, K]) Get(ctx context.Context, key K) (T, bool, error) {
	var zero T
	k, err := t.Key(key)
	if err != nil {
		return zero, false, err
	}
	out, err := t.DB.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(t.Name), Key: k})
	if err != nil {
		return zero, false, err
	}
	if len(out.Item) == 0 {
		return zero, false, nil
	}
	v, err := t.Codec.Decode(out.Item)
	return v, true, err
}
func (t Table[T, K]) Create(ctx context.Context, v T) error {
	item, err := t.Codec.Encode(v)
	if err != nil {
		return err
	}
	keyField := "#pk"
	keyName := t.PartitionName
	if t.SortName != "" {
		keyField = "#sk"
		keyName = t.SortName
	}
	_, err = t.DB.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(t.Name), Item: item, ConditionExpression: aws.String("attribute_not_exists(" + keyField + ")"), ExpressionAttributeNames: map[string]string{keyField: keyName}})
	return err
}
func (t Table[T, K]) Delete(ctx context.Context, key K) error {
	k, err := t.Key(key)
	if err != nil {
		return err
	}
	_, err = t.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(t.Name), Key: k})
	return err
}

// Update writes only supplied attributes and rejects an empty update or key mutation.
func (t Table[T, K]) Update(ctx context.Context, key K, set map[string]types.AttributeValue, remove []string, keyNames ...string) error {
	if len(set) == 0 && len(remove) == 0 {
		return errors.New("dynamox: update has no fields")
	}
	for i, name := range remove {
		if _, ok := set[name]; ok {
			return fmt.Errorf("dynamox: update field %q cannot be both set and removed", name)
		}
		for _, other := range remove[:i] {
			if other == name {
				return fmt.Errorf("dynamox: update field %q is removed more than once", name)
			}
		}
	}
	k, err := t.Key(key)
	if err != nil {
		return err
	}
	for _, name := range keyNames {
		if _, ok := set[name]; ok {
			return fmt.Errorf("dynamox: key field %q cannot be updated", name)
		}
		for _, r := range remove {
			if r == name {
				return fmt.Errorf("dynamox: key field %q cannot be updated", name)
			}
		}
	}
	var clauses []string
	names := map[string]string{}
	values := map[string]types.AttributeValue{}
	i := 0
	for _, name := range sortedKeys(set) {
		val := set[name]
		if val == nil {
			return fmt.Errorf("dynamox: update field %q has nil value", name)
		}
		n := fmt.Sprintf("#f%d", i)
		v := fmt.Sprintf(":v%d", i)
		names[n] = name
		values[v] = val
		clauses = append(clauses, n+" = "+v)
		i++
	}
	var removeNames []string
	for _, name := range remove {
		n := fmt.Sprintf("#f%d", i)
		i++
		names[n] = name
		removeNames = append(removeNames, n)
	}
	parts := make([]string, 0, 2)
	if len(clauses) > 0 {
		parts = append(parts, "SET "+strings.Join(clauses, ", "))
	}
	if len(removeNames) > 0 {
		parts = append(parts, "REMOVE "+strings.Join(removeNames, ", "))
	}
	var valueNames map[string]types.AttributeValue
	if len(values) > 0 {
		valueNames = values
	}
	_, err = t.DB.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: aws.String(t.Name), Key: k, UpdateExpression: aws.String(strings.Join(parts, " ")), ExpressionAttributeNames: names, ExpressionAttributeValues: valueNames})
	return err
}
func (t Table[T, K]) Query(ctx context.Context, q QueryRequest) (Page[T], error) {
	var result Page[T]
	if q.PartitionName == "" {
		return result, errors.New("dynamox: query requires partition key")
	}
	pk, err := marshalValue(q.PartitionValue)
	if err != nil {
		return result, err
	}
	cond := "#pk = :pk"
	names := map[string]string{"#pk": q.PartitionName}
	vals := map[string]types.AttributeValue{":pk": pk}
	if q.Sort != nil {
		if q.SortName == "" {
			return result, errors.New("dynamox: sort condition provided without sort key")
		}
		sv, e := marshalValue(q.Sort.Value)
		if e != nil {
			return result, e
		}
		names["#sk"] = q.SortName
		vals[":sk"] = sv
		switch q.Sort.Operator {
		case Equal, Less, LessEqual, Greater, GreaterEqual:
			cond += " AND #sk " + string(q.Sort.Operator) + " :sk"
		case BeginsWith:
			cond += " AND begins_with(#sk, :sk)"
		default:
			return result, fmt.Errorf("dynamox: unsupported sort operator %q", q.Sort.Operator)
		}
	}
	in := &dynamodb.QueryInput{TableName: aws.String(t.Name), KeyConditionExpression: aws.String(cond), ExpressionAttributeNames: names, ExpressionAttributeValues: vals, ExclusiveStartKey: q.Page.Cursor, ScanIndexForward: q.Page.Forward}
	if q.Page.Limit > 0 {
		in.Limit = aws.Int32(q.Page.Limit)
	}
	if q.IndexName != "" {
		in.IndexName = aws.String(q.IndexName)
	}
	out, err := t.DB.Query(ctx, in)
	if err != nil {
		return result, err
	}
	result.Cursor = out.LastEvaluatedKey
	result.Items = make([]T, 0, len(out.Items))
	for _, item := range out.Items {
		v, e := t.Codec.Decode(item)
		if e != nil {
			return Page[T]{}, e
		}
		result.Items = append(result.Items, v)
	}
	return result, nil
}
func (t Table[T, K]) Scan(ctx context.Context, p PageRequest) (Page[T], error) {
	var result Page[T]
	in := &dynamodb.ScanInput{TableName: aws.String(t.Name), ExclusiveStartKey: p.Cursor}
	if p.Limit > 0 {
		in.Limit = aws.Int32(p.Limit)
	}
	out, err := t.DB.Scan(ctx, in)
	if err != nil {
		return result, err
	}
	result.Cursor = out.LastEvaluatedKey
	result.Items = make([]T, 0, len(out.Items))
	for _, item := range out.Items {
		v, e := t.Codec.Decode(item)
		if e != nil {
			return Page[T]{}, e
		}
		result.Items = append(result.Items, v)
	}
	return result, nil
}
func StringKey(value string) types.AttributeValue { return &types.AttributeValueMemberS{Value: value} }
func IntKey(value int64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(value, 10)}
}

func marshalValue(value any) (types.AttributeValue, error) {
	switch v := value.(type) {
	case time.Time:
		return &types.AttributeValueMemberS{Value: v.UTC().Format(time.RFC3339Nano)}, nil
	case []byte:
		return &types.AttributeValueMemberB{Value: v}, nil
	default:
		return attributevalue.Marshal(value)
	}
}

type ReadItem struct {
	Table  string
	Key    map[string]types.AttributeValue
	Decode func(map[string]types.AttributeValue) error
}
type WriteItem struct {
	Table    string
	Put      map[string]types.AttributeValue
	Key      map[string]types.AttributeValue
	Set      map[string]types.AttributeValue
	Remove   []string
	KeyNames []string
}

func Read[T any, K any](table Table[T, K], key K, dst *T) (ReadItem, error) {
	if dst == nil {
		return ReadItem{}, errors.New("dynamox: transaction read destination is nil")
	}
	k, err := table.Key(key)
	if err != nil {
		return ReadItem{}, err
	}
	return ReadItem{Table: table.Name, Key: k, Decode: func(item map[string]types.AttributeValue) error {
		v, e := table.Codec.Decode(item)
		if e == nil {
			*dst = v
		}
		return e
	}}, nil
}
func (t Table[T, K]) PutOperation(v T) (WriteItem, error) {
	item, err := t.Codec.Encode(v)
	if err != nil {
		return WriteItem{}, err
	}
	return WriteItem{Table: t.Name, Put: item}, nil
}
func (t Table[T, K]) DeleteOperation(key K) (WriteItem, error) {
	k, err := t.Key(key)
	if err != nil {
		return WriteItem{}, err
	}
	return WriteItem{Table: t.Name, Key: k}, nil
}
func (t Table[T, K]) UpdateOperation(key K, set map[string]types.AttributeValue, remove []string, keyNames ...string) (WriteItem, error) {
	k, err := t.Key(key)
	if err != nil {
		return WriteItem{}, err
	}
	if len(set) == 0 && len(remove) == 0 {
		return WriteItem{}, errors.New("dynamox: update has no fields")
	}
	return WriteItem{Table: t.Name, Key: k, Set: set, Remove: remove, KeyNames: keyNames}, nil
}
func TransactGet(ctx context.Context, db DynamoAPI, items ...ReadItem) error {
	if len(items) == 0 {
		return errors.New("dynamox: transaction has no reads")
	}
	if len(items) > 100 {
		return errors.New("dynamox: DynamoDB transaction read limit is 100")
	}
	req := make([]types.TransactGetItem, 0, len(items))
	for _, i := range items {
		req = append(req, types.TransactGetItem{Get: &types.Get{TableName: aws.String(i.Table), Key: i.Key}})
	}
	out, err := db.TransactGetItems(ctx, &dynamodb.TransactGetItemsInput{TransactItems: req})
	if err != nil {
		return err
	}
	if len(out.Responses) != len(items) {
		return errors.New("dynamox: transaction returned an unexpected number of items")
	}
	for i, r := range out.Responses {
		if len(r.Item) == 0 {
			return fmt.Errorf("dynamox: transaction item %d not found", i)
		}
		if items[i].Decode != nil {
			if err := items[i].Decode(r.Item); err != nil {
				return err
			}
		}
	}
	return nil
}
func TransactWrite(ctx context.Context, db DynamoAPI, items ...WriteItem) error {
	if len(items) == 0 {
		return errors.New("dynamox: transaction has no writes")
	}
	if len(items) > 100 {
		return errors.New("dynamox: DynamoDB transaction write limit is 100")
	}
	req := make([]types.TransactWriteItem, 0, len(items))
	for _, i := range items {
		switch {
		case i.Put != nil:
			req = append(req, types.TransactWriteItem{Put: &types.Put{TableName: aws.String(i.Table), Item: i.Put}})
		case i.Set != nil || len(i.Remove) > 0:
			if len(i.Set) == 0 && len(i.Remove) == 0 {
				return errors.New("dynamox: update has no fields")
			}
			for _, name := range i.KeyNames {
				if _, ok := i.Set[name]; ok {
					return fmt.Errorf("dynamox: key field %q cannot be updated", name)
				}
			}
			for n, name := range i.Remove {
				if _, ok := i.Set[name]; ok {
					return fmt.Errorf("dynamox: update field %q cannot be both set and removed", name)
				}
				for _, other := range i.Remove[:n] {
					if other == name {
						return fmt.Errorf("dynamox: update field %q is removed more than once", name)
					}
				}
			}
			names := map[string]string{}
			values := map[string]types.AttributeValue{}
			var sets []string
			for j, name := range sortedKeys(i.Set) {
				n := fmt.Sprintf("#f%d", j)
				x := fmt.Sprintf(":v%d", j)
				names[n] = name
				values[x] = i.Set[name]
				sets = append(sets, n+" = "+x)
			}
			var removes []string
			for _, name := range i.Remove {
				for _, keyName := range i.KeyNames {
					if name == keyName {
						return fmt.Errorf("dynamox: key field %q cannot be updated", name)
					}
				}
				n := fmt.Sprintf("#r%d", len(names))
				names[n] = name
				removes = append(removes, n)
			}
			var parts []string
			if len(sets) > 0 {
				parts = append(parts, "SET "+strings.Join(sets, ", "))
			}
			if len(removes) > 0 {
				parts = append(parts, "REMOVE "+strings.Join(removes, ", "))
			}
			var valueNames map[string]types.AttributeValue
			if len(values) > 0 {
				valueNames = values
			}
			req = append(req, types.TransactWriteItem{Update: &types.Update{TableName: aws.String(i.Table), Key: i.Key, UpdateExpression: aws.String(strings.Join(parts, " ")), ExpressionAttributeNames: names, ExpressionAttributeValues: valueNames}})
		case i.Key != nil:
			req = append(req, types.TransactWriteItem{Delete: &types.Delete{TableName: aws.String(i.Table), Key: i.Key}})
		default:
			return errors.New("dynamox: invalid transaction write")
		}
	}
	_, err := db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: req})
	return err
}
func sortedKeys[V any](m map[string]V) []string {
	r := make([]string, 0, len(m))
	for k := range m {
		r = append(r, k)
	}
	sort.Strings(r)
	return r
}
