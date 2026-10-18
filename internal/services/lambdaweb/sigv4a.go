package lambdaweb

// sigv4a.go — verification of SigV4a (AWS4-ECDSA-P256-SHA256) signatures.
//
// SigV4a signs the same canonical request as SigV4 (X-Amz-Region-Set is one of
// the signed headers), under a credential scope without a Region:
//
//	string to sign = "AWS4-ECDSA-P256-SHA256\n{X-Amz-Date}\n{date}/{service}/aws4_request\n"
//	                 + hex(SHA-256(canonical request))
//	signature      = hex(ASN.1 DER ECDSA-P256 signature of SHA-256(string to sign))
//
// The signing key is not the secret itself but an ECDSA key derived from the
// access key id and secret (a NIST SP 800-108 counter-mode HMAC-SHA256 KDF,
// FIPS 186-4 B.4.2 candidate search), so the verifier — which holds the secret
// — derives the public key. The derivation is checked against the known answer
// in the AWS SDK's own test (see TestDeriveSigV4aKey_matchesSDKKnownAnswer).
// https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv-signing-requests.html

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"net/http"
	"strings"

	"github.com/overcast-sh/overcast/internal/middleware"
)

// deriveSigV4aKey derives the ECDSA P-256 key SigV4a signs with.
func deriveSigV4aKey(accessKey, secret string) *ecdsa.PrivateKey {
	curve := elliptic.P256()
	nMinus2 := new(big.Int).Sub(curve.Params().N, big.NewInt(2)).FillBytes(make([]byte, 32))
	inputKey := []byte("AWS4A" + secret)

	for counter := 1; counter <= 0xFF; counter++ {
		// fixed input = label || 0x00 || (access key || counter) || bit length
		fixed := make([]byte, 0, len(authAlgSigV4a)+1+len(accessKey)+1+4)
		fixed = append(fixed, authAlgSigV4a...)
		fixed = append(fixed, 0)
		fixed = append(fixed, accessKey...)
		fixed = append(fixed, byte(counter))
		fixed = binary.BigEndian.AppendUint32(fixed, 256)

		mac := hmac.New(sha256.New, inputKey)
		_ = binary.Write(mac, binary.BigEndian, int32(1)) // one 256-bit block
		_, _ = mac.Write(fixed)
		candidate := mac.Sum(nil)

		// candidate < N-2, both 32-byte big endian.
		if bytes.Compare(candidate, nMinus2) < 0 {
			d := new(big.Int).SetBytes(candidate)
			d.Add(d, big.NewInt(1))
			priv := &ecdsa.PrivateKey{D: d}
			priv.PublicKey.Curve = curve
			ek, err := ecdh.P256().NewPrivateKey(d.FillBytes(make([]byte, 32)))
			if err != nil {
				return nil
			}
			pub := ek.PublicKey().Bytes() // 0x04 || X || Y
			priv.PublicKey.X = new(big.Int).SetBytes(pub[1:33])
			priv.PublicKey.Y = new(big.Int).SetBytes(pub[33:])
			return priv
		}
	}
	return nil
}

// verifySigV4a checks s's signature on r under the key derived from secret.
// canonicalPath is the canonical URI the client signed.
func verifySigV4a(r *http.Request, s signature, secret, canonicalPath string) bool {
	if !signedHeaderPresent(s.signedHeaders, strings.ToLower(headerRegionSet)) {
		return false // the Region set must be signed or it could be swapped
	}
	der, err := hex.DecodeString(s.sig)
	if err != nil {
		return false
	}
	priv := deriveSigV4aKey(s.accessKey, secret)
	if priv == nil {
		return false
	}
	canonical := middleware.SigV4CanonicalRequest(r, strings.Split(s.signedHeaders, ";"), s.payloadHash, canonicalPath)
	sum := sha256.Sum256([]byte(canonical))
	stringToSign := authAlgSigV4a + "\n" + s.amzDate + "\n" + s.date + "/" + s.service + "/aws4_request\n" + hexOf(sum[:])
	digest := sha256.Sum256([]byte(stringToSign))
	return ecdsa.VerifyASN1(&priv.PublicKey, digest[:], der)
}

func hexOf(b []byte) string { return hex.EncodeToString(b) }
