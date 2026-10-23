package accesstoken

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
)

func pemBlock(t *testing.T, typ string, der []byte) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

func TestParsePublicKeyPEM(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	good := pemBlock(t, "PUBLIC KEY", der)
	got, err := ParsePublicKeyPEM(good)
	if err != nil || !bytes.Equal(got, pub) {
		t.Fatalf("(%x, %v)", got, err)
	}

	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecDER, _ := x509.MarshalPKIXPublicKey(&ec.PublicKey)
	privDER, _ := x509.MarshalPKCS8PrivateKey(priv)
	for name, data := range map[string][]byte{
		"empty":            nil,
		"not PEM":          []byte("ed25519 public key"),
		"private key":      pemBlock(t, "PRIVATE KEY", privDER),
		"wrong PEM type":   pemBlock(t, "ED25519 PUBLIC KEY", der),
		"P-256 public key": pemBlock(t, "PUBLIC KEY", ecDER),
		"garbage DER":      pemBlock(t, "PUBLIC KEY", []byte{1, 2, 3}),
		"trailing data":    append(append([]byte{}, good...), "x"...),
		"two keys":         append(append([]byte{}, good...), good...),
	} {
		if k, err := ParsePublicKeyPEM(data); !errors.Is(err, ErrInvalidConfig) || k != nil {
			t.Errorf("%s: (%v, %v), want ErrInvalidConfig", name, k, err)
		}
	}
}
