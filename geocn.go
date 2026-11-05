package ipfactory

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"github.com/oschwald/maxminddb-golang"
)

// GeoCNConfig 描述 ljxi/GeoCN 的 MaxMind mmdb 后端。
//
// GeoCN 只收录「MaxMind 认为属于中国大陆」的地址段，因此它非常适合放在主备链
// 最前面：命中就用它（精度到区），未命中会返回 ErrNotFound，链会继续落到
// awdb / xdb。
type GeoCNConfig struct {
	// Path 是 GeoCN.mmdb 的路径（GitHub Releases 里的那个文件），必填。
	Path string

	// Name 是后端名，默认 "geocn"。
	Name string

	// Divisions 是 GeoCN 仓库 data/full.txt（或 data/short.txt）的路径，
	// 用来把记录里的 division_code 解成省 / 市 / 区县名称。
	// 留空则只把 division_code 放进 Extra，省市区字段为空。
	Divisions string

	// Preload 为 true 时把 mmdb 整文件读进堆（maxminddb.FromBytes），
	// 而不是 mmap：无运行期缺页、不占句柄，RSS ≈ 文件大小。
	Preload bool
}

type geocnBackend struct {
	name   string
	reader *maxminddb.Reader
	div    map[uint32]string // 行政区划码 -> 名称
}

// OpenGeoCN 打开 GeoCN.mmdb 作为后端。
func OpenGeoCN(cfg GeoCNConfig) (Backend, error) {
	if cfg.Path == "" {
		return nil, errors.New("ipfactory: GeoCNConfig.Path is empty")
	}
	reader, err := openGeoCNReader(cfg)
	if err != nil {
		return nil, err
	}

	name := cfg.Name
	if name == "" {
		name = "geocn"
	}
	b := &geocnBackend{name: name, reader: reader}

	if cfg.Divisions != "" {
		div, err := loadDivisions(cfg.Divisions)
		if err != nil {
			_ = reader.Close()
			return nil, fmt.Errorf("ipfactory: load geocn divisions %q: %w", cfg.Divisions, err)
		}
		b.div = div
	}
	return b, nil
}

func (b *geocnBackend) Name() string { return b.name }

func (b *geocnBackend) Close() error { return b.reader.Close() }

// Lookup 是字符串入口。
func (b *geocnBackend) Lookup(ip string) (*Result, error) {
	addr, err := parseIP(ip)
	if err != nil {
		return nil, err
	}
	return b.LookupAddr(context.Background(), addr)
}

func (b *geocnBackend) LookupAddr(ctx context.Context, addr netip.Addr) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !addr.IsValid() {
		return nil, ErrInvalidIP
	}

	// GeoCN 是 mmdb，v4 地址交给库自己映射。
	ip := net.IP(addr.Unmap().AsSlice())
	var decoded any
	network, ok, err := b.reader.LookupNetwork(ip, &decoded)
	if err != nil {
		return nil, fmt.Errorf("ipfactory: geocn lookup %s: %w", addr, err)
	}
	// maxminddb 把记录解成 map[string]any；用 any 中转可以兼容空记录。
	raw, _ := decoded.(map[string]any)
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, addr)
	}

	res := &Result{
		IP:      addr.String(),
		Version: 4,
		Country: "中国",
		ISOCode: "CN",
		Backend: b.name,
	}
	if !addr.Is4() && !addr.Is4In6() {
		res.Version = 6
	}

	code := toUint32(raw["division_code"])
	if code != 0 {
		res.Province, res.City, res.District = b.splitDivision(code)
	}

	extra := make(map[string]any, len(raw)+2)
	for k, v := range raw {
		extra[k] = v
	}
	if code != 0 {
		extra["division_code"] = code
	}
	if network != nil {
		extra["network"] = network.String()
	}
	if len(extra) > 0 {
		res.Extra = extra
	}
	return res, nil
}

// splitDivision 把 6 位行政区划码拆成省 / 市 / 区县。
//
// 编码规则（GB/T 2260）：前两位省、中间两位市、后两位区县，全 0 表示没有下一级；
// GeoCN 的记录有的只到省（110000），有的到区县（110108）。
func (b *geocnBackend) splitDivision(code uint32) (province, city, district string) {
	if len(b.div) == 0 {
		return "", "", ""
	}

	provinceCode := code / 10000 * 10000
	cityCode := code / 100 * 100

	province = b.div[provinceCode]
	if cityCode != provinceCode {
		city = b.div[cityCode]
		// 直辖市的市级码一般写作「市辖区 / 县」，或与省同名；这类占位名
		// 统一用省名兜底，保证 City 与 xdb / awdb 的「北京市」写法对齐。
		switch city {
		case "", "市辖区", "县":
			city = province
		}
	}
	if code != cityCode && code != provinceCode {
		district = b.div[code]
	}
	return province, city, district
}

// loadDivisions 读取 GeoCN 仓库的行政区划表。
//
// 兼容两种格式：
//
//	data/full.txt   "110000\t北京市"
//	data/short.txt  "110000  北京市"（两个空格）
func loadDivisions(path string) (map[uint32]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	div := make(map[uint32]string, 4096)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		code, name, ok := splitDivisionLine(line)
		if !ok || name == "" {
			continue
		}
		div[code] = name
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return div, nil
}

func splitDivisionLine(line string) (uint32, string, bool) {
	sep := -1
	if i := strings.IndexByte(line, '\t'); i > 0 {
		sep = i
	} else if i := strings.Index(line, "  "); i > 0 {
		sep = i
	}
	if sep <= 0 {
		return 0, "", false
	}

	code, err := strconv.ParseUint(strings.TrimSpace(line[:sep]), 10, 32)
	if err != nil {
		return 0, "", false
	}
	name := strings.TrimSpace(line[sep:])
	return uint32(code), name, true
}

// toUint32 兼容 mmdb 解码出来的各种无符号类型。
func toUint32(v any) uint32 {
	switch typed := v.(type) {
	case uint32:
		return typed
	case uint64:
		return uint32(typed)
	case uint16:
		return uint32(typed)
	case uint:
		return uint32(typed)
	case int:
		if typed > 0 {
			return uint32(typed)
		}
	}
	return 0
}

// openGeoCNReader 打开 mmdb：默认 mmap，Preload 时整文件读进堆。
//
// FromBytes 不拷贝传入的切片，Reader 一直引用它；Close 对非 mmap 的 Reader
// 只清引用，不会释放这块内存。
func openGeoCNReader(cfg GeoCNConfig) (*maxminddb.Reader, error) {
	if cfg.Preload {
		data, err := os.ReadFile(cfg.Path)
		if err != nil {
			return nil, fmt.Errorf("ipfactory: preload geocn mmdb %q: %w", cfg.Path, err)
		}
		reader, err := maxminddb.FromBytes(data)
		if err != nil {
			return nil, fmt.Errorf("ipfactory: parse geocn mmdb %q: %w", cfg.Path, err)
		}
		return reader, nil
	}

	reader, err := maxminddb.Open(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("ipfactory: open geocn mmdb %q: %w", cfg.Path, err)
	}
	return reader, nil
}

var _ Backend = (*geocnBackend)(nil)
