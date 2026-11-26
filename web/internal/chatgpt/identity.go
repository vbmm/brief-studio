package chatgpt

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"
)

type signingKeys struct {
	Keys []signingKey `json:"keys"`
}

type signingKey struct {
	Type      string `json:"kty"`
	ID        string `json:"kid"`
	Algorithm string `json:"alg"`
	Use       string `json:"use"`
	Modulus   string `json:"n"`
	Exponent  string `json:"e"`
}

type idClaims struct {
	Issuer        string          `json:"iss"`
	Audience      json.RawMessage `json:"aud"`
	Subject       string          `json:"sub"`
	Nonce         string          `json:"nonce"`
	Authorized    string          `json:"azp"`
	Expires       int64           `json:"exp"`
	NotBefore     int64           `json:"nbf"`
	Name          string          `json:"name"`
	Email         string          `json:"email"`
	EmailVerified bool            `json:"email_verified"`
}

func verifyIdentity(raw, clientID, nonce string, keys signingKeys, now time.Time) (Profile, error) {
	if len(raw) == 0 || len(raw) > 32<<10 {
		return Profile{}, ErrIdentity
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Profile{}, ErrIdentity
	}
	header, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		return Profile{}, ErrIdentity
	}
	var untrustedHeader struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	if json.Unmarshal(header, &untrustedHeader) != nil || untrustedHeader.Algorithm != "RS256" ||
		untrustedHeader.KeyID == "" {
		return Profile{}, ErrIdentity
	}
	key := keys.rsaKey(untrustedHeader.KeyID)
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if key == nil || err != nil || rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) != nil {
		return Profile{}, ErrIdentity
	}
	claimsJSON, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		return Profile{}, ErrIdentity
	}
	var claims idClaims
	if json.Unmarshal(claimsJSON, &claims) != nil || claims.Issuer != issuer ||
		claims.Subject == "" || claims.Expires <= now.Unix() || claims.NotBefore > now.Unix()+60 ||
		subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(nonce)) != 1 ||
		!validAudience(claims.Audience, claims.Authorized, clientID) {
		return Profile{}, ErrIdentity
	}
	profile := Profile{Name: boundedLabel(claims.Name, 200)}
	if claims.EmailVerified {
		profile.Email = boundedLabel(claims.Email, 320)
	}
	return profile, nil
}

func validAudience(raw json.RawMessage, authorized, clientID string) bool {
	if authorized != "" && authorized != clientID {
		return false
	}
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == clientID
	}
	var multiple []string
	if json.Unmarshal(raw, &multiple) != nil || len(multiple) == 0 ||
		len(multiple) > 1 && authorized != clientID {
		return false
	}
	for _, aud := range multiple {
		if aud == clientID {
			return true
		}
	}
	return false
}

func (keys signingKeys) rsaKey(id string) *rsa.PublicKey {
	var selected *signingKey
	for i := range keys.Keys {
		key := &keys.Keys[i]
		if key.ID != id {
			continue
		}
		if selected != nil {
			return nil
		}
		selected = key
	}
	if selected == nil || selected.Type != "RSA" ||
		selected.Algorithm != "" && selected.Algorithm != "RS256" ||
		selected.Use != "" && selected.Use != "sig" {
		return nil
	}
	n, err := base64.RawURLEncoding.Strict().DecodeString(selected.Modulus)
	if err != nil || len(n) < 256 || len(n) > 1024 {
		return nil
	}
	e, err := base64.RawURLEncoding.Strict().DecodeString(selected.Exponent)
	if err != nil || len(e) == 0 || len(e) > 4 {
		return nil
	}
	modulus := new(big.Int).SetBytes(n)
	exponent := new(big.Int).SetBytes(e).Int64()
	if modulus.BitLen() < 2048 || exponent < 3 || exponent > 1<<31-1 || exponent%2 == 0 {
		return nil
	}
	return &rsa.PublicKey{N: modulus, E: int(exponent)}
}

func boundedLabel(value string, max int) string {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > max {
		return ""
	}
	return value
}
