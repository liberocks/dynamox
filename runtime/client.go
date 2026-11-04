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
type JSON any
type ClientOptions struct {
	TableNames  map[string]string
	IDGenerator IDGenerator
}

func (o ClientOptions) TableName(schemaName string) string {
	if name := o.TableNames[schemaName]; name != "" {
		return name
	}
	return schemaName
}

type Table[T any, K any] struct {
	DB            DynamoAPI
	Name          string
	PartitionName string
	SortName      string
	Codec         Codec[T]
	Key           func(K) (map[string]types.AttributeValue, error)
}
type Expression struct {
	Text   string
	Names  map[string]string
	Values map[string]any
}
type ReadOptions struct{ ConsistentRead bool }
type WriteOptions struct{ Condition *Expression }
type ScanOptions struct {
	Page   PageRequest
	Filter *Expression
}
type PageRequest struct {
	Limit          int32
	Cursor         map[string]types.AttributeValue
	Forward        *bool
	ConsistentRead bool
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
	GlobalIndex              bool
}

func (t Table[T, K]) Get(ctx context.Context, key K) (T, bool, error) {
	return t.GetWithOptions(ctx, key, ReadOptions{})
}
func (t Table[T, K]) GetWithOptions(ctx context.Context, key K, options ReadOptions) (T, bool, error) {
	var zero T
	k, err := t.Key(key)
	if err != nil {
		return zero, false, err
	}
	out, err := t.DB.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(t.Name), Key: k, ConsistentRead: aws.Bool(options.ConsistentRead)})
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
	return t.CreateWithOptions(ctx, v, WriteOptions{})
}
func (t Table[T, K]) CreateWithOptions(ctx context.Context, v T, options WriteOptions) error {
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
	names, values, condition, err := expressionMaps(options.Condition)
	if err != nil {
		return err
	}
	keyField = uniquePlaceholder(names, keyField)
	names[keyField] = keyName
	condition = "attribute_not_exists(" + keyField + ")" + conditionSuffix(condition)
	_, err = t.DB.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(t.Name), Item: item, ConditionExpression: aws.String(condition), ExpressionAttributeNames: names, ExpressionAttributeValues: values})
	return err
}
func (t Table[T, K]) Put(ctx context.Context, v T, options ...WriteOptions) error {
	item, err := t.Codec.Encode(v)
	if err != nil {
		return err
	}
	var opt WriteOptions
	if len(options) > 0 {
		opt = options[0]
	}
	names, values, _, err := expressionMaps(opt.Condition)
	if err != nil {
		return err
	}
	in := &dynamodb.PutItemInput{TableName: aws.String(t.Name), Item: item, ExpressionAttributeNames: nonEmptyNames(names), ExpressionAttributeValues: values}
	setCondition(&in.ConditionExpression, opt.Condition)
	_, err = t.DB.PutItem(ctx, in)
	return err
}
func (t Table[T, K]) Delete(ctx context.Context, key K) error {
	return t.DeleteWithOptions(ctx, key, WriteOptions{})
}
func (t Table[T, K]) DeleteWithOptions(ctx context.Context, key K, options WriteOptions) error {
	k, err := t.Key(key)
	if err != nil {
		return err
	}
	names, values, _, err := expressionMaps(options.Condition)
	if err != nil {
		return err
	}
	in := &dynamodb.DeleteItemInput{TableName: aws.String(t.Name), Key: k, ExpressionAttributeNames: nonEmptyNames(names), ExpressionAttributeValues: values}
	setCondition(&in.ConditionExpression, options.Condition)
	_, err = t.DB.DeleteItem(ctx, in)
	return err
}

// Update writes only supplied attributes and rejects an empty update or key mutation.
func (t Table[T, K]) Update(ctx context.Context, key K, set map[string]types.AttributeValue, remove []string, keyNames ...string) error {
	return t.UpdateWithOptions(ctx, key, set, remove, WriteOptions{}, keyNames...)
}
func (t Table[T, K]) UpdateWithOptions(ctx context.Context, key K, set map[string]types.AttributeValue, remove []string, options WriteOptions, keyNames ...string) error {
	_, err := t.update(ctx, key, set, remove, options, types.ReturnValueNone, keyNames...)
	return err
}
func (t Table[T, K]) UpdateReturning(ctx context.Context, key K, set map[string]types.AttributeValue, remove []string, options WriteOptions, keyNames ...string) (T, error) {
	return t.update(ctx, key, set, remove, options, types.ReturnValueAllNew, keyNames...)
}
func (t Table[T, K]) update(ctx context.Context, key K, set map[string]types.AttributeValue, remove []string, options WriteOptions, returnValues types.ReturnValue, keyNames ...string) (T, error) {
	var zero T
	if len(set) == 0 && len(remove) == 0 {
		return zero, errors.New("dynamox: update has no fields")
	}
	for i, name := range remove {
		if _, ok := set[name]; ok {
			return zero, fmt.Errorf("dynamox: update field %q cannot be both set and removed", name)
		}
		for _, other := range remove[:i] {
			if other == name {
				return zero, fmt.Errorf("dynamox: update field %q is removed more than once", name)
			}
		}
	}
	k, err := t.Key(key)
	if err != nil {
		return zero, err
	}
	for _, name := range keyNames {
		if _, ok := set[name]; ok {
			return zero, fmt.Errorf("dynamox: key field %q cannot be updated", name)
		}
		for _, r := range remove {
			if r == name {
				return zero, fmt.Errorf("dynamox: key field %q cannot be updated", name)
			}
		}
	}
	var clauses []string
	names, values, _, err := expressionMaps(options.Condition)
	if err != nil {
		return zero, err
	}
	if values == nil {
		values = map[string]types.AttributeValue{}
	}
	valueFields := map[string]types.AttributeValue{}
	i := 0
	for _, name := range sortedKeys(set) {
		val := set[name]
		if val == nil {
			return zero, fmt.Errorf("dynamox: update field %q has nil value", name)
		}
		n := uniquePlaceholder(names, fmt.Sprintf("#dx_f%d", i))
		v := uniquePlaceholderAV(values, fmt.Sprintf(":dx_v%d", i))
		names[n] = name
		valueFields[v] = val
		clauses = append(clauses, n+" = "+v)
		i++
	}
	var removeNames []string
	for _, name := range remove {
		n := uniquePlaceholder(names, fmt.Sprintf("#dx_f%d", i))
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
	for k, v := range valueFields {
		values[k] = v
	}
	var valueNames map[string]types.AttributeValue
	if len(values) > 0 {
		valueNames = values
	}
	in := &dynamodb.UpdateItemInput{TableName: aws.String(t.Name), Key: k, UpdateExpression: aws.String(strings.Join(parts, " ")), ExpressionAttributeNames: nonEmptyNames(names), ExpressionAttributeValues: valueNames, ReturnValues: returnValues}
	setCondition(&in.ConditionExpression, options.Condition)
	out, err := t.DB.UpdateItem(ctx, in)
	if err != nil {
		return zero, err
	}
	if returnValues == types.ReturnValueAllNew {
		v, e := t.Codec.Decode(out.Attributes)
		return v, e
	}
	return zero, nil
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
	if q.Page.ConsistentRead {
		if q.GlobalIndex {
			return result, errors.New("dynamox: consistent reads are not supported on GSIs")
		}
		in.ConsistentRead = aws.Bool(true)
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
	return t.ScanWithOptions(ctx, ScanOptions{Page: p})
}
func (t Table[T, K]) ScanWithOptions(ctx context.Context, options ScanOptions) (Page[T], error) {
	var result Page[T]
	p := options.Page
	names, values, _, err := expressionMaps(options.Filter)
	if err != nil {
		return result, err
	}
	in := &dynamodb.ScanInput{TableName: aws.String(t.Name), ExclusiveStartKey: p.Cursor, ConsistentRead: aws.Bool(p.ConsistentRead), ExpressionAttributeNames: nonEmptyNames(names), ExpressionAttributeValues: values}
	if p.Limit > 0 {
		in.Limit = aws.Int32(p.Limit)
	}
	if options.Filter != nil && options.Filter.Text != "" {
		in.FilterExpression = aws.String(options.Filter.Text)
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
	Table      string
	Put        map[string]types.AttributeValue
	Key        map[string]types.AttributeValue
	Set        map[string]types.AttributeValue
	Remove     []string
	KeyNames   []string
	Condition  *Expression
	CreateOnly bool
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
	return t.PutOperationWithOptions(v, WriteOptions{})
}
func (t Table[T, K]) PutOperationWithOptions(v T, options WriteOptions) (WriteItem, error) {
	item, err := t.Codec.Encode(v)
	if err != nil {
		return WriteItem{}, err
	}
	_, _, _, err = expressionMaps(options.Condition)
	if err != nil {
		return WriteItem{}, err
	}
	return WriteItem{Table: t.Name, Put: item, Condition: options.Condition}, nil
}
func (t Table[T, K]) CreateOperation(v T, options WriteOptions) (WriteItem, error) {
	i, err := t.Codec.Encode(v)
	if err != nil {
		return WriteItem{}, err
	}
	names, _, condition, err := expressionMaps(options.Condition)
	if err != nil {
		return WriteItem{}, err
	}
	pk := uniquePlaceholder(names, "#dx_create_pk")
	keyName := t.PartitionName
	if t.SortName != "" {
		keyName = t.SortName
	}
	names[pk] = keyName
	text := "attribute_not_exists(" + pk + ")" + conditionSuffix(condition)
	rawValues := map[string]any{}
	if options.Condition != nil {
		for k, v := range options.Condition.Values {
			rawValues[k] = v
		}
	}
	return WriteItem{Table: t.Name, Put: i, Condition: &Expression{Text: text, Names: names, Values: rawValues}, CreateOnly: true}, nil
}
func (t Table[T, K]) DeleteOperation(key K) (WriteItem, error) {
	return t.DeleteOperationWithOptions(key, WriteOptions{})
}
func (t Table[T, K]) DeleteOperationWithOptions(key K, options WriteOptions) (WriteItem, error) {
	k, err := t.Key(key)
	if err != nil {
		return WriteItem{}, err
	}
	_, _, _, err = expressionMaps(options.Condition)
	if err != nil {
		return WriteItem{}, err
	}
	return WriteItem{Table: t.Name, Key: k, Condition: options.Condition}, nil
}
func (t Table[T, K]) UpdateOperation(key K, set map[string]types.AttributeValue, remove []string, keyNames ...string) (WriteItem, error) {
	return t.UpdateOperationWithOptions(key, set, remove, WriteOptions{}, keyNames...)
}
func (t Table[T, K]) UpdateOperationWithOptions(key K, set map[string]types.AttributeValue, remove []string, options WriteOptions, keyNames ...string) (WriteItem, error) {
	k, err := t.Key(key)
	if err != nil {
		return WriteItem{}, err
	}
	if len(set) == 0 && len(remove) == 0 {
		return WriteItem{}, errors.New("dynamox: update has no fields")
	}
	_, _, _, err = expressionMaps(options.Condition)
	if err != nil {
		return WriteItem{}, err
	}
	return WriteItem{Table: t.Name, Key: k, Set: set, Remove: remove, KeyNames: keyNames, Condition: options.Condition}, nil
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
			put := &types.Put{TableName: aws.String(i.Table), Item: i.Put}
			setCondition(&put.ConditionExpression, i.Condition)
			if i.Condition != nil {
				put.ExpressionAttributeNames = nonEmptyNames(i.Condition.Names)
				values, e := marshalExpressionValues(i.Condition.Values)
				if e != nil {
					return e
				}
				put.ExpressionAttributeValues = values
			}
			req = append(req, types.TransactWriteItem{Put: put})
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
			names, values, _, err := expressionMaps(i.Condition)
			if err != nil {
				return err
			}
			if values == nil {
				values = map[string]types.AttributeValue{}
			}
			fieldValues := map[string]types.AttributeValue{}
			var sets []string
			for j, name := range sortedKeys(i.Set) {
				n := uniquePlaceholder(names, fmt.Sprintf("#dx_f%d", j))
				x := uniquePlaceholderAV(values, fmt.Sprintf(":dx_v%d", j))
				names[n] = name
				fieldValues[x] = i.Set[name]
				sets = append(sets, n+" = "+x)
			}
			var removes []string
			for _, name := range i.Remove {
				for _, keyName := range i.KeyNames {
					if name == keyName {
						return fmt.Errorf("dynamox: key field %q cannot be updated", name)
					}
				}
				n := uniquePlaceholder(names, fmt.Sprintf("#dx_r%d", len(removes)))
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
			for k, v := range fieldValues {
				values[k] = v
			}
			var valueNames map[string]types.AttributeValue
			if len(values) > 0 {
				valueNames = values
			}
			u := &types.Update{TableName: aws.String(i.Table), Key: i.Key, UpdateExpression: aws.String(strings.Join(parts, " ")), ExpressionAttributeNames: nonEmptyNames(names), ExpressionAttributeValues: valueNames}
			setCondition(&u.ConditionExpression, i.Condition)
			req = append(req, types.TransactWriteItem{Update: u})
		case i.Key != nil:
			d := &types.Delete{TableName: aws.String(i.Table), Key: i.Key}
			if i.Condition != nil {
				d.ExpressionAttributeNames = nonEmptyNames(i.Condition.Names)
				values, e := marshalExpressionValues(i.Condition.Values)
				if e != nil {
					return e
				}
				d.ExpressionAttributeValues = values
			}
			setCondition(&d.ConditionExpression, i.Condition)
			req = append(req, types.TransactWriteItem{Delete: d})
		default:
			return errors.New("dynamox: invalid transaction write")
		}
	}
	_, err := db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: req})
	return err
}

func expressionMaps(e *Expression) (map[string]string, map[string]types.AttributeValue, string, error) {
	names := map[string]string{}
	text := ""
	if e != nil {
		for k, v := range e.Names {
			names[k] = v
		}
		values, err := marshalExpressionValues(e.Values)
		if err != nil {
			return nil, nil, "", err
		}
		return names, values, e.Text, nil
	}
	return names, nil, text, nil
}
func marshalExpressionValues(values map[string]any) (map[string]types.AttributeValue, error) {
	if len(values) == 0 {
		return nil, nil
	}
	encoded, err := attributevalue.MarshalMap(values)
	if err != nil {
		return nil, err
	}
	for name := range values {
		if _, ok := encoded[name]; !ok {
			return nil, fmt.Errorf("dynamox: condition value %q has an unsupported type", name)
		}
	}
	return encoded, nil
}
func setCondition(dst **string, e *Expression) {
	if e != nil && e.Text != "" {
		*dst = aws.String(e.Text)
	}
}
func conditionSuffix(text string) string {
	if text == "" {
		return ""
	}
	return " AND (" + text + ")"
}
func uniquePlaceholder[V any](existing map[string]V, base string) string {
	p := base
	for i := 0; ; i++ {
		if _, ok := existing[p]; !ok {
			return p
		}
		p = fmt.Sprintf("%s_%d", base, i+1)
	}
}
func uniquePlaceholderAV(existing map[string]types.AttributeValue, base string) string {
	return uniquePlaceholder(existing, base)
}
func nonEmptyNames(names map[string]string) map[string]string {
	if len(names) == 0 {
		return nil
	}
	return names
}
func sortedKeys[V any](m map[string]V) []string {
	r := make([]string, 0, len(m))
	for k := range m {
		r = append(r, k)
	}
	sort.Strings(r)
	return r
}
