// Package ipfactory 把两类离线 IP 库后端收敛成一套接口：
//
//   - AWDB（github.com/justseemore/awdb）：JSON 头 + 检索树的二进制库
//   - XDB （github.com/justseemore/ipgeo）：ip2region v2 xdb，v4/v6 两个文件
//
// 它只做三件事：
//
//	OpenBackend / OpenChain  工厂：按配置或文件内容选择后端，可组合成主备链
//	Chain                    顺序回落：第一个命中即返回，适合“多库叠加”
//	Result                   后端无关的统一结果
//
// 不含热切换（Reload / Watch / Swap）：需要运行期换库时，自己持有 Backend
// 并在适配层做原子替换，或直接使用 ipgeo 自带的 Provider。
//
// 典型用法：
//
//	backend, err := ipfactory.OpenChain(
//		ipfactory.Config{AWDB: ipfactory.AWDBConfig{Path: "ip.awdb"}},
//		ipfactory.Config{XDB: ipfactory.XDBConfig{V4Path: "v4.xdb", V6Path: "v6.xdb"}},
//	)
//	if err != nil {
//		return err
//	}
//	defer backend.Close()
//
//	res, err := backend.Lookup("8.8.8.8")
//	if errors.Is(err, ipfactory.ErrNotFound) {
//		// 所有后端都未收录
//	}
//	fmt.Println(res.Country, res.Province, res.City)
package ipfactory

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// 统一错误。后端自身的错误会被包装进来，因此用 errors.Is 判断即可。
var (
	// ErrNotFound 表示没有该地址的记录；地址族不被某个后端支持时也归到
	// 这类，方便主备链继续向后找。
	ErrNotFound = errors.New("ipfactory: no record for the address")

	// ErrInvalidIP 表示入参不是合法 IP。
	ErrInvalidIP = errors.New("ipfactory: invalid ip address")

	// ErrNoBackend 表示没有配置任何后端。
	ErrNoBackend = errors.New("ipfactory: no backend configured")
)

// Result 是后端无关的查询结果。
//
// 常见地理字段被提升为结构体字段，不同库特有的列放进 Extra，因此换后端不会
// 让调用方代码崩掉。
type Result struct {
	IP          string         `json:"ip"`
	Version     int            `json:"version"`
	Continent   string         `json:"continent,omitempty"`
	Country     string         `json:"country,omitempty"`
	Province    string         `json:"province,omitempty"`
	City        string         `json:"city,omitempty"`
	District    string         `json:"district,omitempty"`
	ISP         string         `json:"isp,omitempty"`
	NetworkType string         `json:"network_type,omitempty"`
	ISOCode     string         `json:"iso_code,omitempty"`
	IsPrivate   bool           `json:"is_private,omitempty"`
	Backend     string         `json:"backend,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
}

// String 返回 “国家|省|市|区县|运营商” 的紧凑表示。
func (r *Result) String() string {
	if r == nil {
		return ""
	}
	parts := make([]string, 0, 5)
	for _, s := range []string{r.Country, r.Province, r.City, r.District, r.ISP} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "|")
}

// Get 先查常见字段，再查 Extra（大小写不敏感）。
func (r *Result) Get(key string) (any, bool) {
	if r == nil {
		return nil, false
	}
	switch normalizeKey(key) {
	case "ip":
		return r.IP, true
	case "version":
		return r.Version, true
	case "continent":
		return r.Continent, true
	case "country":
		return r.Country, true
	case "province", "region", "state":
		return r.Province, true
	case "city":
		return r.City, true
	case "district", "area":
		return r.District, true
	case "isp":
		return r.ISP, true
	case "network_type", "net_type":
		return r.NetworkType, true
	case "iso_code", "country_code":
		return r.ISOCode, true
	case "is_private":
		return r.IsPrivate, true
	case "backend":
		return r.Backend, true
	}
	if v, ok := r.Extra[key]; ok {
		return v, true
	}
	for k, v := range r.Extra {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return nil, false
}

// Backend 是一个可查询、可关闭的 IP 库后端。
//
// 实现必须并发安全：同一个 Backend 会被多个 goroutine 同时调用，直到 Close
// 之后才保证不再被调用。
type Backend interface {
	// Name 是后端标识，会写进 Result.Backend，便于排查是哪个库命中的。
	Name() string

	// Lookup 是字符串入口，等价于 LookupAddr(context.Background(), addr)。
	Lookup(ip string) (*Result, error)

	// LookupAddr 查询一个地址。未收录返回包装了 ErrNotFound 的错误。
	LookupAddr(ctx context.Context, addr netip.Addr) (*Result, error)

	// Close 释放资源，重复调用必须安全。
	Close() error
}

// 常见列名别名。键是归一化后的列名（小写、空格/连字符/点替换为下划线），
// 值是 Result 的字段名。AWDB 库的列名在不同产品线里差异很大，所以留了
// AWDBConfig.Aliases 做覆盖。
var defaultAliases = map[string]string{
	"continent":     "continent",
	"大洲":            "continent",
	"country":       "country",
	"country_name":  "country",
	"国家":            "country",
	"province":      "province",
	"province_name": "province",
	"region":        "province",
	"state":         "province",
	"省":             "province",
	"省份":            "province",
	"city":          "city",
	"city_name":     "city",
	"城市":            "city",
	"district":      "district",
	"area":          "district",
	"区县":            "district",
	"isp":           "isp",
	"isp_name":      "isp",
	"运营商":           "isp",
	"network_type":  "network_type",
	"net_type":      "network_type",
	"网络类型":          "network_type",
	"iso_code":      "iso_code",
	"country_code":  "iso_code",
	"country_iso":   "iso_code",
	"国家代码":          "iso_code",
}

// normalizeKey 归一化列名。
func normalizeKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.NewReplacer("-", "_", " ", "_", ".", "_").Replace(s)
}

// mapColumn 把一列写入 Result：已知列落到结构体字段，未知列进 Extra。
func mapColumn(res *Result, key string, value any, aliases map[string]string) {
	normalized := normalizeKey(key)

	target := ""
	if aliases != nil {
		target = aliases[normalized]
	}
	if target == "" {
		target = defaultAliases[normalized]
	}
	if target == "" {
		if res.Extra == nil {
			res.Extra = make(map[string]any, 4)
		}
		res.Extra[key] = value
		return
	}

	text := stringify(value)
	switch target {
	case "continent":
		res.Continent = text
	case "country":
		res.Country = text
	case "province":
		res.Province = text
	case "city":
		res.City = text
	case "district":
		res.District = text
	case "isp":
		res.ISP = text
	case "network_type":
		res.NetworkType = text
	case "iso_code":
		res.ISOCode = strings.ToUpper(text)
	default:
		if res.Extra == nil {
			res.Extra = make(map[string]any, 4)
		}
		res.Extra[key] = value
	}
}

// stringify 把任意标量转成字符串；无法转换时退回 fmt.Sprint。
func stringify(v any) string {
	switch typed := v.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	}
	return fmt.Sprint(v)
}

// parseIP 解析字符串 IP，失败时返回包装了 ErrInvalidIP 的错误。
func parseIP(ip string) (netip.Addr, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("%w: %q", ErrInvalidIP, ip)
	}
	return addr, nil
}
