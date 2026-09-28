// Package hashx 提供文件内容哈希。
//
// 粒度：整个文件（v1 口径，见需求 FR-3.3）。
// 若单文件内多条目需独立追踪，v2 再细化到函数块。
package hashx

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
)

// Format 输出摘要格式：sha256:<hex>
const prefix = "sha256:"

// File 计算文件摘要，形如 sha256:ab12…
//
// 文件不存在返回 ("", false)；其它 IO 错误返回 err。
func File(path string) (string, bool, error) {
	handle, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	defer handle.Close()

	digest := sha256.New()
	if _, err := io.Copy(digest, handle); err != nil {
		return "", false, err
	}
	return prefix + hex.EncodeToString(digest.Sum(nil)), true, nil
}

// Bytes 计算内存数据摘要。
func Bytes(data []byte) string {
	sum := sha256.Sum256(data)
	return prefix + hex.EncodeToString(sum[:])
}

// Short 截断展示用：sha256:ab12cd34… -> ab12cd34
func Short(hash string, length int) string {
	if hash == "" {
		return "-"
	}
	hexPart := hash
	if _, after, found := strings.Cut(hash, ":"); found {
		hexPart = after
	}
	if length <= 0 || length > len(hexPart) {
		length = len(hexPart)
	}
	return hexPart[:length]
}
