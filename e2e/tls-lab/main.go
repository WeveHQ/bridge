// TLS qualification fixture. Certificates and credentials are disposable test data.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type counters struct {
	Requests   int `json:"requests"`
	Authorized int `json:"authorized"`
	BodyBytes  int `json:"bodyBytes"`
}
type endpoint struct {
	Certificate atomic.Pointer[tls.Certificate] `json:"-"`
	Fingerprint string                          `json:"fingerprint"`
	Counters    counters                        `json:"counters"`
}

var mu sync.Mutex
var endpoints = map[string]*endpoint{}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
func authority() (*x509.Certificate, *ecdsa.PrivateKey) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(err)
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "Simulated tenant CA"}, NotBefore: time.Now().Add(-24 * time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	must(err)
	cert, err := x509.ParseCertificate(der)
	must(err)
	return cert, key
}
func certificate(ca *x509.Certificate, key *ecdsa.PrivateKey, kind string) (*tls.Certificate, string) {
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(err)
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "SplunkServerDefaultCert"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	switch kind {
	case "strict":
		template.DNSNames = []string{"tls-target"}
	case "legacy":
		template.Subject.CommonName = "tls-target"
	case "expired":
		template.NotBefore = time.Now().Add(-2 * time.Hour)
		template.NotAfter = time.Now().Add(-time.Hour)
	case "future":
		template.NotBefore = time.Now().Add(time.Hour)
		template.NotAfter = time.Now().Add(2 * time.Hour)
	case "wrong_usage":
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &leafKey.PublicKey, key)
	must(err)
	sum := sha256.Sum256(der)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: leafKey}, hex.EncodeToString(sum[:])
}
func main() {
	trusted, trustedKey := authority()
	private, privateKey := authority()
	must(os.MkdirAll("/certs", 0755))
	must(os.WriteFile("/certs/tenant-ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: trusted.Raw}), 0644))
	kinds := []string{"strict", "legacy", "generic", "expired", "future", "wrong_usage", "redirect"}
	for i, kind := range kinds {
		ca, key := private, privateKey
		if kind == "strict" || kind == "legacy" {
			ca, key = trusted, trustedKey
		}
		cert, pin := certificate(ca, key, kind)
		entry := &endpoint{Fingerprint: pin}
		entry.Certificate.Store(cert)
		endpoints[kind] = entry
		server := &http.Server{Addr: fmt.Sprintf(":%d", 8443+i), ReadHeaderTimeout: 5 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
		server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) { return entry.Certificate.Load(), nil }}
		server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			authorized := r.Header.Get("Authorization") == "Bearer simulated-connector-token"
			mu.Lock()
			entry.Counters.Requests++
			entry.Counters.BodyBytes += len(body)
			if authorized {
				entry.Counters.Authorized++
			}
			mu.Unlock()
			if !authorized {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if kind == "redirect" {
				http.Redirect(w, r, "http://tls-target:8080/capture", http.StatusTemporaryRedirect)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
		go func() { must(server.ListenAndServeTLS("", "")) }()
	}
	// A cleartext sink proves that a downgrade never receives a second request.
	var sink atomic.Int32
	go func() {
		must(http.ListenAndServe(":8080", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sink.Add(1); w.WriteHeader(204) })))
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"endpoints": endpoints, "downgradeRequests": sink.Load()})
	})
	mux.HandleFunc("POST /rotate", func(w http.ResponseWriter, r *http.Request) {
		cert, pin := certificate(private, privateKey, "generic")
		mu.Lock()
		endpoints["generic"].Certificate.Store(cert)
		endpoints["generic"].Fingerprint = pin
		mu.Unlock()
		w.WriteHeader(204)
	})
	must(http.ListenAndServe(":8082", mux))
}
