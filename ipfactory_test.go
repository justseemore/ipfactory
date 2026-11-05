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
