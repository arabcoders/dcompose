package registry

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
	"github.com/arabcoders/dcompose/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.Run(m)) }

var linuxAMD64 = engine.ImagePlatform{OS: "linux", Architecture: "amd64"}

// encode handles heterogeneous fixtures only at the encoding/json test boundary.
func encode(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if nil != err {
		t.Fatal(err)
	}
	return data
}

func imageManifest(t *testing.T, platform engine.ImagePlatform, media string) ([]byte, []byte) {
	t.Helper()
	config := encode(t, platform)
	return encode(t, manifest{SchemaVersion: 2, MediaType: media, Config: descriptor{Digest: contentDigest(config)}}), config
}

func serve(t *testing.T, handler http.HandlerFunc) (*Client, engine.ImageKey) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return NewClient(
		server.Client(),
	), engine.ImageKey{
		Ref:           strings.TrimPrefix(server.URL, "https://") + "/team/app:latest",
		ImagePlatform: linuxAMD64,
	}
}

func respond(w http.ResponseWriter, r *http.Request, data []byte) {
	w.Header().Set("Docker-Content-Digest", contentDigest(data))
	if http.MethodHead != r.Method {
		_, _ = w.Write(data)
	}
}

func TestIndex(t *testing.T) {
	for _, media := range []string{ociIndex, dockerList} {
		t.Run(media, func(t *testing.T) {
			child, _ := imageManifest(t, linuxAMD64, ociManifest)
			arm := engine.ImagePlatform{OS: "linux", Architecture: "arm64", Variant: "v8"}
			attestation := engine.ImagePlatform{OS: "unknown", Architecture: "unknown"}
			root := encode(t, manifest{SchemaVersion: 2, MediaType: media, Manifests: []descriptor{
				{Digest: "sha256:" + strings.Repeat("a", 64), Platform: &arm},
				{Digest: contentDigest(child), Platform: &linuxAMD64},
				{Digest: "sha256:" + strings.Repeat("c", 64), Platform: &attestation},
				{Digest: "sha256:" + strings.Repeat("d", 64)},
			}})
			requests := make(map[string]int)
			var mu sync.Mutex
			client, key := serve(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				requests[r.Method+" "+r.URL.Path]++
				switch r.URL.Path {
				case "/v2/team/app/manifests/latest":
					respond(w, r, root)
				case "/v2/team/app/manifests/" + contentDigest(child):
					respond(w, r, child)
				default:
					t.Errorf("unrelated manifest, config, or layer requested: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			for range 2 {
				got := client.InspectConfig(context.Background(), key, []byte("{}"))
				var parsed manifest
				_ = json.Unmarshal(child, &parsed)
				if parsed.Config.Digest != got.Digest || "" != got.Status {
					t.Fatalf("image: %+v", got)
				}
			}
			want := map[string]int{
				"HEAD /v2/team/app/manifests/latest":                 2,
				"GET /v2/team/app/manifests/latest":                  1,
				"GET /v2/team/app/manifests/" + contentDigest(child): 1,
			}
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(want, requests) {
				t.Fatalf("requests: %#v", requests)
			}
		})
	}
}

func TestPlatforms(t *testing.T) {
	for _, test := range []struct {
		name                string
		platform, requested engine.ImagePlatform
		copies              int
		reason              string
	}{
		{"exact", linuxAMD64, linuxAMD64, 1, ""},
		{"missing", engine.ImagePlatform{OS: "linux", Architecture: "arm64"}, linuxAMD64, 1, "platform_missing"},
		{"ambiguous", linuxAMD64, linuxAMD64, 2, "platform_ambiguous"},
		{"arm64_baseline", engine.ImagePlatform{OS: "linux", Architecture: "arm64", Variant: "v8"}, engine.ImagePlatform{OS: "linux", Architecture: "arm64"}, 1, ""},
		{"arm_variant", engine.ImagePlatform{OS: "linux", Architecture: "arm", Variant: "v7"}, engine.ImagePlatform{OS: "linux", Architecture: "arm", Variant: "v6"}, 1, "platform_missing"},
		{"windows_version", engine.ImagePlatform{OS: "windows", Architecture: "amd64", OSVersion: "10.0.1"}, engine.ImagePlatform{OS: "windows", Architecture: "amd64", OSVersion: "10.0.2"}, 1, "platform_missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			child, _ := imageManifest(t, test.platform, dockerManifest)
			root := manifest{SchemaVersion: 2, MediaType: dockerList}
			for i := 0; i < test.copies; i++ {
				root.Manifests = append(root.Manifests, descriptor{Digest: contentDigest(child), Platform: &test.platform})
			}
			data := encode(t, root)
			client, key := serve(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/latest") {
					respond(w, r, data)
					return
				}
				if "" != test.reason {
					t.Error("fetched a child for a missing/ambiguous platform")
				}
				respond(w, r, child)
			})
			key.ImagePlatform = test.requested
			got := client.InspectConfig(context.Background(), key, []byte("{}"))
			if test.reason != got.Reason {
				t.Fatalf("image: %+v", got)
			}
			if "" != test.reason && "unsupported" != got.Status {
				t.Fatalf("image: %+v", got)
			}
		})
	}
}

func TestSingle(t *testing.T) {
	for _, media := range []string{ociManifest, dockerManifest} {
		for _, headStatus := range []int{200, 405, 501} {
			t.Run(fmt.Sprintf("%s/%d", media, headStatus), func(t *testing.T) {
				root, config := imageManifest(t, linuxAMD64, media)
				var heads, gets, blobs int
				var mu sync.Mutex
				client, key := serve(t, func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					if http.MethodHead == r.Method {
						heads++
						if 200 != headStatus {
							w.WriteHeader(headStatus)
							return
						}
					}
					if strings.Contains(r.URL.Path, "/blobs/") {
						blobs++
						respond(w, r, config)
						return
					}
					if http.MethodGet == r.Method {
						gets++
					}
					respond(w, r, root)
				})
				for range 2 {
					got := client.InspectConfig(context.Background(), key, []byte("{}"))
					if contentDigest(config) != got.Digest || "" != got.Status {
						t.Fatalf("image: %+v", got)
					}
				}
				wantGets := 1
				if 200 != headStatus {
					wantGets = 2
				}
				mu.Lock()
				defer mu.Unlock()
				if 2 != heads || wantGets != gets || 1 != blobs {
					t.Fatalf("requests: heads=%d gets=%d blobs=%d", heads, gets, blobs)
				}
			})
		}
	}
}

func TestSinglePlatform(t *testing.T) {
	for _, test := range []struct {
		name              string
		actual, requested engine.ImagePlatform
		reason            string
	}{
		{"arm64", engine.ImagePlatform{OS: "linux", Architecture: "arm64", Variant: "v8"}, engine.ImagePlatform{OS: "linux", Architecture: "arm64"}, ""},
		{"arm", engine.ImagePlatform{OS: "linux", Architecture: "arm", Variant: "v7"}, engine.ImagePlatform{OS: "linux", Architecture: "arm", Variant: "v6"}, "platform_missing"},
		{"windows", engine.ImagePlatform{OS: "windows", Architecture: "amd64", OSVersion: "10.0.1"}, engine.ImagePlatform{OS: "windows", Architecture: "amd64", OSVersion: "10.0.2"}, "platform_missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, config := imageManifest(t, test.actual, ociManifest)
			client, key := serve(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/blobs/") {
					respond(w, r, config)
				} else {
					respond(w, r, root)
				}
			})
			key.ImagePlatform = test.requested
			got := client.InspectConfig(context.Background(), key, []byte("{}"))
			if test.reason != got.Reason {
				t.Fatalf("platform: %+v", got)
			}
		})
	}
}

func TestMediaTypeHeader(t *testing.T) {
	root, config := imageManifest(t, linuxAMD64, ociManifest)
	root = bytes.Replace(root, []byte(`"mediaType":"`+ociManifest+`"`), []byte(`"mediaType":""`), 1)
	client, key := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", ociManifest+"; charset=utf-8")
		if strings.Contains(r.URL.Path, "/blobs/") {
			respond(w, r, config)
		} else {
			respond(w, r, root)
		}
	})
	if got := client.InspectConfig(context.Background(), key, []byte("{}")); "" != got.Status || contentDigest(config) != got.Digest {
		t.Fatalf("manifest media type: %+v", got)
	}
}

func TestSharedIndex(t *testing.T) {
	arm := engine.ImagePlatform{OS: "linux", Architecture: "arm64"}
	amdManifest, _ := imageManifest(t, linuxAMD64, ociManifest)
	armManifest, _ := imageManifest(t, arm, ociManifest)
	root := encode(
		t,
		manifest{
			SchemaVersion: 2,
			MediaType:     ociIndex,
			Manifests: []descriptor{
				{Digest: contentDigest(amdManifest), Platform: &linuxAMD64},
				{Digest: contentDigest(armManifest), Platform: &arm},
			},
		},
	)
	var mu sync.Mutex
	var heads, roots, children int
	client, key := serve(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if http.MethodHead == r.Method {
			heads++
			respond(w, r, root)
			return
		}
		switch r.URL.Path {
		case "/v2/team/app/manifests/latest":
			roots++
			respond(w, r, root)
		case "/v2/team/app/manifests/" + contentDigest(amdManifest):
			children++
			respond(w, r, amdManifest)
		case "/v2/team/app/manifests/" + contentDigest(armManifest):
			children++
			respond(w, r, armManifest)
		default:
			t.Errorf("unexpected metadata request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	amd := client.InspectConfig(context.Background(), key, []byte("{}"))
	key.ImagePlatform = arm
	armResult := client.InspectConfig(context.Background(), key, []byte("{}"))
	if "" != amd.Status || "" != armResult.Status || amd.Digest == armResult.Digest {
		t.Fatalf("platform results mixed: amd=%+v arm=%+v", amd, armResult)
	}
	mu.Lock()
	defer mu.Unlock()
	if 2 != heads || 1 != roots || 2 != children {
		t.Fatalf("shared index refetched: heads=%d roots=%d children=%d", heads, roots, children)
	}
}

func TestObservationCache(t *testing.T) {
	root, config := imageManifest(t, linuxAMD64, ociManifest)
	var requests atomic.Int32
	client, key := serve(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if strings.Contains(r.URL.Path, "/blobs/") {
			respond(w, r, config)
		} else {
			respond(w, r, root)
		}
	})
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	client.Now = func() time.Time { return now }
	first := client.InspectConfig(context.Background(), key, []byte("{}"), 10*time.Minute)
	if "" != first.Status || now != first.CheckedAt || 3 != requests.Load() {
		t.Fatalf("first observation: %+v requests=%d", first, requests.Load())
	}
	now = now.Add(5 * time.Minute)
	cached := client.InspectConfig(context.Background(), key, []byte("{}"), 10*time.Minute)
	if first.CheckedAt != cached.CheckedAt || 3 != requests.Load() {
		t.Fatalf("cache hit renewed observation or used HTTP: %+v requests=%d", cached, requests.Load())
	}
	short := client.InspectConfig(context.Background(), key, []byte("{}"), time.Minute)
	if now != short.CheckedAt || 4 != requests.Load() {
		t.Fatalf("short interval did not HEAD refresh: %+v requests=%d", short, requests.Load())
	}
	now = now.Add(time.Minute)
	manual := client.InspectConfig(context.Background(), key, []byte("{}"), 0)
	if now != manual.CheckedAt || 5 != requests.Load() {
		t.Fatalf("manual check did not refresh: %+v requests=%d", manual, requests.Load())
	}
	client.ApplyCooldown(engine.RegistryCooldown{Registry: strings.SplitN(key.Ref, "/", 2)[0], Until: now.Add(time.Hour), Attempts: 1})
	cached = client.InspectConfig(context.Background(), key, []byte("{}"), 10*time.Minute)
	if "" != cached.Status || manual.CheckedAt != cached.CheckedAt || 5 != requests.Load() {
		t.Fatalf("fresh cached result blocked by HTTP cooldown: %+v requests=%d", cached, requests.Load())
	}
	blocked := client.InspectConfig(context.Background(), key, []byte("{}"), 0)
	if nil == blocked.Cooldown || 5 != requests.Load() {
		t.Fatalf("manual HTTP bypassed cooldown: %+v requests=%d", blocked, requests.Load())
	}
}

func TestObservationFailure(t *testing.T) {
	arm := engine.ImagePlatform{OS: "linux", Architecture: "arm64"}
	amdManifest, _ := imageManifest(t, linuxAMD64, ociManifest)
	armManifest, _ := imageManifest(t, arm, ociManifest)
	root := encode(
		t,
		manifest{
			SchemaVersion: 2,
			MediaType:     ociIndex,
			Manifests: []descriptor{
				{Digest: contentDigest(amdManifest), Platform: &linuxAMD64},
				{Digest: contentDigest(armManifest), Platform: &arm},
			},
		},
	)
	var requests atomic.Int32
	client, key := serve(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/v2/team/app/manifests/latest":
			respond(w, r, root)
		case "/v2/team/app/manifests/" + contentDigest(amdManifest):
			respond(w, r, amdManifest)
		case "/v2/team/app/manifests/" + contentDigest(armManifest):
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected metadata request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	client.Now = func() time.Time { return now }
	first := client.InspectConfig(context.Background(), key, []byte("{}"), 6*time.Hour)
	now = now.Add(time.Hour)
	armKey := key
	armKey.ImagePlatform = arm
	failed := client.InspectConfig(context.Background(), armKey, []byte("{}"), 6*time.Hour)
	if "error" != failed.Status || now != failed.CheckedAt || 4 != requests.Load() {
		t.Fatalf("platform failure reused an old retry timestamp: %+v requests=%d", failed, requests.Load())
	}
	cached := client.InspectConfig(context.Background(), key, []byte("{}"), 6*time.Hour)
	if "" != cached.Status || first.CheckedAt != cached.CheckedAt || 4 != requests.Load() {
		t.Fatalf("platform failure changed the root observation: %+v requests=%d", cached, requests.Load())
	}
}

func TestConcurrentRoot(t *testing.T) {
	arm := engine.ImagePlatform{OS: "linux", Architecture: "arm64"}
	amdManifest, _ := imageManifest(t, linuxAMD64, ociManifest)
	armManifest, _ := imageManifest(t, arm, ociManifest)
	root := encode(
		t,
		manifest{
			SchemaVersion: 2,
			MediaType:     ociIndex,
			Manifests: []descriptor{
				{Digest: contentDigest(amdManifest), Platform: &linuxAMD64},
				{Digest: contentDigest(armManifest), Platform: &arm},
			},
		},
	)
	entered, release := make(chan struct{}), make(chan struct{})
	var heads, roots, children atomic.Int32
	client, key := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if http.MethodHead == r.Method {
			if 1 == heads.Add(1) {
				close(entered)
			}
			<-release
			respond(w, r, root)
			return
		}
		switch r.URL.Path {
		case "/v2/team/app/manifests/latest":
			roots.Add(1)
			respond(w, r, root)
		case "/v2/team/app/manifests/" + contentDigest(amdManifest):
			children.Add(1)
			respond(w, r, amdManifest)
		case "/v2/team/app/manifests/" + contentDigest(armManifest):
			children.Add(1)
			respond(w, r, armManifest)
		default:
			t.Errorf("unexpected registry request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	var workers, started sync.WaitGroup
	started.Add(4)
	workers.Add(4)
	for i := range 4 {
		go func() {
			defer workers.Done()
			target := key
			if 1 == i%2 {
				target.ImagePlatform = arm
			}
			started.Done()
			if got := client.InspectConfig(context.Background(), target, []byte("{}"), time.Hour); "" != got.Status {
				t.Errorf("image: %+v", got)
			}
		}()
	}
	started.Wait()
	<-entered
	close(release)
	workers.Wait()
	if 1 != heads.Load() || 1 != roots.Load() || 2 != children.Load() {
		t.Fatalf("concurrent metadata not shared: heads=%d roots=%d children=%d", heads.Load(), roots.Load(), children.Load())
	}
}

func TestRootChange(t *testing.T) {
	child, _ := imageManifest(t, linuxAMD64, ociManifest)
	var mu sync.Mutex
	current := child
	rootFor := func(data []byte) []byte {
		return encode(
			t,
			manifest{SchemaVersion: 2, MediaType: ociIndex, Manifests: []descriptor{{Digest: contentDigest(data), Platform: &linuxAMD64}}},
		)
	}
	roots, children := 0, 0
	client, key := serve(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/latest") {
			if http.MethodGet == r.Method {
				roots++
			}
			respond(w, r, rootFor(current))
		} else {
			children++
			respond(w, r, current)
		}
	})
	first := client.InspectConfig(context.Background(), key, []byte("{}"), time.Hour)
	mu.Lock()
	current = bytes.Replace(child, []byte(first.Digest), []byte("sha256:"+strings.Repeat("b", 64)), 1)
	mu.Unlock()
	second := client.InspectConfig(context.Background(), key, []byte("{}"), 0)
	if "" != second.Status || first.Digest == second.Digest {
		t.Fatalf("root change retained old platform digest: first=%+v second=%+v", first, second)
	}
	mu.Lock()
	defer mu.Unlock()
	if 2 != roots || 2 != children {
		t.Fatalf("changed root metadata not refreshed: roots=%d children=%d", roots, children)
	}
}

func TestInvalidMetadata(t *testing.T) {
	for _, name := range []string{"json", "schema1", "config_digest", "root_digest", "child_digest", "blob_digest", "platform_missing", "platform_unknown", "reference_digest"} {
		t.Run(name, func(t *testing.T) {
			root, config := imageManifest(t, linuxAMD64, ociManifest)
			child := root
			switch name {
			case "json":
				root = []byte(`{"private-manifest-content":`)
			case "schema1":
				root = []byte(`{"schemaVersion":1}`)
			case "config_digest":
				root = []byte(`{"schemaVersion":2,"mediaType":"` + ociManifest + `","config":{"digest":"bad"}}`)
			case "child_digest":
				root = encode(
					t,
					manifest{
						SchemaVersion: 2,
						MediaType:     ociIndex,
						Manifests:     []descriptor{{Platform: &linuxAMD64, Digest: "sha256:" + strings.Repeat("a", 64)}},
					},
				)
			case "blob_digest":
				config = []byte("wrong bytes")
			case "platform_missing":
				root, config = imageManifest(t, engine.ImagePlatform{OS: "linux", Architecture: "arm64"}, ociManifest)
			case "platform_unknown":
				root, config = imageManifest(t, engine.ImagePlatform{}, ociManifest)
			}
			client, key := serve(t, func(w http.ResponseWriter, r *http.Request) {
				data := root
				if strings.Contains(r.URL.Path, "/blobs/") {
					data = config
				} else if !strings.HasSuffix(r.URL.Path, "/latest") {
					data = child
				}
				if "root_digest" == name && http.MethodGet == r.Method {
					w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("a", 64))
					_, _ = w.Write(data)
					return
				}
				respond(w, r, data)
			})
			if "reference_digest" == name {
				key.Ref = strings.TrimSuffix(key.Ref, ":latest") + "@sha256:" + strings.Repeat("a", 64)
			}
			got := client.InspectConfig(context.Background(), key, []byte("{}"))
			want := "manifest_invalid"
			if "platform_missing" == name {
				want = "platform_missing"
			}
			if want != got.Reason || "" != got.Digest {
				t.Fatalf("image: %+v", got)
			}
		})
	}
}

func authConfig(host string, login credentials) []byte {
	data, _ := json.Marshal(map[string]map[string]credentials{"auths": {host: login}})
	return data
}

func TestAuth(t *testing.T) {
	for _, name := range []string{"basic", "bearer", "identity", "registrytoken", "anonymous", "denied", "token_denied"} {
		t.Run(name, func(t *testing.T) {
			root, config := imageManifest(t, linuxAMD64, ociManifest)
			var server *httptest.Server
			var tokenCalls, anonymous, authorized int
			var mu sync.Mutex
			server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if "/token" == r.URL.Path {
					tokenCalls++
					if "identity" == name {
						if err := r.ParseForm(); nil != err {
							t.Error(err)
						}
						if http.MethodPost != r.Method || "refresh_token" != r.Form.Get("grant_type") ||
							"refresh-secret" != r.Form.Get("refresh_token") {
							t.Errorf("refresh grant: %s %#v", r.Method, r.Form)
						}
					} else {
						username, password, ok := r.BasicAuth()
						if "anonymous" != name && (!ok || "user" != username || "password" != password) {
							t.Errorf("token login: %q/%q", username, password)
						}
					}
					values := r.URL.Query()
					if "identity" == name {
						values = r.Form
					}
					if "repository:team/app:pull" != values.Get("scope") || "registry.test" != values.Get("service") {
						t.Errorf("token scope: %#v", values)
					}
					if "token_denied" == name {
						http.Error(w, "login rejected", http.StatusUnauthorized)
						return
					}
					_, _ = w.Write([]byte(`{"access_token":"exchanged-secret","expires_in":300}`))
					return
				}
				expected := "Bearer exchanged-secret"
				if "registrytoken" == name {
					expected = "Bearer registry-secret"
				}
				if "basic" == name {
					expected = "Basic " + base64.StdEncoding.EncodeToString([]byte("user:password"))
				}
				if "" == r.Header.Get("Authorization") {
					anonymous++
				} else {
					authorized++
				}
				if r.Header.Get("Authorization") != expected || "denied" == name {
					challenge := fmt.Sprintf(`Bearer realm="%s/token",service="registry.test",scope="repository:team/app:pull"`, server.URL)
					if "basic" == name {
						challenge = `Basic realm="private registry"`
					}
					w.Header().Set("WWW-Authenticate", challenge)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if strings.Contains(r.URL.Path, "/blobs/") {
					respond(w, r, config)
				} else {
					respond(w, r, root)
				}
			}))
			t.Cleanup(server.Close)
			client := NewClient(server.Client())
			key := engine.ImageKey{Ref: strings.TrimPrefix(server.URL, "https://") + "/team/app:latest", ImagePlatform: linuxAMD64}
			host := strings.TrimPrefix(server.URL, "https://")
			login := credentials{Auth: base64.StdEncoding.EncodeToString([]byte("user:password"))}
			switch name {
			case "identity":
				login = credentials{IdentityToken: "refresh-secret"}
			case "registrytoken":
				login = credentials{RegistryToken: "registry-secret"}
			case "anonymous":
				login = credentials{}
			}
			for range 2 {
				configKey := host
				if "identity" == name {
					configKey = "https://" + host + "/v1/access-token"
				}
				if "registrytoken" == name {
					configKey = "https://" + host + "/v1/refresh-token"
				}
				got := client.InspectConfig(context.Background(), key, authConfig(configKey, login))
				if "denied" == name || "token_denied" == name {
					if "registry_error" != got.Reason {
						t.Fatalf("denied login: %+v", got)
					}
					break
				}
				if contentDigest(config) != got.Digest || "" != got.Status {
					t.Fatalf("image: %+v", got)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if "registrytoken" == name {
				if 0 != tokenCalls || 0 != anonymous {
					t.Fatalf("registry token was exchanged or retried anonymously")
				}
			} else if "basic" == name {
				if 0 != tokenCalls || 1 != anonymous {
					t.Fatalf("Basic requests: anonymous=%d token=%d", anonymous, tokenCalls)
				}
			} else if 1 != tokenCalls || 1 != anonymous {
				t.Fatalf("token requests: anonymous=%d token=%d authorized=%d", anonymous, tokenCalls, authorized)
			}
		})
	}
}

func TestCredentialIsolation(t *testing.T) {
	root, config := imageManifest(t, linuxAMD64, ociManifest)
	var server *httptest.Server
	var grants []string
	var mu sync.Mutex
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if "/token" == r.URL.Path {
			_, password, _ := r.BasicAuth()
			grants = append(grants, password)
			_, _ = fmt.Fprintf(w, `{"token":%q,"expires_in":300}`, password)
			return
		}
		if "" == r.Header.Get("Authorization") {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token"`, server.URL))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.Contains(r.URL.Path, "/blobs/") {
			respond(w, r, config)
		} else {
			respond(w, r, root)
		}
	}))
	t.Cleanup(server.Close)
	client := NewClient(server.Client())
	host := strings.TrimPrefix(server.URL, "https://")
	key := engine.ImageKey{Ref: host + "/team/app:latest", ImagePlatform: linuxAMD64}
	for _, password := range []string{"full-secret", "limited-secret", "full-secret"} {
		if got := client.InspectConfig(
			context.Background(),
			key,
			authConfig(host, credentials{Username: "same-user", Password: password}),
			time.Hour,
		); "" != got.Status {
			t.Fatalf("image: %+v", got)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(grants, []string{"full-secret", "limited-secret"}) {
		t.Fatalf("credential caches mixed: %#v", grants)
	}
	if (credentials{Username: "same-user", Password: "full-secret"}).account() != (credentials{Username: "same-user", Password: "limited-secret"}).account() {
		t.Fatal("same account has different quota keys")
	}
}

func TestTokenExpiry(t *testing.T) {
	root, config := imageManifest(t, linuxAMD64, ociManifest)
	var mu sync.Mutex
	grants := 0
	client, key := serve(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if "/token" == r.URL.Path {
			grants++
			_, _ = w.Write([]byte(`{"token":"short-lived-secret","expires_in":30}`))
			return
		}
		if "" == r.Header.Get("Authorization") {
			w.Header().Set("WWW-Authenticate", `Bearer realm="https://`+r.Host+`/token"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.Contains(r.URL.Path, "/blobs/") {
			respond(w, r, config)
		} else {
			respond(w, r, root)
		}
	})
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	client.Now = func() time.Time { return now }
	for _, elapsed := range []time.Duration{0, 10 * time.Second, 30 * time.Second} {
		now = now.Add(elapsed)
		if got := client.InspectConfig(context.Background(), key, []byte("{}")); "" != got.Status {
			t.Fatalf("image: %+v", got)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if 2 != grants {
		t.Fatalf("token expiry ignored: grants=%d", grants)
	}
}

func TestNativeAuthKeys(t *testing.T) {
	data := []byte(
		`{"auths":{"https://index.docker.io/v1/":{"auth":"dXNlcjpwYXNz"},"https://index.docker.io/v1/access-token":{"identitytoken":"access-secret"},"https://index.docker.io/v1/refresh-token":{"registrytoken":"refresh-secret"},"https://private.test":{"registrytoken":"private-secret"}}}`,
	)
	for _, host := range []string{"docker.io", "index.docker.io", "registry-1.docker.io"} {
		login, err := resolveCredentials(data, canonicalHost(host))
		if nil != err || "user" != login.Username || "pass" != login.Password || "" != login.IdentityToken || "" != login.RegistryToken {
			t.Fatalf("root alias %s: %+v %v", host, login, err)
		}
	}
	login, err := resolveCredentials(data, "private.test")
	if nil != err || "private-secret" != login.RegistryToken {
		t.Fatalf("native key: %+v %v", login, err)
	}
	emptyRoot := []byte(`{"auths":{"https://index.docker.io/v1/":{},"https://index.docker.io/v1/other":{"registrytoken":"not-selected"}}}`)
	if login, err := resolveCredentials(emptyRoot, "docker.io"); nil != err || (credentials{}) != login {
		t.Fatalf("exact empty root was replaced by another login: %+v %v", login, err)
	}
	for _, test := range []struct {
		key   string
		login credentials
	}{
		{"https://index.docker.io/v1/access-token", credentials{IdentityToken: "native-identity"}},
		{"https://index.docker.io/v1/refresh-token", credentials{RegistryToken: "native-registry"}},
	} {
		login, err := resolveCredentials(authConfig(test.key, test.login), "docker.io")
		if nil != err || test.login != login {
			t.Fatalf("selected legacy record was changed or dropped: %+v %v", login, err)
		}
	}
	for _, invalid := range []string{"null", "[]", `{"auths":"private"}`, `{"auths":{"docker.io":{"auth":"bad-base64"}}}`, `{"credsStore":"helper"}`} {
		if _, err := resolveCredentials([]byte(invalid), "docker.io"); nil == err {
			t.Fatalf("invalid credentials accepted: %s", invalid)
		}
	}
}

func TestRateLimit(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name, retry string
		delay       time.Duration
	}{
		{"seconds", "90", 90 * time.Second},
		{"date", now.Add(2 * time.Hour).Format(http.TimeFormat), 2 * time.Hour},
		{"invalid", "not-a-date", 5 * time.Minute},
		{"missing", "", 5 * time.Minute},
		{"negative", "-5", 5 * time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			var mu sync.Mutex
			client, key := serve(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				requests++
				w.Header().Set("Retry-After", test.retry)
				w.Header().Set("RateLimit-Limit", "100;w=21600")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"errors":[{"message":"pull quota exceeded"}]}`))
			})
			client.Now = func() time.Time { return now }
			got := client.InspectConfig(context.Background(), key, []byte("{}"))
			if "error" != got.Status || "registry_error" != got.Reason || nil == got.Cooldown ||
				now.Add(test.delay) != got.Cooldown.Until ||
				1 != got.Cooldown.Attempts {
				t.Fatalf("cooldown: %+v", got.Cooldown)
			}
			key.Ref = strings.Replace(key.Ref, "/team/app:", "/other/image:", 1)
			blocked := client.InspectConfig(context.Background(), key, []byte("{}"))
			mu.Lock()
			defer mu.Unlock()
			if nil == blocked.Cooldown || 1 != requests {
				t.Fatalf("provider quota not shared: %+v requests=%d", blocked, requests)
			}
		})
	}
}

func TestCooldownAccounts(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	requests := 0
	var mu sync.Mutex
	client, key := serve(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		w.WriteHeader(http.StatusTooManyRequests)
	})
	client.Now = func() time.Time { return now }
	host, _, _ := strings.Cut(key.Ref, "/")
	for _, username := range []string{"one", "two", "one"} {
		if got := client.InspectConfig(
			context.Background(),
			key,
			authConfig(host, credentials{Username: username, Password: "secret"}),
		); nil == got.Cooldown {
			t.Fatalf("missing cooldown: %+v", got)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if 2 != requests {
		t.Fatalf("accounts not separated: requests=%d", requests)
	}
	states := client.Cooldowns()
	if 2 != len(states) || states[0].Account >= states[1].Account {
		t.Fatalf("unstable cooldowns: %+v", states)
	}
	state := states[0]
	state.Until = now.Add(2 * time.Hour)
	state.Attempts = 4
	client.ApplyCooldown(state)
	state.Until = now.Add(time.Minute)
	state.Attempts = 2
	client.ApplyCooldown(state)
	merged := client.Cooldowns()[0]
	if now.Add(2*time.Hour) != merged.Until || 4 != merged.Attempts {
		t.Fatalf("merge shortened cooldown: %+v", merged)
	}
	client.ApplyCooldown(engine.RegistryCooldown{Registry: "index.docker.io", Until: now.Add(time.Hour), Attempts: 2})
	if nil == client.activeCooldown("docker.io", "") || nil != client.activeCooldown("elsewhere.test", "") {
		t.Fatal("provider alias cooldown mismatch")
	}
}

func TestCooldownBackoff(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	limited := true
	var mu sync.Mutex
	root, config := imageManifest(t, linuxAMD64, ociManifest)
	client, key := serve(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if limited {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if strings.Contains(r.URL.Path, "/blobs/") {
			respond(w, r, config)
		} else {
			respond(w, r, root)
		}
	})
	client.Now = func() time.Time { return now }
	for i, delay := range []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute, time.Hour, time.Hour} {
		got := client.InspectConfig(context.Background(), key, []byte("{}"))
		if nil == got.Cooldown || now.Add(delay) != got.Cooldown.Until || i+1 != got.Cooldown.Attempts {
			t.Fatalf("attempt %d: %+v", i, got.Cooldown)
		}
		now = got.Cooldown.Until
	}
	mu.Lock()
	limited = false
	mu.Unlock()
	if got := client.InspectConfig(context.Background(), key, []byte("{}")); "" != got.Status || 0 != len(client.Cooldowns()) {
		t.Fatalf("success did not clear expired backoff: %+v", got)
	}
}

func TestTokenRateLimit(t *testing.T) {
	var server *httptest.Server
	requests := 0
	var mu sync.Mutex
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		if "/token" == r.URL.Path {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token"`, server.URL))
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	client := NewClient(server.Client())
	now := time.Now()
	client.Now = func() time.Time { return now }
	key := engine.ImageKey{Ref: strings.TrimPrefix(server.URL, "https://") + "/team/app:latest", ImagePlatform: linuxAMD64}
	got := client.InspectConfig(context.Background(), key, []byte("{}"))
	if nil == got.Cooldown || now.Add(2*time.Minute) != got.Cooldown.Until {
		t.Fatalf("token quota: %+v", got)
	}
	_ = client.InspectConfig(context.Background(), key, []byte("{}"))
	mu.Lock()
	defer mu.Unlock()
	if 2 != requests {
		t.Fatalf("token cooldown not shared: %d", requests)
	}
}

func TestCooldownLogs(t *testing.T) {
	var logs bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	client, key := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTooManyRequests) })
	got := client.InspectConfig(context.Background(), key, []byte("{}"))
	first := logs.String()
	var record struct {
		Level, Image, Registry, Error string
		Message                       string    `json:"msg"`
		RetryAt                       time.Time `json:"retry_at"`
	}
	if err := json.Unmarshal(logs.Bytes(), &record); nil != err {
		t.Fatal(err)
	}
	if nil == got.Cooldown || "WARN" != record.Level || key.Ref != record.Image || got.Cooldown.Registry != record.Registry ||
		!got.Cooldown.Until.Equal(record.RetryAt) ||
		!strings.Contains(record.Error, fmt.Sprint(http.StatusTooManyRequests)) {
		t.Fatalf("cooldown warning: %s", first)
	}
	if !strings.Contains(record.Message, key.Ref) {
		t.Fatalf("warning hides image: %s", record.Message)
	}
	key.Ref = strings.Replace(key.Ref, "/team/app:", "/other/image:", 1)
	_ = client.InspectConfig(context.Background(), key, []byte("{}"))
	if first != logs.String() {
		t.Fatalf("active cooldown logged another warning: %s", &logs)
	}
}

func TestTLS(t *testing.T) {
	root, config := imageManifest(t, linuxAMD64, ociManifest)
	trusted, key := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/blobs/") {
			respond(w, r, config)
		} else {
			respond(w, r, root)
		}
	})
	if got := NewClient(nil).InspectConfig(context.Background(), key, []byte("{}")); "registry_error" != got.Reason {
		t.Fatalf("untrusted TLS certificate accepted: %+v", got)
	}
	if got := trusted.InspectConfig(context.Background(), key, []byte("{}")); "" != got.Status {
		t.Fatalf("injected trusted client failed: %+v", got)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp: registry is offline")
}

func TestTransportFailure(t *testing.T) {
	var logs bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(original) })
	key := engine.ImageKey{Ref: "registry.test/team/app:latest", ImagePlatform: linuxAMD64}
	client := NewClient(&http.Client{Transport: failingTransport{}})
	if got := client.InspectConfig(context.Background(), key, []byte("{}")); "error" != got.Status || "registry_error" != got.Reason {
		t.Fatalf("no-response transport failure: %+v", got)
	}
	if !strings.Contains(logs.String(), "registry is offline") || !strings.Contains(logs.String(), `"level":"WARN"`) {
		t.Fatalf("transport failure diagnostics: %s", &logs)
	}
	logs.Reset()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := client.InspectConfig(ctx, key, []byte("{}")); "error" != got.Status || "registry_error" != got.Reason {
		t.Fatalf("cancelled inspection: %+v", got)
	}
	if !strings.Contains(logs.String(), context.Canceled.Error()) || !strings.Contains(logs.String(), `"level":"DEBUG"`) {
		t.Fatalf("cancellation diagnostics: %s", &logs)
	}
	for _, status := range []int{401, 403, 404, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			client, key := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
			if got := client.InspectConfig(
				context.Background(),
				key,
				[]byte("{}"),
			); "error" != got.Status ||
				"registry_error" != got.Reason {
				t.Fatalf("registry HTTP error: %+v", got)
			}
		})
	}
}

func TestRedirectFailure(t *testing.T) {
	client, key := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path, http.StatusTemporaryRedirect)
	})
	if got := client.InspectConfig(context.Background(), key, []byte("{}")); "error" != got.Status || "registry_error" != got.Reason {
		t.Fatalf("HTTP redirect failure: %+v", got)
	}
}

func TestDiagnostics(t *testing.T) {
	var logs bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(original) })
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if "/token" == r.URL.Path {
			_, _ = w.Write([]byte(`{"token":"dynamic-secret","expires_in":300}`))
			return
		}
		if "" == r.Header.Get("Authorization") {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token?private=password-secret"`, server.URL))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if http.MethodHead == r.Method {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"message":"login rejected: password-secret refresh-secret static-secret dynamic-secret"}]}`))
	}))
	t.Cleanup(server.Close)
	client := NewClient(server.Client())
	host := strings.TrimPrefix(server.URL, "https://")
	key := engine.ImageKey{Ref: host + "/team/app:latest", ImagePlatform: linuxAMD64}
	data := authConfig(host, credentials{Username: "user", Password: "password-secret", IdentityToken: "refresh-secret"})
	// Keep an unrelated raw record too: all configured secrets must be redacted.
	data = bytes.Replace(data, []byte(`"auths":{`), []byte(`"auths":{"other.test":{"registrytoken":"static-secret"},`), 1)
	got := client.InspectConfig(context.Background(), key, data)
	if "registry_error" != got.Reason {
		t.Fatalf("failure: %+v", got)
	}
	for _, secret := range []string{"password-secret", "refresh-secret", "static-secret", "dynamic-secret", "/token?"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("secret leaked: %s", &logs)
		}
	}
	var record struct {
		Level, Image, Error string
		Message             string `json:"msg"`
	}
	if err := json.Unmarshal(logs.Bytes(), &record); nil != err {
		t.Fatal(err)
	}
	if "WARN" != record.Level || key.Ref != record.Image || !strings.Contains(record.Error, fmt.Sprint(http.StatusForbidden)) ||
		!strings.Contains(record.Error, "login rejected") ||
		!strings.Contains(record.Message, key.Ref) ||
		!strings.Contains(record.Message, "login rejected") {
		t.Fatalf("missing human diagnostics: %s", &logs)
	}
	logs.Reset()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = client.InspectConfig(ctx, key, []byte("{}"))
	if !strings.Contains(logs.String(), `"level":"DEBUG"`) || strings.Contains(logs.String(), `"level":"WARN"`) {
		t.Fatalf("cancellation warning: %s", &logs)
	}
}

func TestConcurrent(t *testing.T) {
	root, config := imageManifest(t, linuxAMD64, ociManifest)
	client, key := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/blobs/") {
			respond(w, r, config)
		} else {
			respond(w, r, root)
		}
	})
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			for range 5 {
				got := client.InspectConfig(context.Background(), key, []byte("{}"))
				if "" != got.Status {
					t.Errorf("image: %+v", got)
				}
				_ = client.Cooldowns()
			}
		})
	}
	workers.Wait()
}

func TestAuthChallenge(t *testing.T) {
	challenge := findChallenge(
		[]string{
			`Basic realm="basic, realm", Bearer realm="https://auth.test/token?a=b", service="registry.test",scope="repository:team/app:pull"`,
		},
	)
	if "bearer" != challenge.scheme || "https://auth.test/token?a=b" != challenge.params["realm"] {
		t.Fatalf("challenge: %+v", challenge)
	}
}
