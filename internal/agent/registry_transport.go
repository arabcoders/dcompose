package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/arabcoders/dcompose/internal/protocol"
)

const registryRequestLimit = 1 << 20
const registryResponseLimit = 8 << 20

func (a *agent) registryRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	var input protocol.RegistryRequest
	if err := decodeBody(r, &input); nil != err {
		return err
	}
	output, err := relayRegistry(ctx, input, http.DefaultTransport)
	if nil != err {
		return err
	}
	respond(w, http.StatusOK, output)
	return nil
}

func relayRegistry(ctx context.Context, input protocol.RegistryRequest, transport http.RoundTripper) (protocol.RegistryResponse, error) {
	switch input.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost:
	default:
		return protocol.RegistryResponse{}, fault(http.StatusBadRequest, "Registry requests support only GET, HEAD and POST.")
	}
	address, err := url.Parse(input.URL)
	if nil != err || "https" != address.Scheme || "" == address.Host || nil != address.User {
		return protocol.RegistryResponse{}, fault(http.StatusBadRequest, "Registry requests require an HTTPS URL without user credentials.")
	}
	if registryRequestLimit < len(input.Body) {
		return protocol.RegistryResponse{}, fault(http.StatusBadRequest, "Registry request body exceeds 1 MiB.")
	}
	request, err := http.NewRequestWithContext(ctx, input.Method, input.URL, bytes.NewReader(input.Body))
	if nil != err {
		return protocol.RegistryResponse{}, fault(http.StatusBadRequest, "Invalid registry request URL.")
	}
	if nil != input.Header {
		request.Header = input.Header.Clone()
	}
	response, err := transport.RoundTrip(request)
	if nil != err {
		if nil != response && nil != response.Body {
			_ = response.Body.Close()
		}
		return protocol.RegistryResponse{Error: registryTransportError(err, input)}, nil
	}
	defer func() { _ = response.Body.Close() }()
	output := protocol.RegistryResponse{Status: response.StatusCode, Header: response.Header.Clone()}
	if http.MethodHead == input.Method {
		return output, nil
	}
	output.Body, err = io.ReadAll(io.LimitReader(response.Body, registryResponseLimit+1))
	if nil != err {
		return protocol.RegistryResponse{}, fault(http.StatusBadGateway, "Could not read registry response: "+registryTransportError(err, input))
	}
	if registryResponseLimit < len(output.Body) {
		return protocol.RegistryResponse{}, fault(http.StatusBadGateway, "Registry response body exceeds 8 MiB.")
	}
	return output, nil
}

// Remove URL wrappers and request secrets before a transport failure leaves the node.
func registryTransportError(err error, input protocol.RegistryRequest) string {
	if addressError, ok := errors.AsType[*url.Error](err); ok {
		err = addressError.Err
	}
	message := err.Error()
	secrets := []string{input.URL, string(input.Body)}
	if address, parseErr := url.Parse(input.URL); nil == parseErr {
		for _, values := range address.Query() {
			secrets = append(secrets, values...)
		}
	}
	for _, value := range input.Header.Values("Authorization") {
		secrets = append(secrets, value)
		if _, token, ok := strings.Cut(value, " "); ok {
			secrets = append(secrets, token)
		}
	}
	if form, parseErr := url.ParseQuery(string(input.Body)); nil == parseErr {
		for _, values := range form {
			secrets = append(secrets, values...)
		}
	}
	for _, secret := range secrets {
		if "" != secret {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
			message = strings.ReplaceAll(message, url.QueryEscape(secret), "[REDACTED]")
		}
	}
	return message
}
