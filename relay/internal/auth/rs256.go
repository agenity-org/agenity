package auth

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
)

// verifyRS256 is the SHA256-with-RSA verification of a JWT signature.
// Pulled out into its own file so the alg can be swapped (RS384, RS512,
// ES256 via ECDSA) in a follow-up without touching auth.go.
func verifyRS256(pub *rsa.PublicKey, data, sig []byte) error {
	h := sha256.Sum256(data)
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], sig)
}
