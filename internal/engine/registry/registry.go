// Package registry reads only Registry V2/OCI image metadata over verified HTTPS.
package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
)

var imageDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// Client keeps credentials' token caches and verified manifest mappings separate.
// Its clock may be set before use by tests; all mutable caches are synchronized.
type Client struct {
	http      *http.Client
	Now       func() time.Time
	mu        sync.Mutex
	tokens    map[string]authorization
	roots     map[rootKey]*rootState
	cooldowns map[cooldownKey]engine.RegistryCooldown
}

func NewClient(httpClient *http.Client) *Client {
	if nil == httpClient {
		httpClient = &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone(), Timeout: 30 * time.Second}
	}
	return &Client{http: httpClient, Now: time.Now, tokens: make(map[string]authorization), roots: make(map[rootKey]*rootState), cooldowns: make(map[cooldownKey]engine.RegistryCooldown)}
}

func (c *Client) InspectConfig(ctx context.Context, key engine.ImageKey, data []byte, ages ...time.Duration) engine.RegistryImage {
	result := engine.RegistryImage{Status: "error", Reason: "registry_error"}
	maxAge := time.Duration(0)
	if 0 < len(ages) {
		maxAge = ages[0]
	}
	sanitize := registryRedactor(data).Replace
	ref, err := parseReference(key.Ref)
	var login credentials
	if nil == err {
		login, err = resolveCredentials(data, ref.registry)
	}
	if nil == err {
		lookup := &lookup{client: c, ref: ref, key: key, login: login, redact: registryRedactor(data)}
		result, err = lookup.inspect(ctx, maxAge)
		if nil == err {
			return result
		}
		var cooldown *cooldownError
		if errors.As(err, &cooldown) {
			checkedAt := result.CheckedAt
			if checkedAt.IsZero() {
				checkedAt = c.Now()
			}
			return engine.RegistryImage{Status: "error", Reason: "registry_error", Cooldown: &cooldown.state, CheckedAt: checkedAt}
		}
		sanitize = lookup.safe
	}
	if result.CheckedAt.IsZero() {
		result.CheckedAt = c.Now()
	}
	level := slog.LevelWarn
	if nil != ctx.Err() || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		level = slog.LevelDebug
	}
	detail := sanitize(err.Error())
	slog.Log(ctx, level, fmt.Sprintf("Could not inspect registry image %q for updates: %s", key.Ref, detail), "image", key.Ref, "platform", key.ImagePlatform, "error", detail)
	return result
}

type reference struct{ registry, host, repository, tag string }

func parseReference(ref string) (reference, error) {
	ref = strings.TrimPrefix(ref, "docker://")
	if "" == ref || strings.ContainsAny(ref, "?#\\ \t\n\r") || strings.Contains(ref, "://") || strings.HasPrefix(ref, "-") {
		return reference{}, errors.New("invalid registry image reference")
	}
	first, rest, slash := strings.Cut(ref, "/")
	host, name := "docker.io", ref
	if slash && (strings.ContainsAny(first, ".:") || "localhost" == first) {
		host, name = first, rest
	}
	host = canonicalHost(host)
	if "docker.io" == host && !strings.Contains(name, "/") {
		name = "library/" + name
	}
	tag := "latest"
	if repository, digest, ok := strings.Cut(name, "@"); ok {
		if !imageDigest.MatchString(digest) {
			return reference{}, errors.New("unsupported manifest reference digest")
		}
		name, tag = repository, digest
	} else if i := strings.LastIndex(name, ":"); -1 != i {
		name, tag = name[:i], name[i+1:]
	}
	if "" == name || "" == tag {
		return reference{}, errors.New("image repository or tag is empty")
	}
	for _, part := range strings.Split(name, "/") {
		if "" == part || "." == part || ".." == part {
			return reference{}, errors.New("invalid image repository")
		}
	}
	endpoint := host
	if "docker.io" == host {
		endpoint = "registry-1.docker.io"
	}
	return reference{registry: host, host: endpoint, repository: name, tag: tag}, nil
}

func canonicalHost(host string) string {
	host = strings.ToLower(host)
	if "index.docker.io" == host || "registry-1.docker.io" == host {
		return "docker.io"
	}
	return host
}

type lookup struct {
	client  *Client
	ref     reference
	key     engine.ImageKey
	login   credentials
	redact  *strings.Replacer
	secrets []string
}

func (l *lookup) safe(message string) string {
	message = l.redact.Replace(message)
	for _, secret := range l.secrets {
		if "" != secret {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	return message
}

func (l *lookup) endpoint(kind, name string) string {
	return "https://" + l.ref.host + "/v2/" + l.ref.repository + "/" + kind + "/" + url.PathEscape(name)
}

func (l *lookup) do(ctx context.Context, method, endpoint, authorization string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if nil != err {
		return nil, errors.New("invalid registry request URL")
	}
	req.Header.Set("Accept", manifestAccept)
	if "" != authorization {
		req.Header.Set("Authorization", authorization)
	}
	return l.send(req)
}

func (l *lookup) send(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	if err := ctx.Err(); nil != err {
		return nil, err
	}
	if state := l.client.activeCooldown(l.ref.registry, l.login.account()); nil != state {
		return nil, &cooldownError{state: *state}
	}
	resp, err := l.client.http.Do(req)
	if nil != err {
		if nil != resp {
			_ = resp.Body.Close()
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("%s registry metadata: %w", req.Method, err)
	}
	if http.StatusTooManyRequests == resp.StatusCode {
		return nil, l.rateLimited(ctx, resp)
	}
	return resp, nil
}

type sanitizedError struct {
	error
	message string
}

func (e *sanitizedError) Error() string { return e.message }
func (e *sanitizedError) Unwrap() error { return e.error }

func (l *lookup) sharedError(err error) error {
	if nil == err {
		return nil
	}
	return &sanitizedError{error: err, message: l.safe(err.Error())}
}

func responseError(resp *http.Response) error {
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if nil != err {
		return fmt.Errorf("HTTP %d %s (could not read registry error: %v)", resp.StatusCode, http.StatusText(resp.StatusCode), err)
	}
	var document struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Description string `json:"error_description"`
		Error       string `json:"error"`
	}
	var details []string
	if nil == json.Unmarshal(data, &document) {
		for _, item := range document.Errors {
			if "" != item.Message {
				details = append(details, item.Message)
			}
		}
		if "" != document.Description {
			details = append(details, document.Description)
		} else if "" != document.Error {
			details = append(details, document.Error)
		}
	} else if !strings.Contains(resp.Header.Get("Content-Type"), "json") && !strings.HasPrefix(strings.TrimSpace(string(data)), "{") && !strings.HasPrefix(strings.TrimSpace(string(data)), "[") {
		details = append(details, strings.TrimSpace(string(data)))
	}
	detail := strings.Join(details, "; ")
	if "" == detail {
		detail = "registry rejected the metadata request"
	}
	return fmt.Errorf("HTTP %d %s: %s", resp.StatusCode, http.StatusText(resp.StatusCode), detail)
}

func registryRedactor(data []byte) *strings.Replacer {
	var config struct {
		Auths map[string]credentials `json:"auths"`
	}
	_ = json.Unmarshal(data, &config)
	var replacements []string
	for _, auth := range config.Auths {
		secrets := []string{auth.Auth, auth.Password, auth.IdentityToken, auth.RegistryToken}
		if "" != auth.Password {
			secrets = append(secrets, auth.Username+":"+auth.Password, base64.StdEncoding.EncodeToString([]byte(auth.Username+":"+auth.Password)))
		}
		if decoded, err := base64.StdEncoding.DecodeString(auth.Auth); nil == err {
			secrets = append(secrets, string(decoded))
			if _, password, ok := strings.Cut(string(decoded), ":"); ok {
				secrets = append(secrets, password)
			}
		}
		for _, secret := range secrets {
			if "" != secret {
				replacements = append(replacements, secret, "[REDACTED]", url.QueryEscape(secret), "[REDACTED]")
			}
		}
	}
	return strings.NewReplacer(replacements...)
}
