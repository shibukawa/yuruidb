// Package dynamodb registers DynamoDB via the signed JSON HTTP API.
// The table must have string keys pk (partition) and sk (sort key).
package dynamodb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
	awsauth "github.com/shibukawa/yuruidb/auth/aws"
	"github.com/shibukawa/yuruidb/internal/httpx"
	"github.com/shibukawa/yuruidb/internal/recordstore"
	"net/http"
	"strings"
)

type Config struct {
	Endpoint, Region, Table, Collection string
	Client                              *http.Client
	Credentials                         awsauth.Provider
	Authorize                           auth.Authorizer
}
type attribute struct {
	S string `json:"S,omitempty"`
	B string `json:"B,omitempty"`
}
type transport struct {
	client                      httpx.Client
	endpoint, table, collection string
}

func init() {
	yuruidb.Register("dynamodb", func(ctx context.Context, v any) (yuruidb.RawStore, error) {
		c, err := yuruidb.DecodeConfig[Config](v)
		if err != nil {
			return nil, err
		}
		return OpenRaw(ctx, c)
	})
}
func OpenRaw(ctx context.Context, c Config) (yuruidb.RawStore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.Table == "" {
		return nil, fmt.Errorf("%w: DynamoDB table required", yuruidb.ErrInvalid)
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	if c.Endpoint == "" {
		c.Endpoint = "https://dynamodb." + c.Region + ".amazonaws.com"
	}
	endpoint, err := httpx.Endpoint(c.Endpoint)
	if err != nil {
		return nil, err
	}
	authorize := c.Authorize
	if authorize == nil {
		authorize = awsauth.Signer(c.Region, "dynamodb", c.Credentials)
	}
	t := &transport{httpx.Client{HTTP: c.Client, Auth: authorize}, endpoint, c.Table, c.Collection}
	return recordstore.New(t, c.Collection, "dynamodb:"+endpoint+"/"+c.Table, yuruidb.Capabilities{ConditionalMutations: true, Distributed: true, StrongReads: true})
}
func (t *transport) call(ctx context.Context, op string, input, output any, mutation bool) error {
	b, err := json.Marshal(input)
	if err != nil {
		return err
	}
	h := http.Header{"Content-Type": {"application/x-amz-json-1.0"}, "X-Amz-Target": {"DynamoDB_20120810." + op}}
	r, err := t.client.Do(ctx, "POST", t.endpoint+"/", b, h, mutation)
	if err != nil {
		var remote struct {
			Type string `json:"__type"`
		}
		if json.Unmarshal(r.Body, &remote) == nil && strings.HasSuffix(remote.Type, "ConditionalCheckFailedException") {
			return yuruidb.ErrConflict
		}
		return err
	}
	if output != nil && json.Unmarshal(r.Body, output) != nil {
		if mutation {
			return errors.Join(yuruidb.ErrOutcomeUnknown, fmt.Errorf("yuruidb: malformed DynamoDB mutation response"))
		}
		return fmt.Errorf("yuruidb: malformed DynamoDB response")
	}
	return nil
}
func (t *transport) key(k string) map[string]attribute {
	return map[string]attribute{"pk": {S: t.collection}, "sk": {S: k}}
}
func (t *transport) Read(ctx context.Context, key string) (recordstore.Item, error) {
	var out struct {
		Item map[string]attribute `json:"Item"`
	}
	err := t.call(ctx, "GetItem", map[string]any{"TableName": t.table, "Key": t.key(key), "ConsistentRead": true}, &out, false)
	if err != nil {
		return recordstore.Item{}, err
	}
	if len(out.Item) == 0 {
		return recordstore.Item{}, yuruidb.ErrNotFound
	}
	data, err := base64.StdEncoding.DecodeString(out.Item["payload"].B)
	if err != nil {
		return recordstore.Item{}, fmt.Errorf("yuruidb: corrupt DynamoDB binary")
	}
	return recordstore.Item{Data: data, Token: out.Item["token"].S}, nil
}
func (t *transport) Write(ctx context.Context, key string, data []byte, expected string) error {
	token, err := yuruidb.NewRevision()
	if err != nil {
		return err
	}
	item := t.key(key)
	item["payload"] = attribute{B: base64.StdEncoding.EncodeToString(data)}
	item["token"] = attribute{S: token}
	input := map[string]any{"TableName": t.table, "Item": item, "ConditionExpression": "attribute_not_exists(#pk)", "ExpressionAttributeNames": map[string]string{"#pk": "pk"}}
	if expected != "" {
		input["ConditionExpression"] = "#token = :expected"
		input["ExpressionAttributeNames"] = map[string]string{"#token": "token"}
		input["ExpressionAttributeValues"] = map[string]attribute{":expected": {S: expected}}
	}
	return t.call(ctx, "PutItem", input, nil, true)
}
func (t *transport) Keys(ctx context.Context, after string, n int) ([]string, bool, error) {
	if after == "" {
		after = t.collection + "/"
	}
	input := map[string]any{"TableName": t.table, "KeyConditionExpression": "#pk = :collection AND #sk > :after", "ExpressionAttributeNames": map[string]string{"#pk": "pk", "#sk": "sk"}, "ExpressionAttributeValues": map[string]attribute{":collection": {S: t.collection}, ":after": {S: after}}, "ConsistentRead": true, "ScanIndexForward": true, "Limit": n, "ProjectionExpression": "#sk"}
	var out struct {
		Items []map[string]attribute `json:"Items"`
		Last  map[string]attribute   `json:"LastEvaluatedKey"`
	}
	if err := t.call(ctx, "Query", input, &out, false); err != nil {
		return nil, false, err
	}
	keys := make([]string, len(out.Items))
	for i, item := range out.Items {
		keys[i] = item["sk"].S
	}
	return keys, len(out.Last) > 0, nil
}
func (t *transport) Close() error { return nil }

// Open opens a JSON-typed collection using this driver.
func Open[T any](ctx context.Context, c Config) (*yuruidb.Store[T], error) {
	raw, err := OpenRaw(ctx, c)
	if err != nil {
		return nil, err
	}
	return yuruidb.Wrap[T](raw), nil
}
