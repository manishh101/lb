# Package `tlsutil`

**Import path:** `intelligent-lb/internal/tlsutil`

The `tlsutil` package provides TLS certificate utilities for the load balancer. Its sole function generates a self-signed X.509 certificate and private key, enabling HTTPS entrypoints without manual certificate setup. It is designed for **development and testing** environments where a trusted CA certificate is not available.

---

## File Structure

```
tlsutil/
└── certs.go  — GenerateSelfSigned function
```

---

## `certs.go` — Self-Signed Certificate Generator

### `GenerateSelfSigned(certPath, keyPath string) error`

```go
func GenerateSelfSigned(certPath, keyPath string) error
```

Generates a self-signed TLS certificate using **ECDSA P-256** and writes two PEM-encoded files:
- `certPath`: contains the certificate (`-----BEGIN CERTIFICATE-----`)
- `keyPath`: contains the private key (`-----BEGIN EC PRIVATE KEY-----`)

---

### Idempotency Check

```go
if _, err := os.Stat(certPath); err == nil {
    if _, err := os.Stat(keyPath); err == nil {
        log.Printf("[TLS] Certificate files already exist (%s, %s), skipping generation", certPath, keyPath)
        return nil  // no-op
    }
}
```

If **both** files already exist (checked via `os.Stat`, which does not open the files), the function returns immediately without modifying them. This means you can call `GenerateSelfSigned` every time the load balancer starts without regenerating certificates (which would invalidate browser trust exceptions added for the old certificate).

---

### Key Type: ECDSA P-256

```go
privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
```

ECDSA (Elliptic Curve Digital Signature Algorithm) with the P-256 curve is chosen over RSA because:
- **Smaller key sizes**: 256-bit ECDSA ≈ 3072-bit RSA security strength.
- **Faster handshakes**: Fewer CPU operations per TLS handshake (~50% faster than RSA-2048 in benchmarks).
- **Smaller certificates**: Reduces TLS handshake payload size.
- **Modern standard**: Recommended by NIST, preferred by TLS 1.3.

`rand.Reader` is Go's interface to the OS cryptographically secure random number source (`/dev/urandom` on Linux). It is the only correct source for key generation.

---

### Certificate Template

```go
template := x509.Certificate{
    SerialNumber: randomSerial(),  // random 128-bit serial
    Subject: pkix.Name{
        Organization: []string{"Intelligent Load Balancer"},
        CommonName:   "localhost",
    },
    NotBefore:             time.Now(),
    NotAfter:              time.Now().Add(365 * 24 * time.Hour),  // 1 year
    KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
    ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
    BasicConstraintsValid: true,

    // Subject Alternative Names (SANs)
    DNSNames: []string{"localhost", "loadbalancer"},
    IPAddresses: []net.IP{
        net.ParseIP("127.0.0.1"),
        net.ParseIP("::1"),
    },
}
```

#### Certificate Properties Explained

| Property | Value | Why |
|---|---|---|
| `SerialNumber` | Random 128-bit integer | Uniqueness per RFC 5280; prevents certificate fingerprinting |
| `Organization` | `"Intelligent Load Balancer"` | Identifies issuer in browser certificate details |
| `CommonName` | `"localhost"` | Legacy field; modern clients use SANs instead |
| `NotBefore` | `time.Now()` | Valid immediately |
| `NotAfter` | 1 year from now | Long enough for dev use; short enough for security hygiene |
| `KeyUsage` | `DigitalSignature + KeyEncipherment` | Required for TLS: signing (ECDSA) + encrypting session key (RSA-based cipher suites) |
| `ExtKeyUsage` | `ServerAuth` | Marks this as a **server certificate** (not a CA or client cert) |
| `BasicConstraintsValid` | `true` | Required field for X.509 v3 certificates |
| `DNSNames` | `localhost`, `loadbalancer` | SANs for host name validation |
| `IPAddresses` | `127.0.0.1`, `::1` | SANs for IP address validation |

**Why `loadbalancer` DNS SAN?** In Docker Compose deployments, services communicate using the service name (e.g., `https://loadbalancer:8443`). Including `loadbalancer` as a SAN allows internal Docker service-to-service TLS communication without "hostname mismatch" errors.

---

### Random Serial Number

```go
func randomSerial() *big.Int {
    serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
    return serial
}
```

Generates a random 128-bit integer as the certificate serial number. `new(big.Int).Lsh(big.NewInt(1), 128)` creates 2^128 (the exclusive upper bound). RFC 5280 requires certificate serial numbers to be unique within a CA; using a random 128-bit number guarantees this with overwhelming probability.

---

### Certificate Creation and Signing

```go
certDER, err := x509.CreateCertificate(
    rand.Reader,         // source of randomness for ECDSA signing
    &template,           // the certificate template
    &template,           // the issuer certificate (self-signed: template signs itself)
    &privateKey.PublicKey, // the public key to certify
    privateKey,          // the signing private key (same key for self-signed)
)
```

`x509.CreateCertificate` returns the certificate in **DER format** (binary). This must be PEM-encoded (base64 wrapped in header/footer) before writing to file.

---

### PEM Encoding and File Writing

```go
// Write certificate
certFile, err := os.Create(certPath)
pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})

// Write private key
keyFile, err := os.Create(keyPath)
keyDER, err := x509.MarshalECPrivateKey(privateKey)
pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
```

`os.Create` truncates the file if it exists (but the idempotency check at the start prevents reaching this point if both files already exist).

`x509.MarshalECPrivateKey` serializes the ECDSA private key to DER using the SEC1/RFC 5915 format (the standard for EC private keys, as opposed to PKCS#8 which Go uses for general private keys).

---

## Full Usage Example

```go
// At startup in main.go, before starting HTTPS entrypoints:
certPath := "server.crt"
keyPath  := "server.key"

if err := tlsutil.GenerateSelfSigned(certPath, keyPath); err != nil {
    log.Fatalf("Failed to generate TLS certificate: %v", err)
}

// Then use the files in entrypoint config:
{
  "entryPoints": {
    "websecure": {
      "address": ":8443",
      "protocol": "https",
      "tls": { "cert_file": "server.crt", "key_file": "server.key" }
    }
  }
}
```

---

## Security Considerations

> **⚠ Development Only**
> Self-signed certificates are **not trusted** by browsers or HTTPS clients by default. This package is only for development and testing.
>
> For production, use certificates from a trusted CA:
> - **Let's Encrypt**: Free DV certificates via ACME protocol
> - **Internal CA**: Use your organization's CA for internal services
> - **Certificate Manager**: Kubernetes `cert-manager`, AWS ACM, GCP Certificate Manager

### Additional Notes
- Cert and key files are written with mode `0644`. For production-grade security, the key file should use `0600` (owner-read-only). This is a limitation of using `os.Create`.
- The private key is **never** encrypted (no passphrase). Again, acceptable for development; production keys should be stored in a secrets manager.
- There is no certificate rotation logic — the cert is valid for 1 year. If the cert expires, delete the files and restart the load balancer to regenerate.

---

## Dependencies

| Package | Role |
|---|---|
| `crypto/ecdsa` | ECDSA key pair generation and marshaling |
| `crypto/elliptic` | P-256 curve parameters |
| `crypto/rand` | OS cryptographically secure random source |
| `crypto/x509` `crypto/x509/pkix` | Certificate template, DER encoding, subject naming |
| `encoding/pem` | PEM block encoding (base64 with header/footer) |
| `math/big` | `*big.Int` for 128-bit random serial number |
| `net` | `net.ParseIP` for SAN IP addresses |
| `os` | File creation and existence checking via `os.Stat` |
| `time` | Certificate validity period (`NotBefore`, `NotAfter`) |
| `log` | Informational startup logging |
