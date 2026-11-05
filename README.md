# ipfactory

把两个离线 IP 库收敛成一套接口，按配置或文件内容**动态选择**用哪个，也可以**同时用**（主备链）。

| 后端 | 提供方 | 文件 |
| --- | --- | --- |
| `awdb` | [github.com/justseemore/awdb](https://github.com/justseemore/awdb) | `.awdb`（JSON 头 + 检索树） |
| `xdb` | [github.com/justseemore/ipgeo](https://github.com/justseemore/ipgeo) | ip2region v2 `.xdb`（v4 / v6 各一个） |

**不含热切换**（无 Reload / Watch / Swap / 引用计数）。需要运行期换库时，自己持有 `Backend` 做原子替换，或者直接用 `ipgeo` 自带的 `Provider`。

## 文件

```
backend.go   Backend 接口、Result、列名映射、统一错误
awdb.go      AWDB 适配器
xdb.go       ip2region xdb 适配器
chain.go     主备链（顺序回落）
factory.go   工厂：Config / OpenBackend / OpenChain
detect.go    按文件头判断格式
ipfactory_test.go
client.go    空占位（热切换层已移除，可删）
```

## 安装

```bash
go get github.com/justseemore/ipfactory
```

仓库里的 `go.mod` 不带版本号，第一次拉取时按**最新**解析：

```bash
cd ipfactory
go mod tidy          # 自动写入 awdb / ipgeo 的最新版本（没有 tag 时用伪版本）
go get -u ./...      # 以后想升到最新，再跑一次
```

两个依赖都在 GitHub 上公开（`justseemore/awdb`、`justseemore/ipgeo`），不需要 `replace`。
只有本地还没推送、想临时联调时才用 `go mod edit -replace ...=../awdb`，发版前删掉。

注：`ipgeo` 的 `go.mod` 声明了 `go 1.26.0`，所以本模块最低也是 `go 1.26.0`。

## 用法

### 1. 自动探测（推荐）

```go
backend, err := ipfactory.OpenBackend(ipfactory.Config{
	AWDB: ipfactory.AWDBConfig{Path: "data/ip.awdb"},
})
if err != nil {
	return err
}
defer backend.Close()

res, err := backend.LookupAddr(ctx, netip.MustParseAddr("114.114.114.114"))
if errors.Is(err, ipfactory.ErrNotFound) {
	// 未收录
}
fmt.Println(res.Country, res.Province, res.City)
```

`Kind` 留空（或 `"auto"`）时，`DetectKind` 会读文件头：前 2 字节是大端 JSON 长度且 JSON 里有 `node_count` / `columns` → `awdb`；否则按 `xdb`。

### 2. 同时用：主备链

```go
backend, err := ipfactory.OpenChain(
	// 先用官方 AWDB，未收录再查 xdb
	ipfactory.Config{Kind: "awdb", AWDB: ipfactory.AWDBConfig{Path: "data/ip.awdb"}},
	ipfactory.Config{Kind: "xdb", XDB: ipfactory.XDBConfig{
		V4Path: "data/ip2region_v4.xdb",
		V6Path: "data/ip2region_v6.xdb",
		LRU:    100000,
	}},
)
```

规则：按顺序查，第一个命中直接返回；全部未命中 → `ErrNotFound`；出现硬错误时记录第一个硬错误并继续尝试后面的后端，全失败则返回它。

适合“v4 只有 awdb、v6 只有 xdb”这种分工：不支持该地址族的后端会返回 `ErrNotFound`，链会自动往后走。

### 3. 强制指定后端

```go
ipfactory.Config{Kind: "awdb"} // 不看文件头，直接按 AWDB 打开
ipfactory.Config{Kind: "xdb"}  // 直接按 ip2region xdb 打开
```

## API

| 符号 | 说明 |
| --- | --- |
| `OpenBackend(Config) (Backend, error)` | 打开单个后端 |
| `OpenChain(...Config) (Backend, error)` | 打开并串成主备链（单个配置时直接返回后端） |
| `NewChain(name, ...Backend) *Chain` | 用现成后端拼链 |
| `DetectKind(path) (string, error)` | 只看头部判断格式 |
| `Backend.Lookup(ip string)` | 字符串入口，内部用 `context.Background()` |
| `Backend.LookupAddr(ctx, netip.Addr)` | 带 context 的查询入口 |
| `Backend.Name()` / `Backend.Close()` | 后端名 / 释放资源（重复调用安全） |
| `Config` | `Kind` + `AWDB` + `XDB` |
| `AWDBConfig` | `Path` / `Name` / `Aliases` |
| `XDBConfig` | `V4Path` / `V6Path` / `Name` / `Policy` / `LRU` / `Layout` |

## 结果映射

`Result` 把两家共有的字段提升成结构体字段：

```
IP Version Continent Country Province City District ISP NetworkType ISOCode IsPrivate Backend Extra
```

- **xdb**：`ipgeo.Info` 直接映射；`Raw`（原始 region 串）放进 `Extra["raw"]`。
- **awdb**：按列名映射。内置常见别名（`country` / `国家` / `country_name`、`iso_code` / `country_code` / `国家代码`、`运营商` 等），归一化规则是“小写 + 空格/`-`/`.` 转 `_`”。
- 不认识的列 → `Extra[原列名]`，嵌套列（多语言区划表）保留 `[]map[string]any` 结构，不丢信息。
- 列名对不上时用 `AWDBConfig.Aliases` 覆盖：

```go
AWDBConfig{
	Path:    "data/ip.awdb",
	Aliases: map[string]string{"continent_cn": "continent", "area_code": "district"},
}
```

## 错误契约

| 错误 | 含义 |
| --- | --- |
| `ErrNotFound` | 无记录；或该后端不支持这个地址族（交给链继续找） |
| `ErrInvalidIP` | 入参不是合法 IP |
| `ErrNoBackend` | 没配置任何后端 |

后端自身的硬错误（文件损坏、mmap 失败等）会原样返回，可用 `errors.Is` 判断其中包装的 `awdb.ErrCorrupt` / `ipgeo.ErrNotFound` 等。

## 需要知道的几点

1. **并发**：`Backend` 要求并发安全。`Chain`、`awdb.Reader`（读写均只读）和 `ipgeo.DB` 都满足；`Chain.Close` 不是并发安全的，不要在查询中途关。
2. **xdb 要区分 v4/v6**：只给 `V4Path` 时，v6 查询会返回 `ErrNotFound`，链会继续往后走。
3. **awdb 的 `4_6` 库**：v4 查询走 96 位偏移，由 `awdb` 内部处理，这里无需关心。
4. **不要为“换库”引一层定时器**：需要自动重载时，用 `ipgeo.Provider`（已有 SIGHUP / 定时刷新），或在自己的服务层重开 `Backend` 后原子替换。

## 测试

```bash
go vet ./... && go test -race ./...
```

测试不依赖真实库文件：`fakeBackend` 覆盖链式回落、硬错误、关闭、ctx 取消；`TestMapColumn` / `TestResultGet` 覆盖映射；`TestDetectKind` 用合成的 awdb 文件头验证探测。
ull` 三者行为完全一致。所以 `XDBConfig.Policy` 只是原样透传（上游恢复后自动生效）；真正有用的缓存是 `XDBConfig.LRU`：按 IP 的结果缓存，256 分片、FIFO 淘汰（命中不提升）。
6. **`Preload` 是 `ipgeo.WithPreload` 的透传**：打开时把 xdb 整文件读进堆（RSS ≈ 文件大小，v4/v6 各自计算），省掉运行期缺页，默认关闭（mmap + `MADV_RANDOM`）。本地联调借助仓库根的 `go.work` 指向本地 `ipgeo`；推到远端前要先发 `ipgeo` 那条改动，否则 `@latest` 拉不到 `WithPreload`。

## 测试

```bash
go vet ./... && go test -race ./...
```

测试不依赖真实库文件：`fakeBackend` 覆盖链式回落、硬错误、关闭、ctx 取消；`TestMapColumn` / `TestResultGet` 覆盖映射；`TestDetectKind` 用合成的 awdb 文件头验证探测。
