package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/artilugio0/proxy-vibes/internal/certs"
)

// TestNewProxy tests the NewProxy constructor
func TestNewProxy(t *testing.T) {
	rootCA, rootKey, _, _, err := certs.GenerateRootCA()
	if err != nil {
		t.Fatalf("Failed to generate Root CA: %v", err)
	}

	p := NewProxy(rootCA, rootKey)

	if p.Client == nil {
		t.Error("Client should be initialized")
	}
	if p.CertCache == nil {
		t.Error("CertCache should be initialized")
	}
	if p.RootCA != rootCA {
		t.Error("RootCA should match the provided rootCA")
	}
	if p.RootKey != rootKey {
		t.Error("RootKey should match the provided rootKey")
	}
	if !p.Client.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify {
		t.Error("TLSClientConfig.InsecureSkipVerify should be true")
	}
}

// TestServeHTTP tests the ServeHTTP method
func TestServeHTTP(t *testing.T) {
	_, rootKey, _, _, err := certs.GenerateRootCA() // Discard rootCA with _
	if err != nil {
		t.Fatalf("Failed to generate Root CA: %v", err)
	}

	p := NewProxy(nil, rootKey) // Pass nil for rootCA since it’s not used
	p.SetRequestModHooks([]ModHook[*http.Request]{
		func(req *http.Request) (*http.Request, error) {
			req.Header.Set("X-Modified", "true")
			return req, nil
		},
	})

	// Mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Modified") != "true" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Write([]byte("Success"))
	}))
	defer server.Close()

	p.Client = &http.Client{
		Transport: &http.Transport{},
	}

	req := httptest.NewRequest("GET", server.URL, nil)
	w := httptest.NewRecorder()

	p.ServeHTTP(w, req)

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	response := string(body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(response, "Success") {
		t.Errorf("Expected response 'Success', got %s", response)
	}
}

// TestCloneRequest tests the cloneRequest function
func TestCloneRequest(t *testing.T) {
	// Test with no body (GET request)
	req := httptest.NewRequest("GET", "http://example.com", nil)
	req.Header.Set("X-Test", "value")
	cloned := cloneRequest(req)

	if cloned == req {
		t.Errorf("cloneRequest should return a new request, got same pointer: %p", req)
	}
	if cloned.URL.String() != req.URL.String() {
		t.Errorf("Expected URL %s, got %s", req.URL.String(), cloned.URL.String())
	}
	if cloned.Header.Get("X-Test") != "value" {
		t.Errorf("Expected header X-Test 'value', got %s", cloned.Header.Get("X-Test"))
	}
	if cloned.Body == nil {
		t.Error("Expected non-nil body (empty reader) for GET request, got nil")
	}
	clonedBody, _ := io.ReadAll(cloned.Body)
	if len(clonedBody) != 0 {
		t.Errorf("Expected empty body for GET request, got %s", string(clonedBody))
	}

	// Test with body (POST request)
	body := "test body"
	reqWithBody := httptest.NewRequest("POST", "http://example.com", strings.NewReader(body))
	clonedWithBody := cloneRequest(reqWithBody)

	if clonedWithBody == reqWithBody {
		t.Errorf("cloneRequest should return a new request, got same pointer: %p", reqWithBody)
	}
	clonedBody, _ = io.ReadAll(clonedWithBody.Body)
	if string(clonedBody) != body {
		t.Errorf("Expected body %s, got %s", body, string(clonedBody))
	}
	if clonedWithBody.ContentLength != int64(len(body)) {
		t.Errorf("Expected ContentLength %d, got %d", len(body), clonedWithBody.ContentLength)
	}

	// Verify original body is preserved
	origBody, _ := io.ReadAll(reqWithBody.Body)
	if string(origBody) != body {
		t.Errorf("Original body should be preserved, got %s", string(origBody))
	}
}

// TestGenerateCert tests the generateCert method
func TestGenerateCert(t *testing.T) {
	_, rootKey, certPEM, _, err := certs.GenerateRootCA() // Discard rootCA with _
	if err != nil {
		t.Fatalf("Failed to generate Root CA: %v", err)
	}
	certBlock, _ := pem.Decode([]byte(certPEM))
	if certBlock == nil {
		t.Fatal("Failed to decode Root CA PEM")
	}
	parsedRootCA, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		t.Fatalf("Failed to parse Root CA: %v", err)
	}

	p := NewProxy(parsedRootCA, rootKey)

	// Test generating a new certificate
	cert1, err := p.generateCert("test.com")
	if err != nil {
		t.Fatalf("Failed to generate certificate: %v", err)
	}
	if cert1 == nil {
		t.Error("Generated certificate is nil")
	}

	// Test caching
	cert2, err := p.generateCert("test.com")
	if err != nil {
		t.Fatalf("Failed to retrieve cached certificate: %v", err)
	}
	if cert1 != cert2 {
		t.Errorf("Expected cached certificate to be the same, got different pointers: %p vs %p", cert1, cert2)
	}

	// Test concurrent access
	var wg sync.WaitGroup
	numGoroutines := 10
	wg.Add(numGoroutines)
	certs := make([]*tls.Certificate, numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			cert, err := p.generateCert("test.com")
			if err != nil {
				t.Errorf("Goroutine %d failed: %v", idx, err)
			}
			certs[idx] = cert
		}(i)
	}
	wg.Wait()

	for i := 1; i < numGoroutines; i++ {
		if certs[0] != certs[i] {
			t.Errorf("Expected all certificates to be the same (cached), got different at index %d: %p vs %p", i, certs[0], certs[i])
		}
	}
}

func TestServeHTTPOutOfScope(t *testing.T) {
	_, rootKey, _, _, err := certs.GenerateRootCA()
	if err != nil {
		t.Fatalf("Failed to generate Root CA: %v", err)
	}

	p := NewProxy(nil, rootKey)
	p.InScopeFunc = func(req *http.Request) bool {
		return false // Out of scope
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Success"))
	}))
	defer server.Close()

	p.Client = &http.Client{
		Transport: &http.Transport{},
	}

	req := httptest.NewRequest("GET", server.URL, nil)
	w := httptest.NewRecorder()

	p.ServeHTTP(w, req)

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	response := string(body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(response, "Success") {
		t.Errorf("Expected response 'Success', got %s", response)
	}
}

// TestServeHTTPWithID tests ID accessibility and consistency in ServeHTTP
func TestServeHTTPWithID(t *testing.T) {
	_, rootKey, _, _, err := certs.GenerateRootCA()
	if err != nil {
		t.Fatalf("Failed to generate Root CA: %v", err)
	}

	p := NewProxy(nil, rootKey)
	var requestID, responseID string

	var wg sync.WaitGroup
	wg.Add(2)
	// Hook to capture request ID
	p.RequestInPipeline = newReadOnlyPipeline([]ReadOnlyHook[*http.Request]{
		func(req *http.Request) error {
			defer wg.Done()
			requestID = GetRequestID(req)
			return nil
		},
	})
	// Hook to capture response ID
	p.ResponseInPipeline = newReadOnlyPipeline([]ReadOnlyHook[*http.Response]{
		func(resp *http.Response) error {
			defer wg.Done()
			responseID = GetResponseID(resp)
			return nil
		},
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Success"))
	}))
	defer server.Close()

	p.Client = &http.Client{
		Transport: &http.Transport{},
	}

	req := httptest.NewRequest("GET", server.URL, nil)
	w := httptest.NewRecorder()

	p.ServeHTTP(w, req)

	wg.Wait()

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	response := string(body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(response, "Success") {
		t.Errorf("Expected response 'Success', got %s", response)
	}

	// Check IDs are accessible
	if requestID == "" {
		t.Errorf("Expected request ID to be set, got empty string")
	}
	if responseID == "" {
		t.Errorf("Expected response ID to be set, got empty string")
	}

	// Check request and response have the same ID
	if requestID != responseID {
		t.Errorf("Expected request ID %q to match response ID %q", requestID, responseID)
	}
}

// TestServeHTTPDifferentIDs tests that different requests have unique IDs
func TestServeHTTPDifferentIDs(t *testing.T) {
	_, rootKey, _, _, err := certs.GenerateRootCA()
	if err != nil {
		t.Fatalf("Failed to generate Root CA: %v", err)
	}

	p := NewProxy(nil, rootKey)
	ids := make([]string, 2)

	// Hook to capture request IDs
	p.RequestInPipeline = newReadOnlyPipeline([]ReadOnlyHook[*http.Request]{
		func(req *http.Request) error {
			if len(ids[0]) == 0 {
				ids[0] = GetRequestID(req)
			} else {
				ids[1] = GetRequestID(req)
			}
			return nil
		},
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Success"))
	}))
	defer server.Close()

	p.Client = &http.Client{
		Transport: &http.Transport{},
	}

	// First request
	req1 := httptest.NewRequest("GET", server.URL+"/1", nil)
	w1 := httptest.NewRecorder()
	p.ServeHTTP(w1, req1)

	// Second request
	req2 := httptest.NewRequest("GET", server.URL+"/2", nil)
	w2 := httptest.NewRecorder()
	p.ServeHTTP(w2, req2)

	// Check responses
	for i, w := range []*httptest.ResponseRecorder{w1, w2} {
		resp := w.Result()
		body, _ := io.ReadAll(resp.Body)
		response := string(body)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Request %d: Expected status 200, got %d", i+1, resp.StatusCode)
		}
		if !strings.Contains(response, "Success") {
			t.Errorf("Request %d: Expected response 'Success', got %s", i+1, response)
		}
	}

	// Check IDs are accessible and different
	if ids[0] == "" || ids[1] == "" {
		t.Errorf("Expected all request IDs to be set, got %v", ids)
	}
	if ids[0] == ids[1] {
		t.Errorf("Expected different IDs, got %q for both requests", ids[0])
	}
}

// TestHandleConnectWithID tests ID accessibility and consistency in HandleConnect
func TestHandleConnectWithID(t *testing.T) {
	rootCA, rootKey, certPEM, _, err := certs.GenerateRootCA()
	if err != nil {
		t.Fatalf("Failed to generate Root CA: %v", err)
	}
	certBlock, _ := pem.Decode([]byte(certPEM))
	if certBlock == nil {
		t.Fatal("Failed to decode Root CA PEM")
	}
	parsedRootCA, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		t.Fatalf("Failed to parse Root CA: %v", err)
	}

	p := NewProxy(rootCA, rootKey)
	var requestID, responseID string

	// Hook to capture request ID
	p.RequestInPipeline = newReadOnlyPipeline([]ReadOnlyHook[*http.Request]{
		func(req *http.Request) error {
			requestID = GetRequestID(req)
			return nil
		},
	})
	// Hook to capture response ID
	p.ResponseInPipeline = newReadOnlyPipeline([]ReadOnlyHook[*http.Response]{
		func(resp *http.Response) error {
			responseID = GetResponseID(resp)
			return nil
		},
	})

	serverCert, err := certs.GenerateCert([]string{"localhost", "127.0.0.1"}, rootCA, rootKey)
	if err != nil {
		t.Fatalf("Failed to generate server certificate: %v", err)
	}
	destServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Success"))
	}))
	destServer.TLS.Certificates = []tls.Certificate{*serverCert}
	defer destServer.Close()

	destAddr := destServer.Listener.Addr().String()

	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.HandleConnect(w, r)
	}))
	defer proxyServer.Close()

	proxyURL, _ := url.Parse("http://" + proxyServer.Listener.Addr().String())
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs: getRootCAPool(parsedRootCA),
			},
		},
	}

	resp, err := client.Get("https://localhost:" + destAddr[strings.LastIndex(destAddr, ":")+1:])
	if err != nil {
		t.Fatalf("Failed to perform request through proxy: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}
	response := string(body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(response, "Success") {
		t.Errorf("Expected response 'Success', got %s", response)
	}

	// Check IDs are accessible
	if requestID == "" {
		t.Errorf("Expected request ID to be set, got empty string")
	}
	if responseID == "" {
		t.Errorf("Expected response ID to be set, got empty string")
	}

	// Check request and response have the same ID
	if requestID != responseID {
		t.Errorf("Expected request ID %q to match response ID %q", requestID, responseID)
	}
}

// TestHandleConnectDifferentIDs tests that different requests in HandleConnect have unique IDs
func TestHandleConnectDifferentIDs(t *testing.T) {
	rootCA, rootKey, certPEM, _, err := certs.GenerateRootCA()
	if err != nil {
		t.Fatalf("Failed to generate Root CA: %v", err)
	}
	certBlock, _ := pem.Decode([]byte(certPEM))
	if certBlock == nil {
		t.Fatal("Failed to decode Root CA PEM")
	}
	parsedRootCA, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		t.Fatalf("Failed to parse Root CA: %v", err)
	}

	p := NewProxy(rootCA, rootKey)
	ids := make([]string, 2)
	var mu sync.Mutex
	requestCount := 0

	// Hook to capture request IDs

	p.RequestInPipeline = newReadOnlyPipeline([]ReadOnlyHook[*http.Request]{
		func(req *http.Request) error {
			mu.Lock()
			defer mu.Unlock()
			if requestCount < 2 {
				ids[requestCount] = GetRequestID(req)
				requestCount++
			}
			return nil
		},
	})

	serverCert, err := certs.GenerateCert([]string{"localhost", "127.0.0.1"}, rootCA, rootKey)
	if err != nil {
		t.Fatalf("Failed to generate server certificate: %v", err)
	}
	destServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Success"))
	}))
	destServer.TLS.Certificates = []tls.Certificate{*serverCert}
	defer destServer.Close()

	destAddr := destServer.Listener.Addr().String()

	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.HandleConnect(w, r)
	}))
	defer proxyServer.Close()

	proxyURL, _ := url.Parse("http://" + proxyServer.Listener.Addr().String())
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs: getRootCAPool(parsedRootCA),
			},
		},
	}

	// First request
	resp1, err := client.Get("https://localhost:" + destAddr[strings.LastIndex(destAddr, ":")+1:] + "/1")
	if err != nil {
		t.Fatalf("First request failed: %v", err)
	}
	defer resp1.Body.Close()

	// Second request
	resp2, err := client.Get("https://localhost:" + destAddr[strings.LastIndex(destAddr, ":")+1:] + "/2")
	if err != nil {
		t.Fatalf("Second request failed: %v", err)
	}
	defer resp2.Body.Close()

	// Check responses
	for i, resp := range []*http.Response{resp1, resp2} {
		body, _ := io.ReadAll(resp.Body)
		response := string(body)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Request %d: Expected status 200, got %d", i+1, resp.StatusCode)
		}
		if !strings.Contains(response, "Success") {
			t.Errorf("Request %d: Expected response 'Success', got %s", i+1, response)
		}
	}

	// Check IDs are accessible and different
	if ids[0] == "" || ids[1] == "" {
		t.Errorf("Expected all request IDs to be set, got %v", ids)
	}
	if ids[0] == ids[1] {
		t.Errorf("Expected different IDs, got %q for both requests", ids[0])
	}
}
