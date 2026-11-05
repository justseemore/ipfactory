package ipfactory

import (
	"fmt"
	"strings"
)

// 后端类型。
const (
	// KindAuto 让工厂按文件内容探测（默认）。
	KindAuto = "auto"
	// KindAWDB 强制使用 AWDB 后端。
	KindAWDB = "awdb"
	// KindXDB 强制使用 ip2region xdb 后端。
	KindXDB = "xdb"
	// KindGeoCN 强制使用 ljxi/GeoCN 的 mmdb 后端。
	KindGeoCN = "geocn"
)

// Config 描述一个后端来源。
//
type Config struct {
	// Kind 是 "awdb" / "xdb" / "geocn" / "auto"（留空等价于 auto）。
	Kind string

	// AWDB 在 Kind 为 awdb 或 auto 时使用。
	AWDB AWDBConfig

	// XDB 在 Kind 为 xdb 或 auto 时使用。
	XDB XDBConfig

	// GeoCN 在 Kind 为 geocn 或 auto 时使用。
	GeoCN GeoCNConfig

	// Raw 为 true 时不做字段值归一化，保留各库的原始写法。
	// 默认 false，即用 Normalize 统一占位符、国家名与 ISO 码。
	Raw bool
}

// resolveKind 决定用哪个后端：显式 Kind 优先，否则按配置和文件内容推断。
func (c Config) resolveKind() (string, error) {
	kind := strings.ToLower(strings.TrimSpace(c.Kind))
	if kind != "" && kind != KindAuto {
		return kind, nil
	}

	// 填了 GeoCN.Path 就说明要用它。mmdb 与 awdb / xdb 的内容特征不同，但
	// GeoCN 与普通 GeoLite2 无法从内容区分，所以不做探测。
	if c.GeoCN.Path != "" {
		return KindGeoCN, nil
	}

	hasXDB := c.XDB.V4Path != "" || c.XDB.V6Path != ""

	if c.AWDB.Path != "" {
		if detected, err := DetectKind(c.AWDB.Path); err == nil {
			if detected == KindXDB && hasXDB {
				return KindXDB, nil
			}
			if detected == KindAWDB {
				return KindAWDB, nil
			}
		}
		// 探测失败就按 awdb 打开，让错误信息带上具体原因。
		return KindAWDB, nil
	}
	if hasXDB {
		return KindXDB, nil
	}
	return "", ErrNoBackend
}

// OpenBackend 按配置打开单个后端。
func OpenBackend(cfg Config) (Backend, error) {
	kind, err := cfg.resolveKind()
	if err != nil {
		return nil, err
	}

	var backend Backend
	switch kind {
	case KindAWDB:
		backend, err = OpenAWDB(cfg.AWDB)
	case KindXDB, "ipgeo", "ip2region":
		backend, err = OpenXDB(cfg.XDB)
	case KindGeoCN, "mmdb":
		backend, err = OpenGeoCN(cfg.GeoCN)
	default:
		return nil, fmt.Errorf("ipfactory: unknown kind %q", kind)
	}
	if err != nil {
		return nil, err
	}

	if cfg.Raw {
		return backend, nil
	}
	return normalizeBackend{Backend: backend}, nil
}

// OpenChain 按顺序打开多个配置并串成主备链。
//
// 只给一个配置时直接返回该后端，不额外包一层 Chain。任一后端打开失败时，
// 已打开的后端会被关闭，不会泄露 mmap。
func OpenChain(cfgs ...Config) (Backend, error) {
	switch len(cfgs) {
	case 0:
		return nil, ErrNoBackend
	case 1:
		return OpenBackend(cfgs[0])
	}

	backends := make([]Backend, 0, len(cfgs))
	for i, cfg := range cfgs {
		b, err := OpenBackend(cfg)
		if err != nil {
			for _, opened := range backends {
				_ = opened.Close()
			}
			return nil, fmt.Errorf("ipfactory: open backend #%d: %w", i, err)
		}
		backends = append(backends, b)
	}
	return NewChain("", backends...), nil
}
