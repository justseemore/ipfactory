package ipfactory

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// fakeBackend 是最小可测后端：命中返 res，否则返具体错误。
type fakeBackend struct {
	name   string
	res    *Result
	err    error
	closed atomic.Int32
}

func (f *fakeBackend) Name() string { return f.name }

func (f *fakeBackend) LookupAddr(ctx context.Context, addr netip.Addr) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.res == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, addr)
	}
	out := *f.res
	if out.IP == "" {
		out.IP = addr.String()
	}
	if out.Backend == "" {
		out.Backend = f.name
	}
	return &out, nil
}

func (f *fakeBackend) Lookup(ip string) (*Result, error) {
	addr, err := parseIP(ip)
	if err != nil {
		return nil, err
	}
	return f.LookupAddr(context.Background(), addr)
}

func (f *fakeBackend) Close() error {
	f.closed.Add(1)
	return nil
}

func (f *fakeBackend) isClosed() bool { return f.closed.Load() > 0 }

func TestChainFallback(t *testing.T) {
	primary := &fakeBackend{name: "awdb"} // 未收录
	backup := &fakeBackend{name: "xdb", res: &Result{Country: "中国", City: "北京"}}

	chain := NewChain("", primary, backup)
	defer chain.Close()

	res, err := chain.LookupAddr(context.Background(), netip.MustParseAddr("1.2.3.4"))
	if err != nil {
		t.Fatalf("LookupAddr: %v", err)
	}
	if res.Backend != "xdb" {
		t.Errorf("Backend = %q, want xdb", res.Backend)
	}
	if res.City != "北京" {
		t.Errorf("City = %q", res.City)
	}
	if chain.Name() != "chain" {
		t.Errorf("Name() = %q, want chain", chain.Name())
	}
}

func TestChainAllNotFound(t *testing.T) {
	chain := NewChain("", &fakeBackend{name: "a"}, &fakeBackend{name: "b"})

	_, err := chain.LookupAddr(context.Background(), netip.MustParseAddr("9.9.9.9"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestChainHardError(t *testing.T) {
	boom := errors.New("boom")
	chain := NewChain("", &fakeBackend{name: "broken", err: boom}, &fakeBackend{name: "empty"})

	_, err := chain.LookupAddr(context.Background(), netip.MustParseAddr("1.2.3.4"))
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want boom", err)
	}
}

func TestChainPrefersFirstHit(t *testing.T) {
	first := &fakeBackend{name: "first", res: &Result{Country: "A"}}
	second := &fakeBackend{name: "second", res: &Result{Country: "B"}}

	chain := NewChain("", first, second)
	defer chain.Close()

	res, err := chain.LookupAddr(context.Background(), netip.MustParseAddr("1.2.3.4"))
	if err != nil {
		t.Fatalf("LookupAddr: %v", err)
	}
	if res.Country != "A" {
		t.Errorf("Country = %q, want A (第一个命中的后端)", res.Country)
	}
}

func TestChainCloseClosesAll(t *testing.T) {
	a := &fakeBackend{name: "a"}
	b := &fakeBackend{name: "b"}
	chain := NewChain("", a, nil, b)

	if err := chain.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !a.isClosed() || !b.isClosed() {
		t.Fatalf("backends not closed: a=%v b=%v", a.isClosed(), b.isClosed())
	}
}

func TestChainContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	chain := NewChain("", &fakeBackend{name: "a", res: &Result{Country: "A"}})
	if _, err := chain.LookupAddr(ctx, netip.MustParseAddr("1.2.3.4")); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestChainEmpty(t *testing.T) {
	chain := NewChain("")
	if _, err := chain.LookupAddr(context.Background(), netip.MustParseAddr("1.2.3.4")); !errors.Is(err, ErrNoBackend) {
		t.Fatalf("error = %v, want ErrNoBackend", err)
	}
}

func TestOpenBackendErrors(t *testing.T) {
	if _, err := OpenBackend(Config{}); !errors.Is(err, ErrNoBackend) {
		t.Errorf("empty config error = %v, want ErrNoBackend", err)
	}
	if _, err := OpenBackend(Config{Kind: "bogus"}); err == nil {
		t.Error("unknown kind accepted")
	}
	if _, err := OpenChain(); !errors.Is(err, ErrNoBackend) {
		t.Errorf("OpenChain() error = %v, want ErrNoBackend", err)
	}
}

func TestOpenBackendMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.awdb")
	if _, err := OpenBackend(Config{AWDB: AWDBConfig{Path: missing}}); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestMapColumn(t *testing.T) {
	res := &Result{}
	aliases := map[string]string{"country_cn": "country"}

	mapColumn(res, "Country-CN", "中国", aliases)
	if res.Country != "中国" {
		t.Errorf("aliases 覆盖失败: Country = %q", res.Country)
	}

	mapColumn(res, "ISO-Code", "cn", nil)
	if res.ISOCode != "CN" {
		t.Errorf("ISOCode = %q, want CN", res.ISOCode)
	}

	mapColumn(res, "城市", "北京", nil)
	if res.City != "北京" {
		t.Errorf("City = %q", res.City)
	}

	nested := []map[string]any{{"code": "100000"}}
	mapColumn(res, "multi_area", nested, nil)
	if _, ok := res.Extra["multi_area"]; !ok {
		t.Errorf("未知列应进 Extra, Extra = %v", res.Extra)
	}
}

func TestResultGet(t *testing.T) {
	res := &Result{IP: "1.2.3.4", Version: 4, Country: "中国", Extra: map[string]any{"isp_name": "电信"}}

	if v, ok := res.Get("country"); !ok || v != "中国" {
		t.Errorf("Get(country) = %v, %v", v, ok)
	}
	if v, ok := res.Get("ISP_NAME"); !ok || v != "电信" {
		t.Errorf("Get(ISP_NAME) = %v, %v", v, ok)
	}
	if _, ok := res.Get("missing"); ok {
		t.Error("Get(missing) 不应命中")
	}

	if got := res.String(); got != "中国" {
		t.Errorf("String() = %q", got)
	}

	var nilResult *Result
	if _, ok := nilResult.Get("x"); ok || nilResult.String() != "" {
		t.Error("nil Result 应安全返回")
	}
}

func TestDetectKind(t *testing.T) {
	dir := t.TempDir()

	header := []byte(`{"node_count":2,"ip_version":"4","decode_type":2,"byte_len":3,"columns":["country"]}`)
	awdbPath := filepath.Join(dir, "a.awdb")
	buf := make([]byte, 2+len(header))
	binary.BigEndian.PutUint16(buf[:2], uint16(len(header)))
	copy(buf[2:], header)
	if err := os.WriteFile(awdbPath, buf, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if kind, err := DetectKind(awdbPath); err != nil || kind != KindAWDB {
		t.Errorf("DetectKind(awdb) = %q, %v", kind, err)
	}

	xdbPath := filepath.Join(dir, "b.xdb")
	if err := os.WriteFile(xdbPath, make([]byte, 512), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if kind, err := DetectKind(xdbPath); err != nil || kind != KindXDB {
		t.Errorf("DetectKind(xdb) = %q, %v", kind, err)
	}

	if _, err := DetectKind(filepath.Join(dir, "missing.bin")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestChainLookup(t *testing.T) {
	chain := NewChain("", &fakeBackend{name: "a"}, &fakeBackend{name: "b", res: &Result{City: "北京"}})
	defer chain.Close()

	res, err := chain.Lookup("1.2.3.4")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if res.City != "北京" {
		t.Errorf("City = %q", res.City)
	}

	// 前后空格应当被忽略
	if _, err := chain.Lookup("  1.2.3.4  "); err != nil {
		t.Errorf("Lookup(带空格) error = %v", err)
	}

	if _, err := chain.Lookup("not-an-ip"); !errors.Is(err, ErrInvalidIP) {
		t.Errorf("Lookup(invalid) error = %v, want ErrInvalidIP", err)
	}
}

func TestSplitDivisionLine(t *testing.T) {
	code, name, ok := splitDivisionLine("110000\t北京市")
	if !ok || code != 110000 || name != "北京市" {
		t.Errorf("tab 格式: %d %q %v", code, name, ok)
	}

	code, name, ok = splitDivisionLine("110108  海淀区")
	if !ok || code != 110108 || name != "海淀区" {
		t.Errorf("双空格格式: %d %q %v", code, name, ok)
	}

	if _, _, ok := splitDivisionLine("garbage"); ok {
		t.Error("无分隔符的行不应被接受")
	}
}

func TestGeoCNDivisionSplit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "full.txt")
	content := "110000\t北京市\n110100\t市辖区\n110108\t海淀区\n310000\t上海市\n310115\t浦东新区\n440000\t广东省\n440300\t深圳市\n440305\t南山区\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	div, err := loadDivisions(path)
	if err != nil {
		t.Fatalf("loadDivisions: %v", err)
	}
	b := &geocnBackend{div: div}

	cases := []struct {
		code                     uint32
		province, city, district string
	}{
		{110108, "北京市", "北京市", "海淀区"},
		{110100, "北京市", "北京市", ""},
		{310115, "上海市", "上海市", "浦东新区"},
		{440305, "广东省", "深圳市", "南山区"},
		{440300, "广东省", "深圳市", ""},
		{440000, "广东省", "", ""},
	}
	for _, tc := range cases {
		p, c, d := b.splitDivision(tc.code)
		if p != tc.province || c != tc.city || d != tc.district {
			t.Errorf("splitDivision(%d) = %q/%q/%q, want %q/%q/%q",
				tc.code, p, c, d, tc.province, tc.city, tc.district)
		}
	}

	// 没有行政区划表时只留空，不能 panic
	empty := &geocnBackend{}
	if p, c, d := empty.splitDivision(110108); p != "" || c != "" || d != "" {
		t.Errorf("empty table: %q/%q/%q", p, c, d)
	}
}

func TestOpenGeoCNErrors(t *testing.T) {
	if _, err := OpenGeoCN(GeoCNConfig{}); err == nil {
		t.Error("empty GeoCNConfig accepted")
	}
	if _, err := OpenGeoCN(GeoCNConfig{Path: filepath.Join(t.TempDir(), "none.mmdb")}); err == nil {
		t.Error("missing mmdb accepted")
	}
}

func TestOpenGeoCNPreloadInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.mmdb")
	if err := os.WriteFile(path, []byte("not an mmdb"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// 两条路径都要报错：mmap 与非 mmap 的校验必须一致
	if _, err := OpenGeoCN(GeoCNConfig{Path: path, Preload: true}); err == nil {
		t.Error("invalid mmdb accepted with Preload")
	}
	if _, err := OpenGeoCN(GeoCNConfig{Path: path}); err == nil {
		t.Error("invalid mmdb accepted")
	}
}

func TestNormalize(t *testing.T) {
	res := &Result{
		Country:     "China",
		Province:    "\u3000北京市 ",
		City:        "0",
		District:    "-",
		ISP:         "电信",
		NetworkType: "unknown",
		ISOCode:     "cn",
	}
	Normalize(res)

	if res.Country != "中国" {
		t.Errorf("Country = %q, want 中国", res.Country)
	}
	if res.Province != "北京市" {
		t.Errorf("Province = %q, want 北京市", res.Province)
	}
	if res.City != "" || res.District != "" || res.NetworkType != "" {
		t.Errorf("占位符未清空: city=%q district=%q net=%q", res.City, res.District, res.NetworkType)
	}
	if res.ISP != "电信" {
		t.Errorf("ISP = %q", res.ISP)
	}
	if res.ISOCode != "CN" {
		t.Errorf("ISOCode = %q, want CN", res.ISOCode)
	}

	// 幂等
	Normalize(res)
	if res.Country != "中国" || res.ISOCode != "CN" {
		t.Errorf("Normalize 不幂等: %+v", res)
	}

	// 只有国家名时补 ISO 码
	only := &Result{Country: "中华人民共和国"}
	Normalize(only)
	if only.Country != "中国" || only.ISOCode != "CN" {
		t.Errorf("only country: %+v", only)
	}

	// nil 安全
	Normalize(nil)
}

func TestEnsureFields(t *testing.T) {
	res := &Result{Country: "中国", Province: ""}
	EnsureFields(res, DefaultMissingPlaceholder)
	if res.Province != "未知" || res.City != "未知" || res.District != "未知" || res.ISP != "未知" {
		t.Errorf("补齐失败: %+v", res)
	}
	if res.Country != "中国" {
		t.Errorf("已有值被覆盖: %q", res.Country)
	}

	// 占位符为空时不动
	other := &Result{Country: "中国"}
	EnsureFields(other, "")
	if other.City != "" {
		t.Errorf("placeholder 为空却补齐了: %q", other.City)
	}

	EnsureFields(nil, DefaultMissingPlaceholder)
}

func TestNormalizeBackend(t *testing.T) {
	raw := &fakeBackend{name: "raw", res: &Result{Country: "CN", Province: "北京市", City: "0"}}
	var backend Backend = normalizeBackend{Backend: raw}

	res, err := backend.Lookup("1.2.3.4")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if res.Country != "中国" || res.City != "" {
		t.Errorf("未归一化: %+v", res)
	}
	if res.Backend != "raw" || backend.Name() != "raw" {
		t.Errorf("Backend/Name 被破坏: %q %q", res.Backend, backend.Name())
	}
}

func TestChainMergeMissing(t *testing.T) {
	// 主后端只到区，缺 City；备后端有 City 和 ISP
	primary := func() Backend {
		return &fakeBackend{name: "geocn", res: &Result{
			Country: "中国", Province: "北京市", District: "海淀区",
		}}
	}
	backup := func() Backend {
		return &fakeBackend{name: "xdb", res: &Result{
			Country: "中国", Province: "北京市", City: "北京市", ISP: "电信",
		}}
	}

	merged, err := NewChain("", primary(), backup()).SetMergeMissing(true).Lookup("1.2.3.4")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if merged.City != "北京市" {
		t.Errorf("City = %q, 应从后续后端补齐", merged.City)
	}
	if merged.District != "海淀区" {
		t.Errorf("District = %q, 不应被覆盖", merged.District)
	}
	if merged.ISP != "电信" {
		t.Errorf("ISP = %q", merged.ISP)
	}
	if merged.Backend != "geocn" {
		t.Errorf("Backend = %q, 应保留基准后端", merged.Backend)
	}

	plain, err := NewChain("", primary(), backup()).Lookup("1.2.3.4")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if plain.City != "" {
		t.Errorf("未开 MergeMissing 却补齐了: City = %q", plain.City)
	}
}
