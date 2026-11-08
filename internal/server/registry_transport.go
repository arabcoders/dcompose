package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/arabcoders/dcompose/internal/protocol"
)

type registryNodeKey struct{}

func registryNodeContext(ctx context.Context, nodeID string) context.Context {
	return context.WithValue(ctx, registryNodeKey{}, nodeID)
}

type registryTransport struct {
	server *Server
}

func (transport registryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if nil != request.Body {
		defer request.Body.Close()
	}
	nodeID, _ := request.Context().Value(registryNodeKey{}).(string)
	if "" == nodeID {
		return nil, errors.New("registry request has no execution node")
	}
	input := protocol.RegistryRequest{Method: request.Method, URL: request.URL.String(), Header: request.Header.Clone()}
	if nil != request.Body {
		body, err := io.ReadAll(io.LimitReader(request.Body, (1<<20)+1))
		if nil != err {
			return nil, errors.New("could not read registry request body")
		}
		if 1<<20 < len(body) {
			return nil, errors.New("registry request body exceeds 1 MiB")
		}
		input.Body = body
	}
	payload, err := json.Marshal(input)
	if nil != err {
		return nil, errors.New("could not encode registry request")
	}
	response, err := transport.server.connect(request.Context(), nodeID, http.MethodPost, "/registry/request", bytes.NewReader(payload))
	if nil != err {
		return nil, fmt.Errorf("registry request through node %q failed: %w", nodeID, err)
	}
	defer response.Body.Close()
	if http.StatusOK != response.StatusCode {
		return nil, readNodeHTTPError(response, fmt.Sprintf("registry request through node %q", nodeID))
	}
	// Base64 encoding of an 8 MiB metadata response fits under this wire limit.
	data, err := io.ReadAll(io.LimitReader(response.Body, (12<<20)+1))
	if nil != err {
		return nil, fmt.Errorf("could not read registry response from node %q: %w", nodeID, err)
	}
	if 12<<20 < len(data) {
		return nil, fmt.Errorf("registry response from node %q exceeds 12 MiB", nodeID)
	}
	var output protocol.RegistryResponse
	if err := json.Unmarshal(data, &output); nil != err {
		return nil, fmt.Errorf("node %q returned an invalid registry response", nodeID)
	}
	if "" != output.Error {
		return nil, fmt.Errorf("registry request through node %q failed: %s", nodeID, output.Error)
	}
	if 100 > output.Status || 599 < output.Status || 8<<20 < len(output.Body) {
		return nil, fmt.Errorf("node %q returned an invalid registry response", nodeID)
	}
	if http.MethodHead == request.Method {
		output.Body = nil
	}
	return &http.Response{
		StatusCode:    output.Status,
		Status:        strconv.Itoa(output.Status) + " " + http.StatusText(output.Status),
		Header:        output.Header,
		Body:          io.NopCloser(bytes.NewReader(output.Body)),
		ContentLength: int64(len(output.Body)),
		Request:       request,
	}, nil
}
