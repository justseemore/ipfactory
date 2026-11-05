package ipfactory

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
)

// Chain 顺序查询多个后端，第一个命中的结果直接返回。
//
// 典型用法：
//   - 主备：官方库未收录时回落到免费库
//   - 分工：v4 查 AWDB、v6 查 XDB（地址族不匹配的后端返回 ErrNotFound，链会继续）
type Chain struct {
	name     string
	backends []Backend
}

// NewChain 创建一个链；nil 后端会被忽略。
func NewChain(name string, backends ...Backend) *Chain {
	if name == "" {
		name = "chain"
	}
	list := make([]Backend, 0, len(backends))
	for _, b := range backends {
		if b != nil {
			list = append(list, b)
		}
	}
	return &Chain{name: name, backends: list}
}

// Name 返回链名。
func (c *Chain) Name() string { return c.name }

// Backends 返回链上的后端（副本）。
func (c *Chain) Backends() []Backend {
	out := make([]Backend, len(c.backends))
	copy(out, c.backends)
	return out
}

// Lookup 是字符串入口，正常命中时走和 LookupAddr 相同的回落逻辑。
func (c *Chain) Lookup(ip string) (*Result, error) {
	addr, err := parseIP(ip)
	if err != nil {
		return nil, err
	}
	return c.LookupAddr(context.Background(), addr)
}

// LookupAddr 依次查询；全部未命中时返回 ErrNotFound，出现硬错误时返回第一个硬错误。
func (c *Chain) LookupAddr(ctx context.Context, addr netip.Addr) (*Result, error) {
	if len(c.backends) == 0 {
		return nil, ErrNoBackend
	}

	var hardErr error
	allNotFound := true

	for _, backend := range c.backends {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, err := backend.LookupAddr(ctx, addr)
		if err == nil {
			if res != nil && res.Backend == "" {
				res.Backend = backend.Name()
			}
			return res, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if !errors.Is(err, ErrNotFound) {
			allNotFound = false
			if hardErr == nil {
				hardErr = err
			}
		}
	}

	if allNotFound {
		return nil, fmt.Errorf("%w: %s (tried %d backends)", ErrNotFound, addr, len(c.backends))
	}
	if hardErr != nil {
		return nil, hardErr
	}
	return nil, fmt.Errorf("%w: %s", ErrNotFound, addr)
}

// Close 关闭所有后端；重复调用安全，错误用 errors.Join 汇总。
func (c *Chain) Close() error {
	errs := make([]error, 0, len(c.backends))
	for _, backend := range c.backends {
		if err := backend.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

var _ Backend = (*Chain)(nil)