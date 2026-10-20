package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func clearRoleEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_AUTHORIZATION_TOKEN", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE", "AWS_EC2_METADATA_DISABLED", "AWS_REGION", "AWS_DEFAULT_REGION"} {
		t.Setenv(key, "")
	}
}
func TestDefaultContainerCredentialsRefresh(t *testing.T) {
	clearRoleEnvironment(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		number := calls.Add(1)
		if r.Header.Get("Authorization") != "pod-token" {
			t.Error("missing pod identity token")
		}
		ttl := 10 * time.Minute
		if number == 1 {
			ttl = 30 * time.Second
		}
		json.NewEncoder(w).Encode(map[string]any{"AccessKeyId": "role-key", "SecretAccessKey": "role-secret", "Token": "role-session", "Expiration": time.Now().Add(ttl)})
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("pod-token"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", server.URL)
	t.Setenv("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE", path)
	provider := Default(server.Client())
	for i := 0; i < 3; i++ {
		got, err := provider(context.Background())
		if err != nil || got.AccessKey != "role-key" || got.SessionToken != "role-session" {
			t.Fatalf("container credentials: %+v %v", got, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("credential cache calls=%d", calls.Load())
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "environment-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "environment-secret")
	got, err := provider(context.Background())
	if err != nil || got.AccessKey != "environment-key" {
		t.Fatalf("environment priority: %v", err)
	}
}
func TestDefaultWebIdentityAndIMDSv2(t *testing.T) {
	t.Run("web identity", func(t *testing.T) {
		clearRoleEnvironment(t)
		path := filepath.Join(t.TempDir(), "identity")
		os.WriteFile(path, []byte("identity-token"), 0600)
		t.Setenv("AWS_ROLE_ARN", "arn:aws:iam::123456789012:role/pod")
		t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", path)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.ParseForm()
			if r.Form.Get("Action") != "AssumeRoleWithWebIdentity" || r.Form.Get("WebIdentityToken") != "identity-token" {
				t.Error("invalid web identity request")
			}
			fmt.Fprintf(w, "<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>web-key</AccessKeyId><SecretAccessKey>web-secret</SecretAccessKey><SessionToken>web-session</SessionToken><Expiration>%s</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>", time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		}))
		defer server.Close()
		got, err := DefaultChain(ChainConfig{Client: server.Client(), STSEndpoint: server.URL})(context.Background())
		if err != nil || got.AccessKey != "web-key" || got.SessionToken != "web-session" {
			t.Fatalf("web identity: %v", err)
		}
	})
	t.Run("IMDSv2", func(t *testing.T) {
		clearRoleEnvironment(t)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/latest/api/token":
				if r.Method != "PUT" || r.Header.Get("X-Aws-Ec2-Metadata-Token-Ttl-Seconds") == "" {
					t.Error("IMDSv2 token request")
				}
				w.Write([]byte("imds-token"))
			case "/latest/meta-data/iam/security-credentials/":
				if r.Header.Get("X-Aws-Ec2-Metadata-Token") != "imds-token" {
					t.Error("IMDSv2 auth missing")
				}
				w.Write([]byte("instance-role"))
			default:
				if r.Header.Get("X-Aws-Ec2-Metadata-Token") != "imds-token" {
					t.Error("IMDSv2 auth missing")
				}
				json.NewEncoder(w).Encode(map[string]any{"AccessKeyId": "ec2-key", "SecretAccessKey": "ec2-secret", "Token": "ec2-session", "Expiration": time.Now().Add(time.Hour)})
			}
		}))
		defer server.Close()
		got, err := DefaultChain(ChainConfig{Client: server.Client(), MetadataEndpoint: server.URL})(context.Background())
		if err != nil || got.AccessKey != "ec2-key" {
			t.Fatalf("IMDSv2: %v", err)
		}
	})
}
func TestRegionAndCredentialErrors(t *testing.T) {
	clearRoleEnvironment(t)
	t.Setenv("AWS_DEFAULT_REGION", "us-west-2")
	t.Setenv("AWS_REGION", "ap-northeast-1")
	if Region("") != "ap-northeast-1" || Region("eu-west-1") != "eu-west-1" {
		t.Fatal("region precedence")
	}
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "http://example.com/SECRET")
	_, err := Default(nil)(context.Background())
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("credential endpoint rejected unsafely: %v", err)
	}
}
