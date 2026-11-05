package ipfactory

import (
	"context"
	"net/netip"
	"strings"
)

// DefaultMissingPlaceholder 是 5 个契约字段缺失时的默认占位符，
// 可通过 Config.MissingPlaceholder 改写。
const DefaultMissingPlaceholder = "未知"

// 常见的“空值占位符”。不同库习惯不同：ip2region 写 "0"，有的写 "-"，
// 统一成空串后调用方无需再判。
var emptyPlaceholders = map[string]struct{}{
	"0":       {},
	"-":       {},
	"_":       {},
	"null":    {},
	"nil":     {},
	"none":    {},
	"n/a":     {},
	"na":      {},
	"unknown": {},
	"未知":      {},
}

// Normalize 把 Result 里的字符串字段统一成一致形态：
//
//   - 去首尾空白（含全角空格 U+3000）
//   - "0" / "-" / "null" / "unknown" / "未知" 之类的占位符统一成空串
//   - Country 归一成中文（china / cn / chn / prc / 中华人民共和国 → 中国）
//   - ISOCode 统一大写；Country 为中国但没给 ISO 码时补 "CN"
//
// 归一化是幂等的，可以被重复调用。它只改标量字段，不动 Extra。
func Normalize(res *Result) {
	if res == nil {
		return
	}

	res.Country = normalizeText(res.Country)
	res.Province = normalizeText(res.Province)
	res.City = normalizeText(res.City)
	res.District = normalizeText(res.District)
	res.ISP = normalizeText(res.ISP)
	res.NetworkType = normalizeText(res.NetworkType)
	res.Continent = normalizeText(res.Continent)

	switch strings.ToLower(res.Country) {
	case "china", "cn", "chn", "prc", "中国", "中华人民共和国":
		res.Country = "中国"
	}

	res.ISOCode = strings.ToUpper(normalizeText(res.ISOCode))
	if res.ISOCode == "" && res.Country == "中国" {
		res.ISOCode = "CN"
	}
}

// EnsureFields 把 5 个契约字段（country / province / city / district / isp）
// 补齐成占位符，保证不论哪个后端、哪条数据，返回结构都完整。
//
// placeholder 为空时不做任何事（相当于保留空串）。
func EnsureFields(res *Result, placeholder string) {
	if res == nil || placeholder == "" {
		return
	}
	if res.Country == "" {
		res.Country = placeholder
	}
	if res.Province == "" {
		res.Province = placeholder
	}
	if res.City == "" {
		res.City = placeholder
	}
	if res.District == "" {
		res.District = placeholder
	}
	if res.ISP == "" {
		res.ISP = placeholder
	}
}

func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\u3000", " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if _, ok := emptyPlaceholders[strings.ToLower(s)]; ok {
		return ""
	}
	return s
}

// normalizeBackend 只做值归一化，不做占位补齐。
//
// 链式查询里每个后端都是这一层，这样 Chain 才能看到“真正的空值”去做补齐。
type normalizeBackend struct {
	Backend
}

func (n normalizeBackend) Lookup(ip string) (*Result, error) {
	res, err := n.Backend.Lookup(ip)
	if err != nil {
		return nil, err
	}
	Normalize(res)
	return res, nil
}

func (n normalizeBackend) LookupAddr(ctx context.Context, addr netip.Addr) (*Result, error) {
	res, err := n.Backend.LookupAddr(ctx, addr)
	if err != nil {
		return nil, err
	}
	Normalize(res)
	return res, nil
}

// ensureBackend 只做占位补齐，永远在归一化之后（单后端或整条链之后）。
type ensureBackend struct {
	Backend
	placeholder string
}

func (e ensureBackend) Lookup(ip string) (*Result, error) {
	res, err := e.Backend.Lookup(ip)
	if err != nil {
		return nil, err
	}
	EnsureFields(res, e.placeholder)
	return res, nil
}

func (e ensureBackend) LookupAddr(ctx context.Context, addr netip.Addr) (*Result, error) {
	res, err := e.Backend.LookupAddr(ctx, addr)
	if err != nil {
		return nil, err
	}
	EnsureFields(res, e.placeholder)
	return res, nil
}

var (
	_ Backend = normalizeBackend{}
	_ Backend = ensureBackend{}
)
