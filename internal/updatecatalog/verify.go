package updatecatalog

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"time"
)

const (
	defaultClockSkew   = 5 * time.Minute
	defaultMaxLifetime = 48 * time.Hour
	maximumClockSkew   = time.Hour
	maximumLifetime    = 7 * 24 * time.Hour
)

func ParseAndVerify(data []byte, policy VerifyPolicy) (VerifiedRelease, error) {
	manifest, signatures, payload, err := parseEnvelope(data)
	if err != nil {
		return VerifiedRelease{}, err
	}
	normalizedPolicy, trusted, err := normalizePolicy(policy)
	if err != nil {
		return VerifiedRelease{}, err
	}
	if err := enforceManifestPolicy(manifest, normalizedPolicy); err != nil {
		return VerifiedRelease{}, err
	}
	digest := signingDigestForPayload(payload)
	validKeyIDs := verifyThreshold(signatures, digest, manifest, normalizedPolicy, trusted)
	if len(validKeyIDs) < normalizedPolicy.Trust.Threshold {
		return VerifiedRelease{}, fmt.Errorf("%w: got %d valid signatures, require %d", ErrInsufficientSignatures, len(validKeyIDs), normalizedPolicy.Trust.Threshold)
	}
	payloadDigest := sha256.Sum256(payload)
	payloadHex := hex.EncodeToString(payloadDigest[:])
	alreadySeen, err := checkState(manifest, payloadHex, normalizedPolicy.Current)
	if err != nil {
		return VerifiedRelease{}, err
	}
	artifact, err := SelectArtifact(manifest, normalizedPolicy.OS, normalizedPolicy.Arch)
	if err != nil {
		return VerifiedRelease{}, err
	}
	return VerifiedRelease{
		Manifest:      cloneManifest(manifest),
		Artifact:      artifact,
		PayloadSHA256: payloadHex,
		ValidKeyIDs:   append([]string(nil), validKeyIDs...),
		AlreadySeen:   alreadySeen,
	}, nil
}

func normalizePolicy(policy VerifyPolicy) (VerifyPolicy, map[string]TrustedKey, error) {
	out := policy
	if out.Now.IsZero() {
		out.Now = time.Now().UTC()
	}
	if out.ClockSkew == 0 {
		out.ClockSkew = defaultClockSkew
	}
	if out.MaxLifetime == 0 {
		out.MaxLifetime = defaultMaxLifetime
	}
	if out.ClockSkew < 0 || out.ClockSkew > maximumClockSkew {
		return VerifyPolicy{}, nil, invalid("clock skew must be between zero and %s", maximumClockSkew)
	}
	if out.MaxLifetime <= 0 || out.MaxLifetime > maximumLifetime {
		return VerifyPolicy{}, nil, invalid("max lifetime must be between zero and %s", maximumLifetime)
	}
	if !namePattern.MatchString(out.Channel) || !namePattern.MatchString(out.OS) || !namePattern.MatchString(out.Arch) {
		return VerifyPolicy{}, nil, invalid("channel and platform policy are required")
	}
	if _, err := parseSemanticVersion(out.RunnerVersion); err != nil {
		return VerifyPolicy{}, nil, fmt.Errorf("%w: invalid current runner version: %v", ErrVersionGate, err)
	}
	if _, err := parseSemanticVersion(out.UpdaterVersion); err != nil {
		return VerifyPolicy{}, nil, fmt.Errorf("%w: invalid current updater version: %v", ErrVersionGate, err)
	}
	if out.Trust.Threshold <= 0 || out.Trust.Threshold > len(out.Trust.Keys) || len(out.Trust.Keys) > 32 {
		return VerifyPolicy{}, nil, invalid("trust threshold is impossible")
	}
	trusted := make(map[string]TrustedKey, len(out.Trust.Keys))
	trustedMaterial := make(map[string]string, len(out.Trust.Keys))
	keys := make([]TrustedKey, 0, len(out.Trust.Keys))
	for _, key := range out.Trust.Keys {
		if !namePattern.MatchString(key.KeyID) || !isP256PublicKey(key.PublicKey) {
			return VerifyPolicy{}, nil, invalid("trusted key %q is invalid or not ECDSA P-256", key.KeyID)
		}
		if _, exists := trusted[key.KeyID]; exists {
			return VerifyPolicy{}, nil, invalid("duplicate trusted key_id %q", key.KeyID)
		}
		fingerprint := publicKeyFingerprint(key.PublicKey)
		if previousID, exists := trustedMaterial[fingerprint]; exists {
			return VerifyPolicy{}, nil, invalid("trusted key material is duplicated under %q and %q", previousID, key.KeyID)
		}
		trustedMaterial[fingerprint] = key.KeyID
		if key.NotBefore.IsZero() || key.NotAfter.IsZero() || !key.NotAfter.After(key.NotBefore) {
			return VerifyPolicy{}, nil, invalid("trusted key %q has an invalid validity window", key.KeyID)
		}
		copy := cloneTrustedKey(key)
		trusted[copy.KeyID] = copy
		keys = append(keys, copy)
	}
	out.Trust.Keys = keys
	if err := validateState(out.Current); err != nil {
		return VerifyPolicy{}, nil, err
	}
	return out, trusted, nil
}

func cloneTrustedKey(key TrustedKey) TrustedKey {
	copy := key
	copy.PublicKey = &ecdsa.PublicKey{
		Curve: key.PublicKey.Curve,
		X:     newBigInt(key.PublicKey.X),
		Y:     newBigInt(key.PublicKey.Y),
	}
	if key.RevokedAt != nil {
		revoked := *key.RevokedAt
		copy.RevokedAt = &revoked
	}
	return copy
}

func newBigInt(value *big.Int) *big.Int {
	return new(big.Int).SetBytes(value.Bytes())
}

func enforceManifestPolicy(manifest Manifest, policy VerifyPolicy) error {
	if manifest.Channel != policy.Channel {
		return fmt.Errorf("%w: channel %q does not match %q", ErrVersionGate, manifest.Channel, policy.Channel)
	}
	if manifest.IssuedAt.After(policy.Now.Add(policy.ClockSkew)) {
		return invalid("manifest issued_at is in the future")
	}
	if policy.Now.After(manifest.ExpiresAt.Add(policy.ClockSkew)) {
		return invalid("manifest is expired")
	}
	if manifest.ExpiresAt.Sub(manifest.IssuedAt) > policy.MaxLifetime {
		return invalid("manifest lifetime exceeds %s", policy.MaxLifetime)
	}
	currentRunner, _ := parseSemanticVersion(policy.RunnerVersion)
	currentUpdater, _ := parseSemanticVersion(policy.UpdaterVersion)
	minimumRunner, _ := parseSemanticVersion(manifest.MinRunner)
	minimumUpdater, _ := parseSemanticVersion(manifest.MinUpdater)
	release, _ := parseSemanticVersion(manifest.Version)
	if compareVersions(currentRunner, minimumRunner) < 0 {
		return fmt.Errorf("%w: runner %s is below minimum %s", ErrVersionGate, policy.RunnerVersion, manifest.MinRunner)
	}
	if compareVersions(currentUpdater, minimumUpdater) < 0 {
		return fmt.Errorf("%w: updater %s is below minimum %s", ErrVersionGate, policy.UpdaterVersion, manifest.MinUpdater)
	}
	if compareVersions(release, currentRunner) < 0 {
		return fmt.Errorf("%w: release %s would downgrade runner %s", ErrVersionGate, manifest.Version, policy.RunnerVersion)
	}
	return nil
}

func verifyThreshold(signatures []Signature, digest [32]byte, manifest Manifest, policy VerifyPolicy, trusted map[string]TrustedKey) []string {
	valid := make([]string, 0, len(signatures))
	for _, signature := range signatures {
		key, exists := trusted[signature.KeyID]
		if !exists || !keyActive(key, manifest.IssuedAt, policy.Now) {
			continue
		}
		decoded, _ := base64.StdEncoding.DecodeString(signature.Value)
		if ecdsa.VerifyASN1(key.PublicKey, digest[:], decoded) {
			valid = append(valid, signature.KeyID)
		}
	}
	sort.Strings(valid)
	return valid
}

func keyActive(key TrustedKey, issuedAt, now time.Time) bool {
	if issuedAt.Before(key.NotBefore) || issuedAt.After(key.NotAfter) || now.Before(key.NotBefore) || now.After(key.NotAfter) {
		return false
	}
	if key.RevokedAt != nil && (!now.Before(*key.RevokedAt) || !issuedAt.Before(*key.RevokedAt)) {
		return false
	}
	return true
}

func validateState(state State) error {
	if state.Sequence == 0 {
		if state.ReleaseID != "" || state.PayloadSHA256 != "" {
			return invalid("empty state cannot contain release identity")
		}
		return nil
	}
	if !releasePattern.MatchString(state.ReleaseID) || !digestPattern.MatchString(state.PayloadSHA256) {
		return invalid("current state requires release_id and lowercase payload digest")
	}
	return nil
}

func checkState(manifest Manifest, payloadDigest string, current State) (bool, error) {
	if current.Sequence == 0 {
		return false, nil
	}
	if manifest.Sequence < current.Sequence {
		return false, fmt.Errorf("%w: sequence %d is below current %d", ErrReplay, manifest.Sequence, current.Sequence)
	}
	if manifest.Sequence == current.Sequence {
		if manifest.ReleaseID != current.ReleaseID || payloadDigest != current.PayloadSHA256 {
			return false, fmt.Errorf("%w: sequence %d has different release content", ErrEquivocation, manifest.Sequence)
		}
		return true, nil
	}
	if manifest.ReleaseID == current.ReleaseID {
		return false, fmt.Errorf("%w: release_id %q reused at a new sequence", ErrEquivocation, manifest.ReleaseID)
	}
	return false, nil
}
