package trustset

import (
	"bytes"
	"strconv"
	"time"
)

func (document Document) Canonical() []byte {
	var out bytes.Buffer
	out.WriteString(`{"format":`)
	out.WriteString(strconv.Itoa(document.Format))
	out.WriteString(`,"issuedAt":"`)
	out.WriteString(formatTimestamp(document.IssuedAt))
	out.WriteString(`","keys":[`)
	for index, key := range document.Keys {
		if index > 0 {
			out.WriteByte(',')
		}
		out.WriteString(`{"keyId":"`)
		out.WriteString(key.KeyID)
		out.WriteString(`",`)
		if key.NotAfter != nil {
			out.WriteString(`"notAfter":"`)
			out.WriteString(formatTimestamp(*key.NotAfter))
			out.WriteString(`",`)
		}
		out.WriteString(`"notBefore":"`)
		out.WriteString(formatTimestamp(key.NotBefore))
		out.WriteString(`","publicKeySpkiBase64":"`)
		out.WriteString(key.PublicKeySPKIBase64)
		out.WriteString(`"}`)
	}
	out.WriteString(`],"revokedKeyIds":[`)
	for index, id := range document.RevokedKeyIDs {
		if index > 0 {
			out.WriteByte(',')
		}
		out.WriteByte('"')
		out.WriteString(id)
		out.WriteByte('"')
	}
	out.WriteString(`],"sequence":`)
	out.WriteString(strconv.FormatInt(document.Sequence, 10))
	out.WriteByte('}')
	return out.Bytes()
}

func formatTimestamp(value time.Time) string {
	return value.UTC().Format(timestampLayout)
}
