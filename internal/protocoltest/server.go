// Package protocoltest implements independent, atomic HTTP protocol fixtures
// for the supported providers. It does not establish live service compatibility.
package protocoltest

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type item struct {
	data  []byte
	token string
}
type Server struct {
	*httptest.Server
	mu    sync.Mutex
	items map[string]item
	seq   int
	kind  string
	t     *testing.T
}

func New(t *testing.T, kind string) *Server {
	t.Helper()
	s := &Server{items: map[string]item{}, kind: kind, t: t}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}
func (s *Server) put(key string, data []byte, expected string) bool {
	old, exists := s.items[key]
	if exists && old.token != expected || !exists && expected != "" {
		return false
	}
	s.seq++
	s.items[key] = item{append([]byte(nil), data...), fmt.Sprint(s.seq)}
	return true
}
func (s *Server) keys(prefix, after string, n int, inclusive bool) ([]string, bool) {
	keys := []string{}
	for k := range s.items {
		if strings.HasPrefix(k, prefix) && (k > after || inclusive && k == after) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	more := len(keys) > n
	if more {
		keys = keys[:n]
	}
	return keys, more
}
func number(v string, def int) int {
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.kind {
	case "s3", "r2":
		s.s3(w, r)
	case "gcs":
		s.gcs(w, r)
	case "dynamodb":
		s.dynamo(w, r)
	case "firestore":
		s.firestore(w, r)
	case "firebase":
		s.firebase(w, r)
	case "cfkv":
		s.kv(w, r)
	default:
		http.Error(w, "unsupported fixture", 500)
	}
}
func (s *Server) s3(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
		s.t.Error("missing SigV4")
		http.Error(w, "missing signature", 403)
		return
	}
	if r.URL.Query().Get("list-type") == "2" {
		keys, more := s.keys(r.URL.Query().Get("prefix"), r.URL.Query().Get("start-after"), number(r.URL.Query().Get("max-keys"), 1000), false)
		var out struct {
			XMLName   xml.Name `xml:"ListBucketResult"`
			Truncated bool     `xml:"IsTruncated"`
			Contents  []struct {
				Key string `xml:"Key"`
			} `xml:"Contents"`
		}
		out.Truncated = more
		for _, key := range keys {
			out.Contents = append(out.Contents, struct {
				Key string `xml:"Key"`
			}{key})
		}
		xml.NewEncoder(w).Encode(out)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/bucket/")
	switch r.Method {
	case "GET":
		v, ok := s.items[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `"`+v.token+`"`)
		w.Write(v.data)
	case "PUT":
		expected := strings.Trim(r.Header.Get("If-Match"), `"`)
		if expected == "" && r.Header.Get("If-None-Match") != "*" {
			s.t.Error("missing conditional write")
			http.Error(w, "conditional required", 400)
			return
		}
		data, _ := io.ReadAll(r.Body)
		if !s.put(key, data, expected) {
			w.WriteHeader(412)
			return
		}
		w.Header().Set("ETag", `"`+s.items[key].token+`"`)
		w.WriteHeader(200)
	default:
		http.Error(w, "method", 405)
	}
}
func (s *Server) gcs(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer test-token" {
		s.t.Error("GCS bearer missing")
	}
	q := r.URL.Query()
	switch {
	case r.Method == "POST":
		key := q.Get("name")
		expected := q.Get("ifGenerationMatch")
		if expected == "" {
			s.t.Error("missing GCS precondition")
			http.Error(w, "precondition", 400)
			return
		}
		if expected == "0" {
			expected = ""
		}
		b, _ := io.ReadAll(r.Body)
		if !s.put(key, b, expected) {
			w.WriteHeader(412)
			return
		}
		reply(w, map[string]any{"name": key, "generation": s.items[key].token})
	case strings.Contains(r.URL.Path, "/o/"):
		key := strings.SplitN(r.URL.Path, "/o/", 2)[1]
		v, ok := s.items[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Goog-Generation", v.token)
		w.Write(v.data)
	default:
		keys, more := s.keys(q.Get("prefix"), q.Get("startOffset"), number(q.Get("maxResults"), 1000), true)
		items := []map[string]string{}
		for _, k := range keys {
			items = append(items, map[string]string{"name": k})
		}
		out := map[string]any{"items": items}
		if more {
			out["nextPageToken"] = "after:" + keys[len(keys)-1]
		}
		if token := q.Get("pageToken"); token != "" {
			keys, more = s.keys(q.Get("prefix"), strings.TrimPrefix(token, "after:"), number(q.Get("maxResults"), 1000), false)
			items = []map[string]string{}
			for _, k := range keys {
				items = append(items, map[string]string{"name": k})
			}
			out = map[string]any{"items": items}
			if more {
				out["nextPageToken"] = "after:" + keys[len(keys)-1]
			}
		}
		reply(w, out)
	}
}

type attr struct {
	S string `json:"S,omitempty"`
	B string `json:"B,omitempty"`
}

func (s *Server) dynamo(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
		s.t.Error("DynamoDB signature missing")
	}
	var in struct {
		Table      string          `json:"TableName"`
		Key        map[string]attr `json:"Key"`
		Item       map[string]attr `json:"Item"`
		Values     map[string]attr `json:"ExpressionAttributeValues"`
		Condition  string          `json:"ConditionExpression"`
		Limit      int             `json:"Limit"`
		Consistent bool            `json:"ConsistentRead"`
		Forward    bool            `json:"ScanIndexForward"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		http.Error(w, "json", 400)
		return
	}
	op := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "DynamoDB_20120810.")
	switch op {
	case "GetItem":
		if !in.Consistent {
			s.t.Error("weak DynamoDB read")
		}
		key := in.Key["pk"].S + "|" + in.Key["sk"].S
		v, ok := s.items[key]
		if !ok {
			reply(w, map[string]any{})
			return
		}
		reply(w, map[string]any{"Item": map[string]attr{"payload": {B: base64.StdEncoding.EncodeToString(v.data)}, "token": {S: v.token}}})
	case "PutItem":
		key := in.Item["pk"].S + "|" + in.Item["sk"].S
		old, exists := s.items[key]
		valid := false
		if in.Condition == "attribute_not_exists(#pk)" {
			valid = !exists
		} else if in.Condition == "#token = :expected" {
			valid = exists && old.token == in.Values[":expected"].S
		} else {
			s.t.Error("DynamoDB missing condition")
		}
		if !valid {
			w.WriteHeader(400)
			reply(w, map[string]any{"__type": "com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException"})
			return
		}
		b, err := base64.StdEncoding.DecodeString(in.Item["payload"].B)
		if err != nil {
			s.t.Error(err)
		}
		s.items[key] = item{b, in.Item["token"].S}
		reply(w, map[string]any{})
	case "Query":
		if !in.Consistent || !in.Forward {
			s.t.Error("DynamoDB query guarantees")
		}
		prefix := in.Values[":collection"].S + "|"
		after := in.Values[":after"].S
		if after == "" {
			s.t.Error("empty DynamoDB sort-key condition")
		}
		keys, more := s.keys(prefix, prefix+after, in.Limit, false)
		items := []map[string]attr{}
		for _, k := range keys {
			items = append(items, map[string]attr{"sk": {S: strings.TrimPrefix(k, prefix)}})
		}
		out := map[string]any{"Items": items}
		if more {
			out["LastEvaluatedKey"] = map[string]attr{"pk": {S: in.Values[":collection"].S}, "sk": {S: strings.TrimPrefix(keys[len(keys)-1], prefix)}}
		}
		reply(w, out)
	default:
		http.Error(w, "unknown DynamoDB operation", 400)
	}
}
func (s *Server) firestore(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer test-token" {
		s.t.Error("Firestore bearer missing")
	}
	parent := "projects/project/databases/(default)/documents"
	path := strings.TrimPrefix(r.URL.Path, "/v1/")
	key := strings.TrimPrefix(path, parent+"/")
	if strings.HasSuffix(path, ":runQuery") {
		var in struct {
			Query struct {
				From []struct {
					Collection string `json:"collectionId"`
				} `json:"from"`
				Start struct {
					Values []struct {
						Ref string `json:"referenceValue"`
					} `json:"values"`
					Before bool `json:"before"`
				} `json:"startAt"`
				Limit int `json:"limit"`
			} `json:"structuredQuery"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		after := ""
		if len(in.Query.Start.Values) > 0 {
			if in.Query.Start.Before {
				s.t.Error("inclusive Firestore cursor")
			}
			after = strings.TrimPrefix(in.Query.Start.Values[0].Ref, parent+"/")
		}
		keys, _ := s.keys(in.Query.From[0].Collection+"/", after, in.Query.Limit, false)
		out := []map[string]any{}
		for _, k := range keys {
			out = append(out, map[string]any{"document": map[string]any{"name": parent + "/" + k}})
		}
		reply(w, out)
		return
	}
	switch r.Method {
	case "GET":
		v, ok := s.items[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		reply(w, map[string]any{"name": parent + "/" + key, "fields": map[string]any{"payload": map[string]any{"bytesValue": base64.StdEncoding.EncodeToString(v.data)}}, "updateTime": v.token})
	case "PATCH":
		var in struct {
			Fields map[string]struct {
				Bytes string `json:"bytesValue"`
			} `json:"fields"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		q := r.URL.Query()
		expected := q.Get("currentDocument.updateTime")
		if expected == "" && q.Get("currentDocument.exists") != "false" {
			s.t.Error("missing Firestore condition")
		}
		v, exists := s.items[key]
		if exists && v.token != expected || !exists && expected != "" {
			w.WriteHeader(400)
			reply(w, map[string]any{"error": map[string]any{"status": "FAILED_PRECONDITION"}})
			return
		}
		b, _ := base64.StdEncoding.DecodeString(in.Fields["payload"].Bytes)
		s.seq++
		s.items[key] = item{b, fmt.Sprintf("2026-10-20T00:00:00.%09dZ", s.seq)}
		reply(w, map[string]any{"updateTime": s.items[key].token})
	default:
		http.Error(w, "method", 405)
	}
}
func (s *Server) firebase(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer test-token" {
		s.t.Error("Firebase bearer missing")
	}
	key := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".json")
	if !strings.Contains(key, "/") {
		q := r.URL.Query()
		if q.Get("orderBy") != `"$key"` {
			s.t.Error("Firebase key ordering missing")
		}
		after := ""
		if start := q.Get("startAt"); start != "" {
			json.Unmarshal([]byte(start), &after)
		}
		keys, _ := s.keys(key+"/", key+"/"+after, number(q.Get("limitToFirst"), 1000), true)
		out := map[string]json.RawMessage{}
		for _, k := range keys {
			out[strings.TrimPrefix(k, key+"/")] = s.items[k].data
		}
		reply(w, out)
		return
	}
	switch r.Method {
	case "GET":
		v, ok := s.items[key]
		if !ok {
			w.Header().Set("ETag", "null_etag")
			w.Write([]byte("null"))
			return
		}
		if r.Header.Get("X-Firebase-ETag") != "true" {
			s.t.Error("Firebase ETag request missing")
		}
		w.Header().Set("ETag", `"`+v.token+`"`)
		w.Write(v.data)
	case "PUT":
		expected := strings.Trim(r.Header.Get("If-Match"), `"`)
		if expected == "null_etag" {
			expected = ""
		} else if expected == "" {
			s.t.Error("Firebase condition missing")
		}
		b, _ := io.ReadAll(r.Body)
		if !s.put(key, b, expected) {
			w.WriteHeader(412)
			return
		}
		w.Write(b)
	default:
		http.Error(w, "method", 405)
	}
}
func (s *Server) kv(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer test-token" {
		s.t.Error("KV bearer missing")
	}
	if strings.HasSuffix(r.URL.Path, "/keys") {
		q := r.URL.Query()
		keys, more := s.keys(q.Get("prefix"), q.Get("cursor"), number(q.Get("limit"), 1000), false)
		result := []map[string]string{}
		for _, k := range keys {
			result = append(result, map[string]string{"name": k})
		}
		cursor := ""
		if more {
			cursor = keys[len(keys)-1]
		}
		reply(w, map[string]any{"success": true, "result": result, "result_info": map[string]any{"cursor": cursor}})
		return
	}
	parts := strings.SplitN(r.URL.Path, "/values/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	key, err := url.PathUnescape(parts[1])
	if err != nil {
		http.Error(w, "key", 400)
		return
	}
	switch r.Method {
	case "GET":
		v, ok := s.items[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(v.data)
	case "PUT":
		b, _ := io.ReadAll(r.Body)
		s.seq++
		s.items[key] = item{b, fmt.Sprint(s.seq)}
		reply(w, map[string]any{"success": true})
	case "DELETE":
		delete(s.items, key)
		reply(w, map[string]any{"success": true})
	default:
		http.Error(w, "method", 405)
	}
}
