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

	// DisableMergeMissing 关闭链式字段补齐。默认开启：首个命中的后端若
	// 国家/省/市/区县/ISP 有空缺，继续用后续后端补上（不覆盖已有值）。
	DisableMergeMissing bool

	// MissingPlaceholder 是 5 个契约字段缺失时的占位符，默认 "未知"。
	// 完全不填充请用 Raw。
	MissingPlaceholder string

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

	placeholder := cfg.MissingPlaceholder
	if placeholder == "" {
		placeholder = DefaultMissingPlaceholder
	}
	return ensureBackend{
		Backend:     normalizeBackend{Backend: backend},
		placeholder: placeholder,
	}, nil
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
		// 剥掉最外层的补齐包装：链内部要看到真实空值才能合并，
		// 补齐统一在整条链返回后做一次。
		if ensured, ok := b.(ensureBackend); ok {
			b = ensured.Backend
		}
		backends = append(backends, b)
	}

	merge := true
	for _, cfg := range cfgs {
		if cfg.DisableMergeMissing {
			merge = false
			break
		}
	}

	chain := NewChain("", backends...).SetMergeMissing(merge)
	if cfgs[0].Raw {
		return chain, nil
	}

	placeholder := cfgs[0].MissingPlaceholder
	if placeholder == "" {
		placeholder = DefaultMissingPlaceholder
	}
	return ensureBackend{Backend: chain, placeholder: placeholder}, nil
}
