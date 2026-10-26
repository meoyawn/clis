package service

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/md5" // Native website wire-format derivation, not credential hashing.
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
)

// The website's parseEncryptedResponse transport: Base64 ciphertext,
// AES-128-ECB/PKCS7, then zlib JSON. This public client format grants no access;
// authentication and redaction remain enforced by the provider.
func decodeWire(data []byte, out any) error {
	// Autocomplete returns a plain array; only object responses can carry the
	// encrypted salary envelope. Decode other JSON directly into its owner type.
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return json.Unmarshal(data, out)
	}
	var envelope struct {
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("decode native envelope: %w", err)
	}
	if envelope.Payload == "" {
		return json.Unmarshal(data, out)
	}
	sum := md5.Sum([]byte("levelstothemoon!!"))
	key := []byte(base64.StdEncoding.EncodeToString(sum[:])[:16])
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	plain, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil || len(plain) == 0 || len(plain)%aes.BlockSize != 0 {
		return fmt.Errorf("invalid native ciphertext")
	}
	for i := 0; i < len(plain); i += aes.BlockSize {
		block.Decrypt(plain[i:i+aes.BlockSize], plain[i:i+aes.BlockSize])
	}
	padding := int(plain[len(plain)-1])
	if padding < 1 || padding > aes.BlockSize || padding > len(plain) {
		return fmt.Errorf("invalid native padding")
	}
	for _, b := range plain[len(plain)-padding:] {
		if int(b) != padding {
			return fmt.Errorf("invalid native padding")
		}
	}
	reader, err := zlib.NewReader(bytes.NewReader(plain[:len(plain)-padding]))
	if err != nil {
		return fmt.Errorf("decode native compressed payload: %w", err)
	}
	defer reader.Close()
	payload, err := io.ReadAll(io.LimitReader(reader, (8<<20)+1))
	if err != nil {
		return err
	}
	if len(payload) > 8<<20 {
		return fmt.Errorf("native payload exceeds 8 MiB")
	}
	if err = json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode native payload JSON: %w", err)
	}
	return nil
}
