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
)

// Config 描述一个后端来源。
//
type Config struct {
	// Kind 是 "awdb" / "xdb" / "auto"（留空等价于 auto）。
	Kind string

	// AWDB 在 Kind 为 awdb 或 auto 时使用。
	AWDB AWDBConfig

	// XDB 在 Kind 为 xdb 或 auto 时使用。
	XDB XDBConfig
}

// resolveKind 决定用哪个后端：显式 Kind 优先，否则按文件内容探测。
func (c Config) resolveKind() (string, error) {
	kind := strings.ToLower(strings.TrimSpace(c.Kind))
	if kind != "" && kind != KindAuto {
		return kind, nil
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

	switch kind {
	case KindAWDB:
		return OpenAWDB(cfg.AWDB)
	case KindXDB, "ipgeo":
		return OpenXDB(cfg.XDB)
	}
	return nil, fmt.Errorf("ipfactory: unknown kind %q", kind)
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
