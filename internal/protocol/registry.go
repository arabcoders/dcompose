package protocol

import (
	"net/http"

	"github.com/arabcoders/dcompose/internal/engine"
)

type RegistryRequest struct {
	Method string      `json:"method"`
	URL    string      `json:"url"`
	Header http.Header `json:"header"`
	Body   []byte      `json:"body,omitempty"`
}

type RegistryResponse struct {
	Status int         `json:"status"`
	Header http.Header `json:"header"`
	Body   []byte      `json:"body,omitempty"`
	Error  string      `json:"error,omitempty"`
}

type ImageInspectRequest struct {
	Stack     string `json:"stack"`
	Container string `json:"container,omitempty"`
}

type ImageInspection struct {
	ID      string             `json:"id"`
	Service engine.Service     `json:"service"`
	Target  engine.ImageTarget `json:"target"`
}

type ImageInspectResponse struct {
	Images []ImageInspection `json:"images"`
}

type ImageInvalidated struct {
	Stack string   `json:"stack"`
	Refs  []string `json:"refs"`
}

// RegistryCredential is shared only between the server and authenticated nodes.
type RegistryCredential struct {
	ID               string            `json:"id"`
	Registry         string            `json:"registry"`
	Label            string            `json:"label"`
	Auth             map[string]string `json:"auth"`
	BackgroundChecks bool              `json:"background_checks"`
}

// RegistrySync replaces a node's managed registry credentials.
type RegistrySync struct {
	Revision   string               `json:"revision"`
	Registries []RegistryCredential `json:"registries"`
}

type RegistryApplied struct {
	Revision string `json:"revision"`
}
