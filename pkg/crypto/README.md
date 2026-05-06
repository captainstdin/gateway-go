# crypto 包

## 功能

提供 Gateway Worker 架构中所有组件间通讯的 **AES-256-CBC 加密/解密** 能力。

## 原理

### 密钥派生

```
SecretKey (用户配置的字符串)
    ↓ SHA-256
AES Key (32 字节，适用于 AES-256)
```

所有组件（Register、Gateway、Worker）使用相同的 `SecretKey`，通过 SHA-256 派生出一致的 AES 密钥。

### 加密格式

**二进制模式**（Gateway ↔ Worker 内部通讯）：

```
[16 字节随机 IV] + [PKCS7 填充后的 AES-256-CBC 密文]
```

**文本模式**（Register ↔ Gateway/Worker 文本协议）：

```
Base64(上述二进制格式) + "\n"
```

### 数据流中的加密层

```
原始数据 → AES-256-CBC 加密 → [4字节密文长度][密文]  (二进制传输)
原始数据 → AES-256-CBC 加密 → Base64 编码 + 换行符  (文本传输)
```

## 导出函数

| 函数 | 用途 |
|---|---|
| `DeriveKey(secretKey)` | 从密钥字符串派生 32 字节 AES 密钥 |
| `Encrypt(plaintext, key)` | AES-256-CBC 加密，返回 `[IV][密文]` |
| `Decrypt(ciphertext, key)` | AES-256-CBC 解密 |
| `EncryptToBase64(plaintext, key)` | 加密后 Base64 编码（文本协议用） |
| `DecryptFromBase64(encoded, key)` | Base64 解码后解密（文本协议用） |

## 安全特性

- 每次加密使用 `crypto/rand` 生成**随机 IV**，相同明文每次加密结果不同
- PKCS7 填充，解密时严格校验填充合法性
