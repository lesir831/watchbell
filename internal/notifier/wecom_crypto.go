package notifier

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"sort"
	"strings"
)

var errWeComMessage = errors.New("invalid encrypted WeCom message")

func WeComSignature(token, timestamp, nonce, encrypted string) string {
	parts := []string{token, timestamp, nonce, encrypted}
	sort.Strings(parts)
	sum := sha1.Sum([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(sum[:])
}

func DecryptWeCom(cfg WeComConfig, signature, timestamp, nonce, encrypted string) ([]byte, error) {
	expected := WeComSignature(cfg.Token, timestamp, nonce, encrypted)
	if timestamp == "" || nonce == "" || subtle.ConstantTimeCompare([]byte(signature), []byte(expected)) != 1 {
		return nil, errWeComMessage
	}
	key, err := base64.StdEncoding.DecodeString(cfg.EncodingAESKey + "=")
	if err != nil || len(key) != 32 {
		return nil, errWeComMessage
	}
	data, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil || len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errWeComMessage
	}
	block, _ := aes.NewCipher(key)
	cipher.NewCBCDecrypter(block, key[:aes.BlockSize]).CryptBlocks(data, data)
	padding := int(data[len(data)-1])
	if padding < 1 || padding > 32 || padding > len(data) || !bytes.Equal(data[len(data)-padding:], bytes.Repeat([]byte{byte(padding)}, padding)) {
		return nil, errWeComMessage
	}
	data = data[:len(data)-padding]
	if len(data) < 20 {
		return nil, errWeComMessage
	}
	size := uint64(binary.BigEndian.Uint32(data[16:20]))
	if size > uint64(len(data)-20) {
		return nil, errWeComMessage
	}
	end := 20 + int(size)
	if subtle.ConstantTimeCompare(data[end:], []byte(cfg.CorpID)) != 1 {
		return nil, errWeComMessage
	}
	return data[20:end], nil
}

// EncryptWeCom returns an encrypted XML envelope for passive replies.
func EncryptWeCom(cfg WeComConfig, content []byte, timestamp, nonce string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(cfg.EncodingAESKey + "=")
	if err != nil || len(key) != 32 {
		return nil, errWeComMessage
	}
	data := make([]byte, 20, 20+len(content)+len(cfg.CorpID)+32)
	if _, err := rand.Read(data[:16]); err != nil {
		return nil, err
	}
	binary.BigEndian.PutUint32(data[16:20], uint32(len(content)))
	data = append(data, content...)
	data = append(data, cfg.CorpID...)
	padding := 32 - len(data)%32
	data = append(data, bytes.Repeat([]byte{byte(padding)}, padding)...)
	block, _ := aes.NewCipher(key)
	cipher.NewCBCEncrypter(block, key[:aes.BlockSize]).CryptBlocks(data, data)
	encrypted := base64.StdEncoding.EncodeToString(data)
	return xml.Marshal(struct {
		XMLName   xml.Name `xml:"xml"`
		Encrypt   string   `xml:"Encrypt"`
		Signature string   `xml:"MsgSignature"`
		Timestamp string   `xml:"TimeStamp"`
		Nonce     string   `xml:"Nonce"`
	}{Encrypt: encrypted, Signature: WeComSignature(cfg.Token, timestamp, nonce, encrypted), Timestamp: timestamp, Nonce: nonce})
}
