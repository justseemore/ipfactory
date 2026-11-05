package ipfactory

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// DetectKind 读文件头判断格式。
//
// AWDB 以 2 字节大端 JSON 头长度开头，JSON 里必有 node_count 与 columns；
// 其它一律按 ip2region XDB 处理。只读前几 KB，不加载整个库。
func DetectKind(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var head [2]byte
	if _, err := io.ReadFull(f, head[:]); err != nil {
		return "", fmt.Errorf("ipfactory: %q: cannot read header: %w", path, err)
	}

	if metaLen := int(binary.BigEndian.Uint16(head[:])); metaLen > 0 {
		buf := make([]byte, metaLen)
		if _, err := io.ReadFull(f, buf); err == nil {
			var probe struct {
				NodeCount *uint32         `json:"node_count"`
				Columns   json.RawMessage `json:"columns"`
			}
			if err := json.Unmarshal(buf, &probe); err == nil && probe.NodeCount != nil && len(probe.Columns) > 0 {
				return KindAWDB, nil
			}
		}
	}

	return KindXDB, nil
}