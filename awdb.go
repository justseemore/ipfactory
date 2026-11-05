package ipfactory

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/justseemore/awdb"
)

// AWDBConfig 描述一个 AWDB 文件后端。
type AWDBConfig struct {
	// Path 是 .awdb 文件路径，必填。
	Path string

	// Name 是后端名，默认 "awdb"。
	Name string

	// Preload 为 true 时让 awdb 把整文件读进堆内存（os.ReadFile + OpenBytes），
	// 而不是 mmap：无运行期缺页、不占句柄，RSS ≈ 文件大小。
	Preload bool

	// Aliases 覆盖或补充列名映射，键会被归一化（小写、- / 空格 / . 转 _）。
	// 例如 {"continent_cn": "continent"}。
	Aliases map[string]string
}

type awdbBackend struct {
	name    string
	reader  *awdb.Reader
	aliases map[string]string
}

// OpenAWDB 打开一个 AWDB 文件作为后端。
func OpenAWDB(cfg AWDBConfig) (Backend, error) {
	if cfg.Path == "" {
		return nil, errors.New("ipfactory: AWDBConfig.Path is empty")
	}
	reader, err := awdb.Open(cfg.Path, awdb.WithPreload(cfg.Preload))
	if err != nil {
		return nil, fmt.Errorf("ipfactory: open awdb %q: %w", cfg.Path, err)
	}
	name := cfg.Name
	if name == "" {
		name = "awdb"
	}
	return &awdbBackend{name: name, reader: reader, aliases: cfg.Aliases}, nil
}

func (b *awdbBackend) Name() string { return b.name }

func (b *awdbBackend) Close() error { return b.reader.Close() }

// Lookup 是字符串入口。
func (b *awdbBackend) Lookup(ip string) (*Result, error) {
	addr, err := parseIP(ip)
	if err != nil {
		return nil, err
	}
	return b.LookupAddr(context.Background(), addr)
}

func (b *awdbBackend) LookupAddr(ctx context.Context, addr netip.Addr) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	record, err := b.reader.LookupAddr(addr)
	if err != nil {
		switch {
		case errors.Is(err, awdb.ErrNotFound):
			return nil, fmt.Errorf("%w: %s", ErrNotFound, addr)
		case errors.Is(err, awdb.ErrIPv6QueryOnIPv4DB),
			errors.Is(err, awdb.ErrIPv4QueryOnIPv6DB):
			// 地址族不在该库覆盖范围内：当作未命中，让主备链继续找。
			return nil, fmt.Errorf("%w: %s (address family not covered by %s)", ErrNotFound, addr, b.name)
		case errors.Is(err, awdb.ErrInvalidIP):
			return nil, fmt.Errorf("%w: %s", ErrInvalidIP, addr)
		}
		return nil, err
	}

	res := &Result{IP: addr.String(), Version: 4, Backend: b.name}
	if !addr.Is4() && !addr.Is4In6() {
		res.Version = 6
	}

	// Map() 会把嵌套列（多语言区划表）转成 []map[string]any，未知列原样进 Extra。
	for key, value := range record.Map() {
		mapColumn(res, key, value, b.aliases)
	}
	return res, nil
}

// 编译期断言，避免 awdb 的接口变动后这里悄悄失配。
var _ Backend = (*awdbBackend)(nil)