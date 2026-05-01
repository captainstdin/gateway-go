package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
)

// DeriveKey 从 secretKey 派生 AES-256 密钥（32字节）
func DeriveKey(secretKey string) []byte {
	hash := sha256.Sum256([]byte(secretKey))
	return hash[:]
}

// Encrypt 使用 AES-256-CBC 加密
// 返回格式: [16字节IV] + [密文]
func Encrypt(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	// PKCS7 填充
	plaintext = pkcs7Pad(plaintext, aes.BlockSize)

	// 生成随机 IV
	ciphertext := make([]byte, aes.BlockSize+len(plaintext))
	iv := ciphertext[:aes.BlockSize]
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, err
	}

	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(ciphertext[aes.BlockSize:], plaintext)

	return ciphertext, nil
}

// Decrypt 使用 AES-256-CBC 解密
// 输入格式: [16字节IV] + [密文]
func Decrypt(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	if len(ciphertext) < aes.BlockSize {
		return nil, errors.New("ciphertext too short")
	}

	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, errors.New("ciphertext is not a multiple of the block size")
	}

	iv := ciphertext[:aes.BlockSize]
	ciphertext = ciphertext[aes.BlockSize:]

	mode := cipher.NewCBCDecrypter(block, iv)
	plaintext := make([]byte, len(ciphertext))
	mode.CryptBlocks(plaintext, ciphertext)

	// PKCS7 去填充
	plaintext, err = pkcs7Unpad(plaintext)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
}

// EncryptToBase64 加密后返回 Base64 编码字符串（用于文本协议）
func EncryptToBase64(plaintext, key []byte) (string, error) {
	encrypted, err := Encrypt(plaintext, key)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

// DecryptFromBase64 从 Base64 字符串解密（用于文本协议）
func DecryptFromBase64(encoded string, key []byte) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	return Decrypt(ciphertext, key)
}

// pkcs7Pad PKCS7 填充
func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	padBytes := make([]byte, padding)
	for i := range padBytes {
		padBytes[i] = byte(padding)
	}
	return append(data, padBytes...)
}

// pkcs7Unpad PKCS7 去填充
func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("empty data")
	}
	padding := int(data[len(data)-1])
	if padding == 0 || padding > aes.BlockSize || padding > len(data) {
		return nil, errors.New("invalid padding")
	}
	for i := len(data) - padding; i < len(data); i++ {
		if data[i] != byte(padding) {
			return nil, errors.New("invalid padding")
		}
	}
	return data[:len(data)-padding], nil
}
