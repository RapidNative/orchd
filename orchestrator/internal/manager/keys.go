package manager

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
)

// tinbaseDemoKeyExp is tinbase's DEMO_KEY_EXP (src/jwt.ts): the expiry its
// deterministic keys carry, the same value Supabase's local demo keys use.
const tinbaseDemoKeyExp = 1983812996

// tinbaseKeyClaims are the claims of tinbase's deterministic keys, in the order
// tinbase serialises them, so the tokens are byte-identical to `tinbase keys`.
type tinbaseKeyClaims struct {
	Iss  string `json:"iss"`
	Role string `json:"role"`
	Exp  int64  `json:"exp"`
}

// signTinbaseKeys derives a project's anon and service_role keys from its JWT
// secret exactly as `tinbase keys` does (deriveApiKeys, 'deterministic'): HS256
// over {"iss":"supabase-demo","role":<role>,"exp":DEMO_KEY_EXP} with the raw
// secret as the HMAC key.
//
// It used to run `docker run <image> tinbase keys` for every new database. On a
// busy box (LRU evictions backing up projects at the same time) that container
// hung until its 3-minute timeout, so a re-created project took 3+ minutes,
// RapidNative's reprovision webhook gave up first and answered 500, and the
// customer's preview showed no data (Twin Keys, 2026-11-17; 8 kills in Nov).
// Signing two JWTs needs no container.
func signTinbaseKeys(secret string) (anon, svc string) {
	return signTinbaseKey(secret, "anon"), signTinbaseKey(secret, "service_role")
}

func signTinbaseKey(secret, role string) string {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, _ := json.Marshal(tinbaseKeyClaims{Iss: "supabase-demo", Role: role, Exp: tinbaseDemoKeyExp})
	data := header + "." + enc.EncodeToString(claims)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(data))
	return data + "." + enc.EncodeToString(mac.Sum(nil))
}
