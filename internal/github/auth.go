package github

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"time"
)

// GetInstallationToken exchanges a GitHub App private key + installation ID for
// a short-lived installation access token. The token is suitable for both the
// Checks API and the Commit Statuses API when the app has those permissions.
func GetInstallationToken(appID, installationID int64, pemBytes []byte) (string, error) {
	jwt, err := generateAppJWT(appID, pemBytes)
	if err != nil {
		return "", fmt.Errorf("generate jwt: %w", err)
	}

	apiURL := fmt.Sprintf("https://api.github.com/app/installations/%d/access_tokens", installationID)
	req, err := http.NewRequest(http.MethodPost, apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return "", fmt.Errorf("github installation token %s: %s", resp.Status, buf.String())
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode installation token response: %w", err)
	}
	if result.Token == "" {
		return "", fmt.Errorf("installation token response missing token field")
	}
	return result.Token, nil
}

// generateAppJWT creates a signed RS256 JWT for GitHub App authentication.
// The token is valid for up to 10 minutes (GitHub's max for app JWTs).
func generateAppJWT(appID int64, pemBytes []byte) (string, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return "", fmt.Errorf("no PEM block found in private key")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		// Try PKCS8 as well (modern key format).
		k, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err2 != nil {
			return "", fmt.Errorf("parse private key (PKCS1: %v, PKCS8: %v)", err, err2)
		}
		var ok bool
		key, ok = k.(*rsa.PrivateKey)
		if !ok {
			return "", fmt.Errorf("private key is not RSA")
		}
	}

	header := `{"alg":"RS256","typ":"JWT"}`
	now := time.Now()
	payload := fmt.Sprintf(`{"iat":%d,"exp":%d,"iss":"%d"}`,
		now.Unix(), now.Add(10*time.Minute).Unix(), appID)

	h := base64.RawURLEncoding.EncodeToString([]byte(header))
	p := base64.RawURLEncoding.EncodeToString([]byte(payload))

	message := h + "." + p
	hasher := sha256.New()
	hasher.Write([]byte(message))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hasher.Sum(nil))
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	s := base64.RawURLEncoding.EncodeToString(sig)

	return h + "." + p + "." + s, nil
}
