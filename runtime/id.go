package runtime

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// SequenceTableName is created by `dynamox sync` when a schema uses @default(serial).
const SequenceTableName = "dynamox_sequences"

// IDGenerator generates string IDs for models that declare a UUID or ObjectID default.
type IDGenerator func(string) (string, error)

var randomRead = rand.Read

var objectIDState struct {
	once    sync.Once
	process [5]byte
	counter atomic.Uint32
	err     error
}

// GenerateID returns a UUID or MongoDB ObjectID string. Serial IDs are allocated
// with NextSerial because their counter is shared through DynamoDB.
func GenerateID(generator string) (string, error) {
	switch strings.ToLower(generator) {
	case "uuidv1":
		return generateUUIDv1()
	case "uuidv4":
		return generateUUIDv4()
	case "uuidv7":
		return generateUUIDv7()
	case "objectid":
		return generateObjectID()
	default:
		return "", fmt.Errorf("dynamox: unsupported string ID generator %q", generator)
	}
}

func generateUUIDv1() (string, error) {
	var random [8]byte
	if _, err := randomRead(random[:]); err != nil {
		return "", fmt.Errorf("dynamox: generate uuidv1: %w", err)
	}
	const gregorianOffset = uint64(12219292800)
	now := time.Now().UnixNano()
	stamp := uint64(now/100) + gregorianOffset*10000000
	var value [16]byte
	binary.BigEndian.PutUint32(value[0:4], uint32(stamp))
	binary.BigEndian.PutUint16(value[4:6], uint16(stamp>>32))
	binary.BigEndian.PutUint16(value[6:8], uint16(stamp>>48)&0x0fff|0x1000)
	sequence := uint16(random[6]&0x3f)<<8 | uint16(random[7])
	value[8] = byte(sequence>>8)&0x3f | 0x80
	value[9] = byte(sequence)
	copy(value[10:], random[:6])
	value[10] |= 0x01 // Mark the randomly generated node as non-Ethernet.
	return formatUUID(value), nil
}

func generateUUIDv4() (string, error) {
	var value [16]byte
	if _, err := randomRead(value[:]); err != nil {
		return "", fmt.Errorf("dynamox: generate uuidv4: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return formatUUID(value), nil
}

func generateUUIDv7() (string, error) {
	var value [16]byte
	if _, err := randomRead(value[6:]); err != nil {
		return "", fmt.Errorf("dynamox: generate uuidv7: %w", err)
	}
	ms := uint64(time.Now().UnixMilli())
	value[0] = byte(ms >> 40)
	value[1] = byte(ms >> 32)
	value[2] = byte(ms >> 24)
	value[3] = byte(ms >> 16)
	value[4] = byte(ms >> 8)
	value[5] = byte(ms)
	value[6] = value[6]&0x0f | 0x70
	value[8] = value[8]&0x3f | 0x80
	return formatUUID(value), nil
}

func generateObjectID() (string, error) {
	objectIDState.once.Do(func() {
		var seed [8]byte
		if _, err := randomRead(seed[:]); err != nil {
			objectIDState.err = err
			return
		}
		copy(objectIDState.process[:], seed[:5])
		start := uint32(seed[5])<<16 | uint32(seed[6])<<8 | uint32(seed[7])
		objectIDState.counter.Store(start)
	})
	if objectIDState.err != nil {
		return "", fmt.Errorf("dynamox: initialize objectid generator: %w", objectIDState.err)
	}
	var value [12]byte
	binary.BigEndian.PutUint32(value[:4], uint32(time.Now().Unix()))
	copy(value[4:9], objectIDState.process[:])
	counter := objectIDState.counter.Add(1) & 0x00ffffff
	value[9] = byte(counter >> 16)
	value[10] = byte(counter >> 8)
	value[11] = byte(counter)
	return hex.EncodeToString(value[:]), nil
}

func formatUUID(value [16]byte) string {
	var raw [32]byte
	hex.Encode(raw[:], value[:])
	return string(raw[0:8]) + "-" + string(raw[8:12]) + "-" + string(raw[12:16]) + "-" + string(raw[16:20]) + "-" + string(raw[20:32])
}

// NextSerial allocates a process-independent, monotonically increasing Int ID
// by atomically incrementing the named counter in the sequence table.
func NextSerial(ctx context.Context, db DynamoAPI, sequenceName string) (int64, error) {
	out, err := db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(SequenceTableName),
		Key:                       map[string]types.AttributeValue{"name": StringKey(sequenceName)},
		UpdateExpression:          aws.String("ADD #value :one"),
		ExpressionAttributeNames:  map[string]string{"#value": "value"},
		ExpressionAttributeValues: map[string]types.AttributeValue{":one": &types.AttributeValueMemberN{Value: "1"}},
		ReturnValues:              types.ReturnValueUpdatedNew,
	})
	if err != nil {
		return 0, fmt.Errorf("dynamox: allocate serial for %s: %w", sequenceName, err)
	}
	if out == nil {
		return 0, errors.New("dynamox: serial counter returned no attributes")
	}
	value, ok := out.Attributes["value"].(*types.AttributeValueMemberN)
	if !ok {
		return 0, errors.New("dynamox: serial counter returned a non-numeric value")
	}
	n, err := strconv.ParseInt(value.Value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("dynamox: parse serial counter: %w", err)
	}
	return n, nil
}
