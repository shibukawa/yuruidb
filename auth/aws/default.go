package aws

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Region resolves an explicit region before AWS_REGION, AWS_DEFAULT_REGION,
// and the compatibility default us-east-1.
func Region(explicit string) string {
	if explicit != "" {
		return explicit
	}
	for _, k := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return "us-east-1"
}

// ChainConfig configures environment, web identity, container, then IMDSv2
// credentials. Shared profiles and SSO can still be injected with Provider.
type ChainConfig struct {
	Client                        *http.Client
	STSEndpoint, MetadataEndpoint string
}
type expiringCredentials struct {
	AccessKey  string    `json:"AccessKeyId"`
	SecretKey  string    `json:"SecretAccessKey"`
	Token      string    `json:"Token"`
	Expiration time.Time `json:"Expiration"`
}

func (c expiringCredentials) credentials() (Credentials, error) {
	if c.AccessKey == "" || c.SecretKey == "" || c.Expiration.IsZero() || !c.Expiration.After(time.Now()) {
		return Credentials{}, fmt.Errorf("yuruidb: invalid or expired AWS role credentials")
	}
	return Credentials{c.AccessKey, c.SecretKey, c.Token}, nil
}
func Default(client *http.Client) Provider { return DefaultChain(ChainConfig{Client: client}) }
func DefaultChain(config ChainConfig) Provider {
	client := config.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	} else {
		copy := *client
		copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client = &copy
	}
	var mu sync.Mutex
	var cached Credentials
	var expiry time.Time
	var source string
	return func(ctx context.Context) (Credentials, error) {
		if err := ctx.Err(); err != nil {
			return Credentials{}, err
		}
		env, _ := Environment(ctx)
		if env.AccessKey != "" || env.SecretKey != "" {
			if env.AccessKey == "" || env.SecretKey == "" {
				return Credentials{}, fmt.Errorf("yuruidb: incomplete AWS environment credentials")
			}
			return env, nil
		}
		identity := strings.Join([]string{os.Getenv("AWS_ROLE_ARN"), os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE"), os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"), os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI"), os.Getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE"), os.Getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN"), os.Getenv("AWS_EC2_METADATA_DISABLED")}, "\x00")
		mu.Lock()
		defer mu.Unlock()
		if identity == source && time.Until(expiry) > time.Minute {
			return cached, nil
		}
		var value expiringCredentials
		var err error
		switch {
		case os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE") != "" || os.Getenv("AWS_ROLE_ARN") != "":
			value, err = webIdentity(ctx, client, config.STSEndpoint)
		case os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") != "" || os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI") != "":
			value, err = containerCredentials(ctx, client)
		default:
			if strings.EqualFold(os.Getenv("AWS_EC2_METADATA_DISABLED"), "true") {
				return Credentials{}, fmt.Errorf("yuruidb: no AWS credential source")
			}
			value, err = instanceCredentials(ctx, client, config.MetadataEndpoint)
		}
		if err != nil {
			return Credentials{}, err
		}
		credentials, err := value.credentials()
		if err != nil {
			return Credentials{}, err
		}
		cached = credentials
		expiry = value.Expiration
		source = identity
		return credentials, nil
	}
}
func credentialRequest(ctx context.Context, client *http.Client, method, target, body string, headers http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("yuruidb: invalid credential endpoint")
	}
	req.Header = headers
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("yuruidb: AWS credential request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("yuruidb: AWS credential endpoint HTTP %d", response.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || len(b) >= 1<<20 {
		return nil, fmt.Errorf("yuruidb: invalid credential response")
	}
	return b, nil
}
func webIdentity(ctx context.Context, client *http.Client, endpoint string) (expiringCredentials, error) {
	var out expiringCredentials
	role, path := os.Getenv("AWS_ROLE_ARN"), os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE")
	if role == "" || path == "" {
		return out, fmt.Errorf("yuruidb: AWS role and web identity token file required")
	}
	token, err := os.ReadFile(path)
	if err != nil {
		return out, fmt.Errorf("yuruidb: cannot read AWS web identity token")
	}
	if endpoint == "" {
		endpoint = "https://sts." + Region("") + ".amazonaws.com/"
	}
	session := os.Getenv("AWS_ROLE_SESSION_NAME")
	if session == "" {
		session = "yuruidb"
	}
	form := url.Values{"Action": {"AssumeRoleWithWebIdentity"}, "Version": {"2011-06-15"}, "RoleArn": {role}, "RoleSessionName": {session}, "WebIdentityToken": {strings.TrimSpace(string(token))}}
	b, err := credentialRequest(ctx, client, "POST", endpoint, form.Encode(), http.Header{"Content-Type": {"application/x-www-form-urlencoded"}})
	if err != nil {
		return out, err
	}
	// AWS uses explicit element names, not the public Credentials field names.
	var result struct {
		Result struct {
			Credentials struct {
				AccessKey  string    `xml:"AccessKeyId"`
				SecretKey  string    `xml:"SecretAccessKey"`
				Token      string    `xml:"SessionToken"`
				Expiration time.Time `xml:"Expiration"`
			} `xml:"Credentials"`
		} `xml:"AssumeRoleWithWebIdentityResult"`
	}
	if xml.Unmarshal(b, &result) != nil {
		return out, fmt.Errorf("yuruidb: malformed AWS web identity response")
	}
	v := result.Result.Credentials
	return expiringCredentials{v.AccessKey, v.SecretKey, v.Token, v.Expiration}, nil
}
func containerCredentials(ctx context.Context, client *http.Client) (expiringCredentials, error) {
	var out expiringCredentials
	target := os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI")
	if relative := os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"); relative != "" {
		if !strings.HasPrefix(relative, "/") || strings.HasPrefix(relative, "//") {
			return out, fmt.Errorf("yuruidb: invalid container credential path")
		}
		target = "http://169.254.170.2" + relative
	}
	u, err := url.Parse(target)
	if err != nil || u.User != nil || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return out, fmt.Errorf("yuruidb: invalid container credential endpoint")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if u.Scheme == "http" && host != "localhost" && (ip == nil || !ip.IsLoopback()) && host != "169.254.170.2" && host != "169.254.170.23" && host != "fd00:ec2::23" {
		return out, fmt.Errorf("yuruidb: insecure container credential endpoint")
	}
	token := os.Getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN")
	if path := os.Getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return out, fmt.Errorf("yuruidb: cannot read container authorization token")
		}
		token = strings.TrimSpace(string(b))
	}
	h := make(http.Header)
	if token != "" {
		h.Set("Authorization", token)
	}
	b, err := credentialRequest(ctx, client, "GET", target, "", h)
	if err != nil {
		return out, err
	}
	if json.Unmarshal(b, &out) != nil {
		return out, fmt.Errorf("yuruidb: malformed container credentials")
	}
	return out, nil
}
func instanceCredentials(ctx context.Context, client *http.Client, endpoint string) (expiringCredentials, error) {
	var out expiringCredentials
	if endpoint == "" {
		endpoint = "http://169.254.169.254"
	}
	endpoint = strings.TrimRight(endpoint, "/")
	b, err := credentialRequest(ctx, client, "PUT", endpoint+"/latest/api/token", "", http.Header{"X-Aws-Ec2-Metadata-Token-Ttl-Seconds": {"21600"}})
	if err != nil {
		return out, err
	}
	headers := http.Header{"X-Aws-Ec2-Metadata-Token": {string(b)}}
	b, err = credentialRequest(ctx, client, "GET", endpoint+"/latest/meta-data/iam/security-credentials/", "", headers)
	if err != nil {
		return out, err
	}
	role := strings.TrimSpace(string(b))
	if role == "" || strings.ContainsAny(role, "/\\\r\n ?#") {
		return out, fmt.Errorf("yuruidb: invalid EC2 role name")
	}
	b, err = credentialRequest(ctx, client, "GET", endpoint+"/latest/meta-data/iam/security-credentials/"+url.PathEscape(role), "", headers)
	if err != nil {
		return out, err
	}
	if json.Unmarshal(b, &out) != nil {
		return out, fmt.Errorf("yuruidb: malformed EC2 credentials")
	}
	return out, nil
}
