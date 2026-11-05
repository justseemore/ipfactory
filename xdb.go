package ipfactory

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/justseemore/ipgeo"
)

// XDBConfig 描述一个 ip2region xdb 后端（v4 / v6 各一个文件，可只给其中一个）。
type XDBConfig struct {
	V4Path string
	V6Path string

	// Name 是后端名，默认 "xdb"。
	Name string

	// Policy 对应 ipgeo.WithCachePolicy（CacheNone / CacheVectorIndex / CacheFull）。
	//
	// 注意：ipgeo 当前分支已统一为「mmap 整文件 + 二分查找」，
	// WithCachePolicy / WithV4CachePolicy / WithV6CachePolicy / WithSearchers 都是
	// 兼容用的空实现，三种策略行为完全一致。这里只做原样透传，等上游恢复行为后
	// 自然生效；想要结果缓存请用 LRU。
	Policy ipgeo.CachePolicy

	// LRU 是结果缓存条数，0 表示关闭。
	LRU int

	// Preload 为 true 时让 ipgeo 把 xdb 整个读进堆内存（RSS ≈ 文件大小），
	// 而不是 mmap 按需缺页。适合库文件固定、实例内存可控的部署。
	Preload bool

	// Layout 显式指定 region 串的字段布局；nil 表示由 ipgeo 自动推断。
	Layout ipgeo.Layout
}

type xdbBackend struct {
	name string
	db   *ipgeo.DB
}

// OpenXDB 打开 ip2region xdb 作为后端。
func OpenXDB(cfg XDBConfig) (Backend, error) {
	if cfg.V4Path == "" && cfg.V6Path == "" {
		return nil, errors.New("ipfactory: XDBConfig needs V4Path or V6Path")
	}

	opts := make([]ipgeo.Option, 0, 4)
	opts = append(opts, ipgeo.WithCachePolicy(cfg.Policy))
	if cfg.LRU > 0 {
		opts = append(opts, ipgeo.WithLRU(cfg.LRU))
	}
	if cfg.Preload {
		opts = append(opts, ipgeo.WithPreload(true))
	}
	if cfg.Layout != nil {
		opts = append(opts, ipgeo.WithLayout(cfg.Layout))
	}

	db, err := ipgeo.Open(cfg.V4Path, cfg.V6Path, opts...)
	if err != nil {
		return nil, fmt.Errorf("ipfactory: open xdb (v4=%q v6=%q): %w", cfg.V4Path, cfg.V6Path, err)
	}

	name := cfg.Name
	if name == "" {
		name = "xdb"
	}
	return &xdbBackend{name: name, db: db}, nil
}

func (b *xdbBackend) Name() string { return b.name }

// Close 忽略 ipgeo.DB.Close 没有返回值这一点，保持 Backend 的签名统一。
func (b *xdbBackend) Close() error {
	b.db.Close()
	return nil
}

// Lookup 是字符串入口。
func (b *xdbBackend) Lookup(ip string) (*Result, error) {
	addr, err := parseIP(ip)
	if err != nil {
		return nil, err
	}
	return b.LookupAddr(context.Background(), addr)
}

func (b *xdbBackend) LookupAddr(ctx context.Context, addr netip.Addr) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	info, err := b.db.LookupAddr(addr)
	if err != nil {
		switch {
		case errors.Is(err, ipgeo.ErrNotFound):
			return nil, fmt.Errorf("%w: %s", ErrNotFound, addr)
		case errors.Is(err, ipgeo.ErrInvalidIP):
			return nil, fmt.Errorf("%w: %s", ErrInvalidIP, addr)
		}
		return nil, err
	}

	res := &Result{
		IP:          info.IP,
		Version:     int(info.Version),
		Continent:   info.Continent,
		Country:     info.Country,
		Province:    info.Province,
		City:        info.City,
		District:    info.District,
		ISP:         info.ISP,
		NetworkType: info.NetworkType,
		ISOCode:     info.ISOCode,
		IsPrivate:   info.IsPrivate,
		Backend:     b.name,
	}
	if res.IP == "" {
		res.IP = addr.String()
	}
	if info.Raw != "" {
		res.Extra = map[string]any{"raw": info.Raw}
	}
	return res, nil
}

var _ Backend = (*xdbBackend)(nil)