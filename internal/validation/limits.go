package validation

// maxJSONSafeInteger is the largest integer that survives the protocol's JSON
// number representation without losing precision in JavaScript consumers.
const maxJSONSafeInteger = uint64(1<<53 - 1)
