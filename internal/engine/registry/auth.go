package registry

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type credentials struct {
	Username      string `json:"username"`
	Password      string `json:"password"`
	Auth          string `json:"auth"`
	IdentityToken string `json:"identitytoken"`
	RegistryToken string `json:"registrytoken"`
}

func fingerprint(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (c credentials) fingerprint() string {
	data, _ := json.Marshal(c)
	return fingerprint(string(data))
}

func (c credentials) account() string {
	if "" != c.Username {
		return fingerprint("username:" + c.Username)
	}
	if "" != c.IdentityToken {
		return fingerprint("identitytoken:" + c.IdentityToken)
	}
	if "" != c.RegistryToken {
		return fingerprint("registrytoken:" + c.RegistryToken)
	}
	if "" != c.Password || "" != c.Auth {
		return fingerprint("credential:" + c.fingerprint())
	}
	return ""
}

func resolveCredentials(data []byte, registry string) (credentials, error) {
	var config *struct {
		Auths   map[string]credentials `json:"auths"`
		Helpers map[string]string      `json:"credHelpers"`
		Store   string                 `json:"credsStore"`
	}
	if err := json.Unmarshal(data, &config); nil != err {
		return credentials{}, fmt.Errorf("parse registry credentials: %w", err)
	}
	if nil == config {
		return credentials{}, errors.New("registry credentials must be a JSON object")
	}
	// Prefer exact/native root keys, then Docker's legacy URL-to-host fallback.
	// Imported records remain independent and retain their native field values.
	keys := make([]string, 0, len(config.Auths))
	for key := range config.Auths {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	sort.SliceStable(keys, func(i, j int) bool { return rootAuthKey(keys[i]) && !rootAuthKey(keys[j]) })
	preferred := registry
	if "docker.io" == registry {
		preferred = "https://index.docker.io/v1/"
	}
	if _, ok := config.Auths[preferred]; ok {
		keys = append([]string{preferred}, keys...)
	}
	for _, key := range keys {
		if authRegistry(key) != registry {
			continue
		}
		login := config.Auths[key]
		if "" != login.Auth {
			decoded, err := base64.StdEncoding.DecodeString(login.Auth)
			if nil != err {
				return credentials{}, errors.New("registry login has invalid base64 auth")
			}
			username, password, ok := strings.Cut(string(decoded), ":")
			if !ok {
				return credentials{}, errors.New("registry login auth is missing username/password separator")
			}
			if "" == login.Username {
				login.Username = username
			}
			if "" == login.Password {
				login.Password = strings.TrimRight(password, "\x00")
			}
		}
		return login, nil
	}
	for key, helper := range config.Helpers {
		if registry == authRegistry(key) && "" != helper {
			return credentials{}, errors.New(
				"registry login uses a credential helper; configure an inline registry login for metadata checks",
			)
		}
	}
	if "" != config.Store {
		return credentials{}, errors.New("registry login uses a credential store; configure an inline registry login for metadata checks")
	}
	return credentials{}, nil
}

func authRegistry(key string) string {
	key = strings.TrimSuffix(key, "/")
	if strings.Contains(key, "://") {
		parsed, err := url.Parse(key)
		if nil != err || nil != parsed.User || "" != parsed.RawQuery || "" != parsed.Fragment {
			return ""
		}
		return canonicalHost(parsed.Host)
	}
	host, _, _ := strings.Cut(key, "/")
	return canonicalHost(host)
}

func rootAuthKey(key string) bool {
	key = strings.TrimSuffix(key, "/")
	if strings.Contains(key, "://") {
		parsed, err := url.Parse(key)
		return nil == err && ("" == parsed.Path || ("/v1" == parsed.Path && "docker.io" == canonicalHost(parsed.Host)))
	}
	return !strings.Contains(key, "/")
}

type authorization struct {
	value   string
	expires time.Time
}

func (l *lookup) authKey() string {
	return l.ref.registry + "/" + l.ref.repository + ":" + l.login.fingerprint()
}

func (l *lookup) request(ctx context.Context, method, endpoint string) (*http.Response, error) {
	c := l.client
	key := l.authKey()
	c.mu.Lock()
	auth := c.tokens[key]
	c.mu.Unlock()
	if !auth.expires.After(c.Now()) {
		auth = authorization{}
	}
	if "" != l.login.RegistryToken {
		auth.value = "Bearer " + l.login.RegistryToken
	}
	if _, secret, ok := strings.Cut(auth.value, " "); ok {
		l.secrets = append(l.secrets, secret)
	}
	resp, err := l.do(ctx, method, endpoint, auth.value)
	if nil != err || http.StatusUnauthorized != resp.StatusCode {
		return resp, err
	}
	challenge := findChallenge(resp.Header.Values("WWW-Authenticate"))
	if "" == challenge.scheme || "" != l.login.RegistryToken {
		return resp, nil
	}
	_ = resp.Body.Close()
	var value string
	var expires time.Time
	switch challenge.scheme {
	case "basic":
		if "" == l.login.Username && "" == l.login.Password {
			return nil, errors.New("HTTP 401 Unauthorized: registry requires a configured Basic login")
		}
		value = "Basic " + base64.StdEncoding.EncodeToString([]byte(l.login.Username+":"+l.login.Password))
		l.secrets = append(l.secrets, strings.TrimPrefix(value, "Basic "))
		expires = c.Now().Add(time.Hour)
	case "bearer":
		value, expires, err = l.exchange(ctx, challenge.params)
		if nil != err {
			return nil, err
		}
	}
	// A rejected authenticated request is returned to the caller, never retried
	// anonymously. Cache only an authorization the registry actually accepted.
	resp, err = l.do(ctx, method, endpoint, value)
	if nil == err && http.StatusUnauthorized != resp.StatusCode && http.StatusForbidden != resp.StatusCode {
		c.mu.Lock()
		c.tokens[key] = authorization{value: value, expires: expires}
		c.mu.Unlock()
	}
	return resp, err
}

type authChallenge struct {
	scheme string
	params map[string]string
}

var challengeStart = regexp.MustCompile(`(?i)(?:^|,)\s*(Bearer|Basic)\s+`)
var challengeParam = regexp.MustCompile(`([a-zA-Z_]+)\s*=\s*("(?:\\.|[^"\\])*"|[^,\s]+)`)

func findChallenge(headers []string) authChallenge {
	var basic authChallenge
	for _, header := range headers {
		matches := challengeStart.FindAllStringSubmatchIndex(header, -1)
		for i, match := range matches {
			end := len(header)
			if i+1 < len(matches) {
				end = matches[i+1][0]
			}
			challenge := authChallenge{scheme: strings.ToLower(header[match[2]:match[3]]), params: make(map[string]string)}
			for _, pair := range challengeParam.FindAllStringSubmatch(header[match[1]:end], -1) {
				value := pair[2]
				if strings.HasPrefix(value, `"`) {
					decoded, err := strconv.Unquote(value)
					if nil != err {
						continue
					}
					value = decoded
				}
				challenge.params[strings.ToLower(pair[1])] = value
			}
			if "bearer" == challenge.scheme {
				return challenge
			}
			basic = challenge
		}
	}
	return basic
}

func (l *lookup) exchange(ctx context.Context, params map[string]string) (string, time.Time, error) {
	realm, err := url.Parse(params["realm"])
	if nil != err || "https" != realm.Scheme || "" == realm.Host || nil != realm.User || "" != realm.Fragment {
		return "", time.Time{}, errors.New("HTTP 401 Unauthorized: registry Bearer challenge has no valid HTTPS token realm")
	}
	scope := params["scope"]
	if "" == scope {
		scope = "repository:" + l.ref.repository + ":pull"
	}
	values := realm.Query()
	values.Set("service", params["service"])
	values.Set("scope", scope)
	method := http.MethodGet
	var body io.Reader
	if "" != l.login.IdentityToken {
		method = http.MethodPost
		values = url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {l.login.IdentityToken},
			"service":       {params["service"]},
			"scope":         {scope},
			"client_id":     {"docker"},
		}
		body = strings.NewReader(values.Encode())
	} else {
		if "" != l.login.Username {
			values.Set("account", l.login.Username)
		}
		realm.RawQuery = values.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, realm.String(), body)
	if nil != err {
		return "", time.Time{}, errors.New("invalid registry token request")
	}
	if http.MethodPost == method {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else if "" != l.login.Username || "" != l.login.Password {
		req.SetBasicAuth(l.login.Username, l.login.Password)
	}
	resp, err := l.send(req)
	if nil != err {
		return "", time.Time{}, err
	}
	if http.StatusOK != resp.StatusCode {
		return "", time.Time{}, responseError(resp)
	}
	defer func() { _ = resp.Body.Close() }()
	var token struct {
		Token       string    `json:"token"`
		AccessToken string    `json:"access_token"`
		ExpiresIn   int64     `json:"expires_in"`
		IssuedAt    time.Time `json:"issued_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&token); nil != err {
		return "", time.Time{}, errors.New("registry returned an invalid token response")
	}
	if "" != token.AccessToken {
		token.Token = token.AccessToken
	}
	if "" == token.Token {
		return "", time.Time{}, errors.New("registry token response is missing an access token")
	}
	l.secrets = append(l.secrets, token.Token)
	if 0 >= token.ExpiresIn {
		token.ExpiresIn = 60
	}
	// Leave a small margin rather than reusing a token at its exact expiry.
	lifetime := time.Duration(token.ExpiresIn) * time.Second
	if lifetime > 10*time.Second {
		lifetime -= 10 * time.Second
	}
	issued := token.IssuedAt
	if issued.IsZero() {
		issued = l.client.Now()
	}
	return "Bearer " + token.Token, issued.Add(lifetime), nil
}
