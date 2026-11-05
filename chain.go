package ipfactory

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
)

// Chain 顺序查询多个后端，第一个命中的结果直接返回。
//
// 可选开启“字段补齐”（默认开启，可用 SetMergeMissing(false) 关掉）：首个命中的
// 后端若国家/省/市/区县/ISP 有空缺，会继续查后续后端把空字段填上，但不覆盖已有值。适合“GeoCN 只给到区、
// ip2region 只有省市”这类互补场景。
type Chain struct {
	name     string
	backends []Backend
	merge    bool
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

// SetMergeMissing 开关字段补齐，返回自身便于链式调用。
func (c *Chain) SetMergeMissing(merge bool) *Chain {
	c.merge = merge
	return c
}

// Name 返回链名。
func (c *Chain) Name() string { return c.name }

// Backends 返回链上的后端（副本）。
func (c *Chain) Backends() []Backend {
	out := make([]Backend, len(c.backends))
	copy(out, c.backends)
	return out
}

// Lookup 是字符串入口。
func (c *Chain) Lookup(ip string) (*Result, error) {
	addr, err := parseIP(ip)
	if err != nil {
		return nil, err
	}
	return c.LookupAddr(context.Background(), addr)
}

// LookupAddr 依次查询：第一个命中的后端作为基准；开启 merge 时继续用后续
// 后端补齐空字段。全部未命中返回 ErrNotFound，出现硬错误时返回第一个硬错误。
func (c *Chain) LookupAddr(ctx context.Context, addr netip.Addr) (*Result, error) {
	if len(c.backends) == 0 {
		return nil, ErrNoBackend
	}

	var (
		base        *Result
		baseIdx     = -1
		hardErr     error
		allNotFound = true
	)

	for i, backend := range c.backends {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, err := backend.LookupAddr(ctx, addr)
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
				allNotFound = false
				if hardErr == nil {
					hardErr = err
				}
			}
			continue
		}
		if res == nil {
			continue
		}
		if res.Backend == "" {
			res.Backend = backend.Name()
		}
		base = res
		baseIdx = i
		break
	}

	if base == nil {
		if allNotFound {
			return nil, fmt.Errorf("%w: %s (tried %d backends)", ErrNotFound, addr, len(c.backends))
		}
		if hardErr != nil {
			return nil, hardErr
		}
		return nil, fmt.Errorf("%w: %s", ErrNotFound, addr)
	}

	if !c.merge || !missingCore(base) {
		return base, nil
	}

	// 已命中就以基准为准，补齐阶段出错也不影响结果。
	for _, backend := range c.backends[baseIdx+1:] {
		if ctx.Err() != nil {
			break
		}
		res, err := backend.LookupAddr(ctx, addr)
		if err != nil || res == nil {
			continue
		}
		fillMissing(base, res)
		if !missingCore(base) {
			break
		}
	}
	return base, nil
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

// missingCore 判断 5 个契约字段是否还缺。ISP 也计入：调用方要求任何后端都能
// 拿到这 5 个字段，代价是可能为一条记录多查一次后续后端。
func missingCore(res *Result) bool {
	return res.Country == "" || res.Province == "" ||
		res.City == "" || res.District == "" || res.ISP == ""
}

// fillMissing 只用 src 的非空值填 dst 的空字段，不覆盖已有值。
func fillMissing(dst, src *Result) {
	if dst.Country == "" {
		dst.Country = src.Country
	}
	if dst.Province == "" {
		dst.Province = src.Province
	}
	if dst.City == "" {
		dst.City = src.City
	}
	if dst.District == "" {
		dst.District = src.District
	}
	if dst.ISP == "" {
		dst.ISP = src.ISP
	}
	if dst.ISOCode == "" {
		dst.ISOCode = src.ISOCode
	}
	if dst.NetworkType == "" {
		dst.NetworkType = src.NetworkType
	}
}

var _ Backend = (*Chain)(nil)
