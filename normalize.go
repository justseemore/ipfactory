package ipfactory

import (
	"context"
	"net/netip"
	"strings"
)

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

// normalizeBackend 在查询返回前做一次归一化，由 OpenBackend 默认包上。
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

var _ Backend = normalizeBackend{}
