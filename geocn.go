package ipfactory

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
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

	divPath := cfg.Divisions
	if divPath == "" {
		divPath = findDivisions(cfg.Path)
	}
	if divPath != "" {
		div, err := loadDivisions(divPath)
		if err != nil {
			if cfg.Divisions != "" {
				_ = reader.Close()
				return nil, fmt.Errorf("ipfactory: load geocn divisions %q: %w", divPath, err)
			}
			// 自动探测失败不致命：还有内置省级表兜底。
			div = nil
		}
		b.div = div
	}
	return b, nil
}

// findDivisions 在 mmdb 旁边找 GeoCN 的行政区划表：优先 full.txt，其次 short.txt。
func findDivisions(mmdbPath string) string {
	dir := filepath.Dir(mmdbPath)
	candidates := []string{
		filepath.Join(dir, "full.txt"),
		filepath.Join(dir, "short.txt"),
		filepath.Join(dir, "data", "full.txt"),
		filepath.Join(dir, "data", "short.txt"),
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
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

	// 记录里除了 division_code 还可能带 isp / country / city 等字段，
	// 统一走列名映射；未知列会落进 Extra。
	for key, value := range raw {
		mapColumn(res, key, value, nil)
	}

	// division_code 比记录里的文本字段更精确，放在最后覆盖。
	if code := toUint32(raw["division_code"]); code != 0 {
		province, city, district := b.splitDivision(code)
		if province != "" {
			res.Province = province
		}
		if city != "" {
			res.City = city
		}
		if district != "" {
			res.District = district
		}
	}

	if network != nil {
		if res.Extra == nil {
			res.Extra = make(map[string]any, 1)
		}
		res.Extra["network"] = network.String()
	}
	return res, nil
}

// splitDivision 把 6 位行政区划码拆成省 / 市 / 区县。
//
// 编码规则（GB/T 2260）：前两位省、中间两位市、后两位区县，全 0 表示没有下一级；
// GeoCN 的记录有的只到省（110000），有的到区县（110108）。
func (b *geocnBackend) splitDivision(code uint32) (province, city, district string) {
	provinceCode := code / 10000 * 10000
	cityCode := code / 100 * 100

	province = b.lookupDiv(provinceCode)
	if cityCode != provinceCode {
		city = b.lookupDiv(cityCode)
		// 直辖市的市级码一般写作「市辖区 / 县」，或与省同名；这类占位名
		// 统一用省名兜底，保证 City 与 xdb / awdb 的「北京市」写法对齐。
		switch city {
		case "", "市辖区", "县":
			city = province
		}
	}
	if code != cityCode && code != provinceCode {
		district = b.lookupDiv(code)
	}
	return province, city, district
}

// lookupDiv 查行政区划名：优先用外部表，其次用内置的省级兜底表。
func (b *geocnBackend) lookupDiv(code uint32) string {
	if name := b.div[code]; name != "" {
		return name
	}
	return builtinProvinces[code]
}

// builtinProvinces 是 34 个省级行政区划码的兜底名称。
// 没有提供 divisions 表时，至少能保证 Province（及直辖市的 City）有值；
// 市/区县名称仍需外部表。
var builtinProvinces = map[uint32]string{
	110000: "北京市",
	120000: "天津市",
	130000: "河北省",
	140000: "山西省",
	150000: "内蒙古自治区",
	210000: "辽宁省",
	220000: "吉林省",
	230000: "黑龙江省",
	310000: "上海市",
	320000: "江苏省",
	330000: "浙江省",
	340000: "安徽省",
	350000: "福建省",
	360000: "江西省",
	370000: "山东省",
	410000: "河南省",
	420000: "湖北省",
	430000: "湖南省",
	440000: "广东省",
	450000: "广西壮族自治区",
	460000: "海南省",
	500000: "重庆市",
	510000: "四川省",
	520000: "贵州省",
	530000: "云南省",
	540000: "西藏自治区",
	610000: "陕西省",
	620000: "甘肃省",
	630000: "青海省",
	640000: "宁夏回族自治区",
	650000: "新疆维吾尔自治区",
	710000: "台湾省",
	810000: "香港特别行政区",
	820000: "澳门特别行政区",
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
